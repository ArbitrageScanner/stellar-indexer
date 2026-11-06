// Package httpx holds HTTP building blocks shared by the internal API and the
// public gateway: request IDs, JSON errors, security headers, CORS, client
// IPs, API keys, rate limiting, a response cache and the static UI handler.
// It has no database or Stellar dependencies.
package httpx

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

type ctxKey int

const ctxRequestID ctxKey = 0

var reRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestID echoes a well-formed X-Request-ID or generates one, and puts it
// on the context and the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !reRequestID.MatchString(id) {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
			r.Header.Set("X-Request-ID", id) // propagate upstream
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// RequestIDOf returns the request ID set by RequestID.
func RequestIDOf(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// WriteJSON encodes v with status code.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// WriteError writes {"error", "request_id"}.
func WriteError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	WriteJSON(w, code, map[string]string{"error": msg, "request_id": RequestIDOf(r.Context())})
}

// StatusWriter captures the status code and size of a response.
type StatusWriter struct {
	http.ResponseWriter
	Status int
	Bytes  int
}

func (w *StatusWriter) WriteHeader(code int) {
	if w.Status == 0 {
		w.Status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *StatusWriter) Write(b []byte) (int, error) {
	if w.Status == 0 {
		w.Status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.Bytes += n
	return n, err
}

func (w *StatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// UICSP allows the explorer to load only its own code; connect-src stays
// open because the UI can be pointed at another indexer's API.
const UICSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"connect-src 'self' http: https:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// SecurityHeaders sets nosniff, no-referrer, DENY framing, and the CSP on /ui.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/ui") {
			h.Set("Content-Security-Policy", UICSP)
		}
		next.ServeHTTP(w, r)
	})
}

// CORS applies the allowed origins ("*" = any) and answers preflights.
func CORS(origins []string) func(http.Handler) http.Handler {
	anyOrigin := len(origins) == 0 || slices.Contains(origins, "*")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			origin := r.Header.Get("Origin")
			switch {
			case anyOrigin:
				h.Set("Access-Control-Allow-Origin", "*")
			case origin != "" && slices.Contains(origins, origin):
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
}

// ClientIP is the remote address, or the first X-Forwarded-For hop when the
// server sits behind a trusted proxy.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
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

// Bearer returns the token of an "Authorization: Bearer" header.
func Bearer(r *http.Request) string {
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// Keys is a set of secrets compared in constant time.
type Keys [][]byte

func NewKeys(keys []string) Keys {
	out := make(Keys, 0, len(keys))
	for _, k := range keys {
		if k != "" {
			out = append(out, []byte(k))
		}
	}
	return out
}

// Match reports whether got equals one of the keys (constant time per key).
func (k Keys) Match(got string) bool {
	ok := 0
	for _, key := range k {
		ok |= subtle.ConstantTimeCompare([]byte(got), key)
	}
	return ok == 1 && got != ""
}

// RequireKey enforces k (X-API-Key or Bearer) on every path not skipped.
func RequireKey(k Keys, skip func(string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if len(k) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip != nil && skip(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get("X-API-Key")
			if key == "" {
				key = Bearer(r)
			}
			if !k.Match(key) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="stellar-indexer"`)
				WriteError(w, r, http.StatusUnauthorized, "missing or invalid API key (send X-API-Key)")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// UIHandler serves the static explorer under /ui/. index.html is never
// cached so a new release is picked up on reload; other files briefly.
func UIHandler(ui fs.FS) http.Handler {
	files := http.StripPrefix("/ui/", http.FileServerFS(ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/ui/")
		if p == "" || p == "index.html" || p == "config.js" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		files.ServeHTTP(w, r)
	})
}
