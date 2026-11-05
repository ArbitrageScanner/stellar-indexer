// Package api is the HTTP transport of the indexer: routing, middleware and
// thin handlers that parse and validate parameters, call the repository
// (internal/query) and domain logic (internal/analytics), and encode JSON.
// It holds no SQL and no business rules. See docs/API.md.
package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/query"
)

// Config configures the HTTP API. Zero values disable optional features.
type Config struct {
	Network    string // shown in /status and the index
	CursorName string // ingest cursor reported by /status
	USDAsset   string // asset whose XLM market prices XLM in USD; empty disables USD values

	UI fs.FS // explorer served at /ui/; nil disables it

	LabelsToken string // Bearer token required for label writes
	LabelsOpen  bool   // allow label writes without a token (local use only)

	APIKeys     []string // when set, required (X-API-Key or Bearer) except on /health, /ready, /metrics, /ui/
	CORSOrigins []string // allowed origins; default ["*"]
	RateRPS     float64  // per-client requests per second; 0 = no limit
	RateBurst   int      // bucket size; default 2×RateRPS
	TrustProxy  bool     // take the client IP from X-Forwarded-For

	CacheTTL    time.Duration // response cache for heavy endpoints; 0 = off
	ReadyMaxLag time.Duration // /ready fails when the newest ledger is older; 0 = no lag check
}

// Server serves the API.
type Server struct {
	repo    *query.Repo
	log     *slog.Logger
	cfg     Config
	cache   *responseCache
	limiter *rateLimiter
}

// New builds a server over a Postgres pool.
func New(pool *pgxpool.Pool, log *slog.Logger, cfg Config) *Server {
	if len(cfg.CORSOrigins) == 0 {
		cfg.CORSOrigins = []string{"*"}
	}
	s := &Server{repo: query.New(pool), log: log, cfg: cfg}
	if cfg.CacheTTL > 0 {
		s.cache = newResponseCache(cfg.CacheTTL, 512)
	}
	if cfg.RateRPS > 0 {
		burst := cfg.RateBurst
		if burst <= 0 {
			burst = int(2*cfg.RateRPS) + 1
		}
		s.limiter = newRateLimiter(cfg.RateRPS, burst)
	}
	return s
}

// route is one registered endpoint. cached routes go through the response
// cache; the table is also the source for the index and the OpenAPI test.
type route struct {
	method, pattern string
	handler         http.HandlerFunc
	cached          bool
}

func (s *Server) routes() []route {
	return []route{
		{"GET", "/{$}", s.index, false},
		{"GET", "/health", s.health, false},
		{"GET", "/ready", s.ready, false},
		{"GET", "/metrics", metrics.Handler().ServeHTTP, false},
		{"GET", "/openapi.yaml", s.openapi, false},
		{"GET", "/status", s.status, false},
		{"GET", "/search", s.search, false},

		{"GET", "/ledgers", s.ledgers, false},
		{"GET", "/ledgers/{seq}", s.ledger, false},
		{"GET", "/transactions", s.transactions, false},
		{"GET", "/transactions/{hash}", s.transaction, false},
		{"GET", "/operations", s.operations, false},
		{"GET", "/transfers", s.transfers, false},
		{"GET", "/events", s.events, false},
		{"GET", "/contracts", s.contracts, true},
		{"GET", "/contracts/{id}", s.contract, false},
		{"GET", "/contracts/{id}/events", s.contractEvents, false},

		{"GET", "/accounts/top", s.topAccounts, false},
		{"GET", "/accounts/{id}", s.account, false},
		{"GET", "/accounts/{id}/operations", s.accountOperations, false},
		{"GET", "/accounts/{id}/transfers", s.accountTransfers, false},
		{"GET", "/accounts/{id}/trades", s.accountTrades, false},
		{"GET", "/accounts/{id}/events", s.accountEvents, false},
		{"GET", "/accounts/{id}/portfolio", s.portfolio, false},
		{"GET", "/accounts/{id}/stats", s.accountStats, false},
		{"GET", "/accounts/{id}/tags", s.accountTags, false},

		{"GET", "/assets", s.assets, true},
		{"GET", "/assets/{key}", s.asset, false},
		{"GET", "/assets/{key}/holders", s.assetHolders, false},
		{"GET", "/pairs", s.pairs, true},
		{"GET", "/trades", s.trades, false},
		{"GET", "/price", s.price, false},
		{"GET", "/candles", s.candles, false},
		{"GET", "/pools", s.pools, false},

		{"GET", "/protocols", s.protocols, true},
		{"GET", "/protocols/{name}/summary", s.protocolSummary, true},
		{"GET", "/protocols/{name}/events", s.protocolEvents, false},

		{"GET", "/stats/network", s.networkStats, true},
		{"GET", "/stats/operations", s.operationStats, true},
		{"GET", "/stats/tables", s.tableStats, true},

		{"GET", "/labels", s.labels, false},
		{"GET", "/labels/tags", s.labelTags, false},
		{"POST", "/labels", s.putLabel, false},
		{"DELETE", "/labels/{address}/{tag}", s.deleteLabel, false},

		{"GET", "/leaderboards", s.leaderboards, true},
		{"GET", "/alerts", s.alerts, true},
	}
}

// Handler returns the full middleware chain around the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		var h http.Handler = rt.handler
		if rt.cached && s.cache != nil {
			h = s.cache.wrap(h)
		}
		mux.Handle(rt.method+" "+rt.pattern, withRoute(rt.method+" "+rt.pattern, h))
	}
	if s.cfg.UI != nil {
		mux.Handle("GET /ui/", withRoute("GET /ui/", uiHandler(s.cfg.UI)))
		mux.Handle("GET /ui", withRoute("GET /ui", http.RedirectHandler("/ui/", http.StatusMovedPermanently)))
	}
	var h http.Handler = mux
	h = s.authenticate(h)
	h = s.rateLimit(h)
	h = s.cors(h)
	h = securityHeaders(h)
	h = s.recoverPanics(h)
	h = s.observe(h)
	return requestID(h)
}
