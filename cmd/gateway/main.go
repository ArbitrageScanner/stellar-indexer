// Command gateway is the public edge of a split deployment: it serves the
// explorer UI and proxies the read API to an internal `indexer api` over a
// private network. It contains no database or ingestion code and needs no
// credentials other than the internal API key / client certificate.
//
//	UPSTREAM_URL=http://10.77.0.2:8080 UPSTREAM_API_KEY=... gateway
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/gateway"
	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
	"github.com/ArbitrageScanner/stellar-indexer/web"
)

func main() {
	log := newLogger(env("LOG_LEVEL", "info"), env("LOG_FORMAT", "text"))
	if err := run(log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, addr, metricsAddr, certFile, keyFile, err := load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	g, err := gateway.New(cfg, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: addr, Handler: g.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: cfg.Timeout + 15*time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 32 << 10}
	errc := make(chan error, 2)
	go func() {
		log.Info("gateway listening", "addr", addr, "upstream", cfg.Upstream.Redacted(), "ui", cfg.UI != nil,
			"upstream_tls", cfg.UpstreamTLS != nil, "public_keys", len(cfg.APIKeys), "blocked", cfg.Block)
		var err error
		if certFile != "" {
			err = srv.ListenAndServeTLS(certFile, keyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	// Metrics on a separate (private) listener, never on the public one.
	var msrv *http.Server
	if metricsAddr != "" && metricsAddr != "off" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", metrics.Handler())
		msrv = &http.Server{Addr: metricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := msrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	if msrv != nil {
		_ = msrv.Shutdown(sctx)
	}
	return err
}

func load() (cfg gateway.Config, addr, metricsAddr, certFile, keyFile string, err error) {
	var errs []error
	u, perr := url.Parse(os.Getenv("UPSTREAM_URL"))
	if perr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		errs = append(errs, errors.New("UPSTREAM_URL must be http(s)://host:port of the internal indexer API"))
	}
	cfg.Upstream = u
	cfg.UpstreamKey = os.Getenv("UPSTREAM_API_KEY")
	cfg.UpstreamTLS, perr = gateway.UpstreamTLS(os.Getenv("UPSTREAM_CA_FILE"), os.Getenv("UPSTREAM_CERT_FILE"),
		os.Getenv("UPSTREAM_KEY_FILE"), os.Getenv("UPSTREAM_SERVER_NAME"))
	if perr != nil {
		errs = append(errs, perr)
	}
	cfg.Timeout = envDuration("UPSTREAM_TIMEOUT", 30*time.Second)
	if envBool("UI_ENABLED", true) {
		ui := web.FS()
		if d := os.Getenv("UI_DIR"); d != "" {
			ui = os.DirFS(d)
		}
		cfg.UI = ui
	}
	cfg.APIKeys = envList("API_KEYS", nil)
	cfg.CORSOrigins = envList("CORS_ORIGINS", []string{"*"})
	cfg.RateRPS = envFloat("RATE_LIMIT_RPS", 20)
	cfg.RateBurst = int(envFloat("RATE_LIMIT_BURST", 40))
	cfg.TrustProxy = envBool("TRUST_PROXY", false)
	cfg.CacheTTL = envDuration("EDGE_CACHE_TTL", 5*time.Second)
	cfg.Block = envList("BLOCK_PATHS", gateway.DefaultBlock)
	cfg.MaxBody = int64(envFloat("MAX_BODY_BYTES", 16<<10))
	if cfg.Upstream != nil && cfg.Upstream.Scheme == "http" && cfg.UpstreamKey == "" {
		errs = append(errs, errors.New("UPSTREAM_API_KEY is required for a plain-http upstream (the internal API must not be open)"))
	}
	certFile, keyFile = os.Getenv("TLS_CERT_FILE"), os.Getenv("TLS_KEY_FILE")
	if (certFile == "") != (keyFile == "") {
		errs = append(errs, errors.New("TLS_CERT_FILE and TLS_KEY_FILE go together"))
	}
	return cfg, env("HTTP_ADDR", ":8080"), env("METRICS_ADDR", "127.0.0.1:9100"), certFile, keyFile, errors.Join(errs...)
}

func newLogger(level, format string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	opts := &slog.HandlerOptions{Level: l}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if b, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return b
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return f
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return d
	}
	return def
}

func envList(k string, def []string) []string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
