package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/httpx"
	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
)

type ctxKey int

const ctxRoute ctxKey = 0

func requestIDOf(ctx context.Context) string { return httpx.RequestIDOf(ctx) }

// withRoute records the matched route pattern for metrics and logs (the raw
// path would explode label cardinality).
func withRoute(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := r.Context().Value(ctxRoute).(*string); ok {
			*p = pattern
		}
		next.ServeHTTP(w, r)
	})
}

// observe records Prometheus metrics by route pattern and writes a debug
// access log line with the request id.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		route := "unmatched"
		sw := &httpx.StatusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), ctxRoute, &route)))
		if sw.Status == 0 {
			sw.Status = http.StatusOK
		}
		dur := time.Since(start)
		metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(sw.Status)).Inc()
		metrics.HTTPDuration.WithLabelValues(route).Observe(dur.Seconds())
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "route", route, "status", sw.Status,
			"bytes", sw.Bytes, "dur", dur, "client", s.clientIP(r), "request_id", requestIDOf(r.Context()))
	})
}

// recoverPanics turns a handler panic into a logged 500.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v) // net/http's sentinel for aborting a response
				}
				s.log.Error("panic", "path", r.URL.Path, "err", v, "request_id", requestIDOf(r.Context()))
				writeError(w, r, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// exempt paths skip rate limiting and API keys (probes, scrapes, static UI).
func exempt(path string) bool {
	return path == "/health" || path == "/ready" || path == "/metrics" || path == "/ui" || strings.HasPrefix(path, "/ui/")
}

func (s *Server) clientIP(r *http.Request) string { return httpx.ClientIP(r, s.cfg.TrustProxy) }

// errLabelsClosed etc. explain why a label write was refused.
var (
	errLabelsClosed = errors.New("label writes are disabled on this server: set LABELS_TOKEN (and send it as a Bearer token) or LABELS_OPEN=true")
	errLabelsToken  = errors.New("label writes require Authorization: Bearer <LABELS_TOKEN>")
)

// labelWriteAllowed implements the label write policy: closed by default,
// a Bearer token when LABELS_TOKEN is set, or open with LABELS_OPEN.
func (s *Server) labelWriteAllowed(r *http.Request) (int, error) {
	if s.cfg.LabelsToken != "" {
		if httpx.NewKeys([]string{s.cfg.LabelsToken}).Match(httpx.Bearer(r)) {
			return 0, nil
		}
		return http.StatusUnauthorized, errLabelsToken
	}
	if s.cfg.LabelsOpen {
		return 0, nil
	}
	return http.StatusForbidden, errLabelsClosed
}
