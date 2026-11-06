// Package gateway is the public edge of a split deployment: it serves the
// explorer UI and reverse-proxies the read API to an internal `indexer api`
// on a private network (WireGuard/SSH tunnel), so the indexer server needs
// no inbound internet connections and the edge holds no database
// credentials.
//
// The gateway authenticates itself upstream with an internal API key and,
// optionally, a client certificate (mTLS); public clients never see either.
// It applies edge rate limiting, a short response cache, CORS and security
// headers, blocks internal endpoints, and rewrites X-Forwarded-For so the
// upstream can trust the client IP it reports.
package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/httpx"
	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
)

type Config struct {
	Upstream    *url.URL      // internal API base, e.g. http://10.77.0.2:8080
	UpstreamKey string        // sent as X-API-Key (the internal API's API_KEYS)
	UpstreamTLS *tls.Config   // CA / client certificate for an https upstream
	Timeout     time.Duration // per upstream request

	UI          fs.FS    // explorer; nil = API only
	APIKeys     []string // optional public keys required from clients
	CORSOrigins []string
	RateRPS     float64
	RateBurst   int
	TrustProxy  bool          // behind a TLS terminator (Caddy) that sets X-Forwarded-For
	CacheTTL    time.Duration // edge cache for GETs; 0 = off
	Block       []string      // path prefixes never proxied
	MaxBody     int64         // request body limit (label writes)
}

// DefaultBlock hides internal endpoints from the public.
var DefaultBlock = []string{"/metrics", "/stats/tables"}

type Gateway struct {
	cfg     Config
	log     *slog.Logger
	proxy   *httputil.ReverseProxy
	client  *http.Client
	cache   *httpx.Cache
	limiter *httpx.RateLimiter
}

func New(cfg Config, log *slog.Logger) (*Gateway, error) {
	if cfg.Upstream == nil || cfg.Upstream.Host == "" {
		return nil, errors.New("upstream URL is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = 16 << 10
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = cfg.UpstreamTLS
	tr.ResponseHeaderTimeout = cfg.Timeout
	tr.MaxIdleConnsPerHost = 64
	g := &Gateway{cfg: cfg, log: log, client: &http.Client{Transport: tr, Timeout: cfg.Timeout}}
	g.proxy = &httputil.ReverseProxy{
		Transport:      tr,
		Rewrite:        g.rewrite,
		ModifyResponse: g.modifyResponse,
		ErrorHandler:   g.upstreamError,
	}
	if cfg.CacheTTL > 0 {
		g.cache = httpx.NewCache(cfg.CacheTTL, 2048)
	}
	if cfg.RateRPS > 0 {
		burst := cfg.RateBurst
		if burst <= 0 {
			burst = int(2*cfg.RateRPS) + 1
		}
		g.limiter = httpx.NewRateLimiter(cfg.RateRPS, burst)
	}
	return g, nil
}

// rewrite targets the upstream and replaces client-controlled identity
// headers with ones the upstream can trust.
func (g *Gateway) rewrite(pr *httputil.ProxyRequest) {
	pr.SetURL(g.cfg.Upstream)
	pr.Out.Host = g.cfg.Upstream.Host
	pr.Out.Header.Del("X-API-Key")
	if g.cfg.UpstreamKey != "" {
		pr.Out.Header.Set("X-API-Key", g.cfg.UpstreamKey)
	}
	// Only the label write policy uses Authorization upstream; never forward it elsewhere.
	if !strings.HasPrefix(pr.In.URL.Path, "/labels") || pr.In.Method == http.MethodGet {
		pr.Out.Header.Del("Authorization")
	}
	pr.Out.Header.Del("Forwarded")
	pr.Out.Header.Del("X-Forwarded-Host")
	pr.Out.Header.Del("X-Forwarded-Proto")
	pr.Out.Header.Set("X-Forwarded-For", httpx.ClientIP(pr.In, g.cfg.TrustProxy))
	pr.Out.Header.Set("X-Request-ID", httpx.RequestIDOf(pr.In.Context()))
}

// modifyResponse drops upstream headers the gateway owns (CORS, security,
// request id) so they are not duplicated.
func (g *Gateway) modifyResponse(resp *http.Response) error {
	for k := range resp.Header {
		if strings.HasPrefix(k, "Access-Control-") {
			resp.Header.Del(k)
		}
	}
	for _, k := range []string{"X-Request-Id", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Content-Security-Policy", "Www-Authenticate"} {
		resp.Header.Del(k)
	}
	labelWrite := strings.HasPrefix(resp.Request.URL.Path, "/labels") && resp.Request.Method != http.MethodGet
	if resp.StatusCode == http.StatusUnauthorized && !labelWrite {
		// The upstream rejected the gateway's own credentials: a deployment
		// error, not the client's. Answer 502 without the upstream's message.
		// (A 401 on a label write is about the client's LABELS_TOKEN and passes through.)
		g.log.Error("upstream rejected gateway credentials (check UPSTREAM_API_KEY / client certificate)", "path", resp.Request.URL.Path)
		metrics.UpstreamErrors.WithLabelValues("auth").Inc()
		_ = resp.Body.Close()
		body := `{"error":"indexer API unavailable"}`
		resp.StatusCode, resp.Status = http.StatusBadGateway, "502 Bad Gateway"
		resp.Body = io.NopCloser(strings.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header = http.Header{"Content-Type": {"application/json"}, "Content-Length": {strconv.Itoa(len(body))}}
	}
	return nil
}

func (g *Gateway) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	kind := "unreachable"
	if errors.Is(err, context.DeadlineExceeded) {
		kind = "timeout"
	}
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return // client went away
	}
	metrics.UpstreamErrors.WithLabelValues(kind).Inc()
	g.log.Error("upstream error", "path", r.URL.Path, "err", err, "request_id", httpx.RequestIDOf(r.Context()))
	code := http.StatusBadGateway
	if kind == "timeout" {
		code = http.StatusGatewayTimeout
	}
	httpx.WriteError(w, r, code, "indexer API unavailable")
}

func (g *Gateway) blocked(path string) bool {
	for _, p := range g.cfg.Block {
		if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

func exempt(path string) bool {
	return path == "/health" || path == "/ready" || path == "/ui" || strings.HasPrefix(path, "/ui/")
}

// Handler returns the public HTTP handler.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", g.ready)
	if g.cfg.UI != nil {
		mux.HandleFunc("GET /ui/config.js", g.uiConfig)
		mux.Handle("GET /ui/", httpx.UIHandler(g.cfg.UI))
		mux.Handle("GET /ui", http.RedirectHandler("/ui/", http.StatusMovedPermanently))
	}
	var api http.Handler = g.proxy
	if g.cache != nil {
		cached := g.cache.Wrap(g.proxy)
		api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path != "/status" {
				cached.ServeHTTP(w, r)
				return
			}
			g.proxy.ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case g.blocked(r.URL.Path):
			httpx.WriteError(w, r, http.StatusNotFound, "not found")
		case r.URL.Path == "/" && g.cfg.UI != nil && strings.Contains(r.Header.Get("Accept"), "text/html"):
			http.Redirect(w, r, "/ui/", http.StatusFound)
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			api.ServeHTTP(w, r)
		case (r.Method == http.MethodPost || r.Method == http.MethodDelete) && strings.HasPrefix(r.URL.Path, "/labels"):
			r.Body = http.MaxBytesReader(w, r.Body, g.cfg.MaxBody)
			g.proxy.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			httpx.WriteError(w, r, http.StatusMethodNotAllowed, "method not allowed")
		}
	})
	var h http.Handler = mux
	h = httpx.RequireKey(httpx.NewKeys(g.cfg.APIKeys), exempt)(h)
	h = httpx.RateLimit(g.limiter, func(r *http.Request) string { return httpx.ClientIP(r, g.cfg.TrustProxy) }, exempt)(h)
	h = httpx.CORS(g.cfg.CORSOrigins)(h)
	h = httpx.SecurityHeaders(h)
	h = g.observe(h)
	return httpx.RequestID(h)
}

