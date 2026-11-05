package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxRoute
)

var reRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// requestID echoes a well-formed X-Request-ID or generates one, and puts it
// on the context and the response.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !reRequestID.MatchString(id) {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

func requestIDOf(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

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

// statusWriter captures the status code and size of a response.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// observe records Prometheus metrics by route pattern and writes a debug
// access log line with the request id.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		route := "unmatched"
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), ctxRoute, &route)))
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		dur := time.Since(start)
		metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(sw.status)).Inc()
		metrics.HTTPDuration.WithLabelValues(route).Observe(dur.Seconds())
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "route", route, "status", sw.status,
			"bytes", sw.bytes, "dur", dur, "client", s.clientIP(r), "request_id", requestIDOf(r.Context()))
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

// uiCSP allows the explorer to load only its own code; connect-src stays
// open because the UI can be pointed at another indexer's API.
const uiCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"connect-src 'self' http: https:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/ui") {
			h.Set("Content-Security-Policy", uiCSP)
		}
		next.ServeHTTP(w, r)
	})
}

// cors applies the configured origins and answers preflights.
func (s *Server) cors(next http.Handler) http.Handler {
	any := slices.Contains(s.cfg.CORSOrigins, "*")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		origin := r.Header.Get("Origin")
		switch {
		case any:
			h.Set("Access-Control-Allow-Origin", "*")
		case origin != "" && slices.Contains(s.cfg.CORSOrigins, origin):
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
		}
		h.Set("Access-Control-Expose-Headers", "X-Request-ID, X-Cache, Retry-After")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Request-ID")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// exempt paths skip rate limiting and API keys (probes, scrapes, static UI).
func exempt(path string) bool {
	return path == "/health" || path == "/ready" || path == "/metrics" || path == "/ui" || strings.HasPrefix(path, "/ui/")
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	if s.limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if ok, wait := s.limiter.allow(s.clientIP(r), time.Now()); !ok {
			metrics.RateLimited.Inc()
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			writeError(w, r, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP is the remote address, or the first X-Forwarded-For hop when the
// server sits behind a trusted proxy.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// authenticate enforces API keys when configured.
func (s *Server) authenticate(next http.Handler) http.Handler {
	if len(s.cfg.APIKeys) == 0 {
		return next
	}
	keys := make([][]byte, len(s.cfg.APIKeys))
	for i, k := range s.cfg.APIKeys {
		keys[i] = []byte(k)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get("X-API-Key")
		if key == "" {
			key = bearer(r)
		}
		if !matchesAny([]byte(key), keys) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="stellar-indexer"`)
			writeError(w, r, http.StatusUnauthorized, "missing or invalid API key (send X-API-Key)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// matchesAny compares in constant time per candidate.
func matchesAny(got []byte, keys [][]byte) bool {
	ok := 0
	for _, k := range keys {
		ok |= subtle.ConstantTimeCompare(got, k)
	}
	return ok == 1 && len(got) > 0
}

// errLabelsClosed etc. explain why a label write was refused.
var (
	errLabelsClosed = errors.New("label writes are disabled on this server: set LABELS_TOKEN (and send it as a Bearer token) or LABELS_OPEN=true")
	errLabelsToken  = errors.New("label writes require Authorization: Bearer <LABELS_TOKEN>")
)

// labelWriteAllowed implements the label write policy: closed by default,
// a Bearer token when LABELS_TOKEN is set, or open with LABELS_OPEN.
func (s *Server) labelWriteAllowed(r *http.Request) (int, error) {
	if s.cfg.LabelsToken != "" {
		if matchesAny([]byte(bearer(r)), [][]byte{[]byte(s.cfg.LabelsToken)}) {
			return 0, nil
		}
		return http.StatusUnauthorized, errLabelsToken
	}
	if s.cfg.LabelsOpen {
		return 0, nil
	}
	return http.StatusForbidden, errLabelsClosed
}
