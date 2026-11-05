package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/api"
	"github.com/ArbitrageScanner/stellar-indexer/internal/config"
	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/store"
	"github.com/ArbitrageScanner/stellar-indexer/web"
)

func apiConfig(cfg config.Config) api.Config {
	var ui fs.FS
	switch {
	case cfg.UIDir != "":
		ui = os.DirFS(cfg.UIDir)
	case cfg.UI:
		ui = web.FS()
	}
	return api.Config{
		Network: cfg.Network, CursorName: cfg.CursorName, USDAsset: cfg.USDAsset, UI: ui,
		LabelsToken: cfg.LabelsToken, LabelsOpen: cfg.LabelsOpen, APIKeys: cfg.APIKeys, CORSOrigins: cfg.CORSOrigins,
		RateRPS: cfg.RateRPS, RateBurst: cfg.RateBurst, TrustProxy: cfg.TrustProxy, CacheTTL: cfg.CacheTTL, ReadyMaxLag: cfg.ReadyMaxLag,
	}
}

// serveAPI starts the HTTP API; the returned func shuts it down gracefully.
func serveAPI(ctx context.Context, cfg config.Config, db *store.Store, log *slog.Logger, errc chan<- error) func() {
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.New(db.Pool, log, apiConfig(cfg)).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	return func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}
}

// serveMetrics exposes /metrics and /health for the fetch and write processes.
func serveMetrics(cfg config.Config, log *slog.Logger) func() {
	if cfg.MetricsAddr == "" || cfg.MetricsAddr == "off" {
		return func() {}
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	srv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server", "err", err)
		}
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}