// uiConfig replaces the static web/static/config.js: same-origin API, and the
// paths this edge blocks, so the UI never requests them.
func (g *Gateway) uiConfig(w http.ResponseWriter, _ *http.Request) {
	blocked, _ := json.Marshal(g.cfg.Block)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = fmt.Fprintf(w, "window.STELLAR_INDEXER = { apiBase: '', blocked: %s };\n", blocked)
}

// ready reports whether the indexer behind the tunnel is reachable and fresh.
func (g *Gateway) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	u := *g.cfg.Upstream
	u.Path = strings.TrimSuffix(u.Path, "/") + "/ready"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if g.cfg.UpstreamKey != "" {
		req.Header.Set("X-API-Key", g.cfg.UpstreamKey)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		metrics.UpstreamErrors.WithLabelValues("unreachable").Inc()
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "reason": "indexer API unreachable"})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "reason": "indexer not ready (upstream " + strconv.Itoa(resp.StatusCode) + ")"})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// observe records metrics labelled by the first path segment (bounded cardinality).
func (g *Gateway) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &httpx.StatusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.Status == 0 {
			sw.Status = http.StatusOK
		}
		seg := "/"
		if p := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2); p[0] != "" {
			seg = "/" + p[0]
		}
		route := r.Method + " " + seg
		metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(sw.Status)).Inc()
		metrics.HTTPDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
		g.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", sw.Status, "bytes", sw.Bytes,
			"dur", time.Since(start), "client", httpx.ClientIP(r, g.cfg.TrustProxy), "request_id", httpx.RequestIDOf(r.Context()))
	})
}

// UpstreamTLS builds the client TLS config: a CA bundle to verify the
// internal API, and an optional client certificate for mTLS.
func UpstreamTLS(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	if caFile == "" && certFile == "" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: serverName}
	if caFile != "" {
		pem, err := os.ReadFile(caFile) //nolint:gosec // operator-supplied path
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s: no certificates found", caFile)
		}
		cfg.RootCAs = pool
	}
	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}
