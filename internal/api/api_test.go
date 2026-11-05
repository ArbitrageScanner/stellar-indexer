package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"gopkg.in/yaml.v3"
)

// These tests need no database: they exercise routes that fail validation,
// policy or middleware before any query runs.

func newTestServer(cfg Config) *Server {
	return New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
}

func do(h http.Handler, method, path string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "203.0.113.7:1234"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOpenAPICoversRoutes(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(openapiSpec, &spec); err != nil {
		t.Fatalf("openapi.yaml does not parse: %v", err)
	}
	documented := map[string]bool{}
	for p, ops := range spec.Paths {
		for m := range ops {
			documented[strings.ToUpper(m)+" "+p] = true
		}
	}
	registered := map[string]bool{}
	for _, rt := range newTestServer(Config{}).routes() {
		p := rt.pattern
		if p == "/{$}" {
			p = "/"
		}
		registered[rt.method+" "+p] = true
		if !documented[rt.method+" "+p] {
			t.Errorf("route %s %s is not in openapi.yaml", rt.method, p)
		}
	}
	for k := range documented {
		if !registered[k] {
			t.Errorf("openapi.yaml documents %s, which is not registered", k)
		}
	}
}

func TestRequestIDAndSecurityHeaders(t *testing.T) {
	h := newTestServer(Config{UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}}).Handler()
	rec := do(h, "GET", "/openapi.yaml", map[string]string{"X-Request-ID": "abc-123"}, "")
	if rec.Code != 200 || rec.Header().Get("X-Request-ID") != "abc-123" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	rec = do(h, "GET", "/openapi.yaml", map[string]string{"X-Request-ID": "bad id with spaces"}, "")
	if id := rec.Header().Get("X-Request-ID"); id == "" || strings.Contains(id, " ") {
		t.Fatalf("request id not regenerated: %q", id)
	}
	rec = do(h, "GET", "/ui/", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("ui: %d csp=%q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
}

func TestPanicRecovery(t *testing.T) {
	// /health with no pool panics inside the repository; the middleware must answer 500 JSON.
	rec := do(newTestServer(Config{}).Handler(), "GET", "/health", nil, "")
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"request_id"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestCORS(t *testing.T) {
	h := newTestServer(Config{}).Handler()
	rec := do(h, "OPTIONS", "/labels", map[string]string{"Origin": "https://x.example", "Access-Control-Request-Method": "POST"}, "")
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "X-API-Key") {
		t.Fatalf("preflight %d %v", rec.Code, rec.Header())
	}
	h = newTestServer(Config{CORSOrigins: []string{"https://ok.example"}}).Handler()
	if got := do(h, "GET", "/openapi.yaml", map[string]string{"Origin": "https://ok.example"}, "").Header().Get("Access-Control-Allow-Origin"); got != "https://ok.example" {
		t.Fatalf("allowed origin not echoed: %q", got)
	}
	if got := do(h, "GET", "/openapi.yaml", map[string]string{"Origin": "https://evil.example"}, "").Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin allowed: %q", got)
	}
}

func TestRateLimit(t *testing.T) {
	h := newTestServer(Config{RateRPS: 1, RateBurst: 2}).Handler()
	for i := 0; i < 2; i++ {
		if c := do(h, "GET", "/openapi.yaml", nil, "").Code; c != 200 {
			t.Fatalf("request %d: %d", i, c)
		}
	}
	rec := do(h, "GET", "/openapi.yaml", nil, "")
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d %v", rec.Code, rec.Header())
	}
	for i := 0; i < 5; i++ { // probes and scrapes are exempt
		if c := do(h, "GET", "/metrics", nil, "").Code; c != 200 {
			t.Fatalf("/metrics limited: %d", c)
		}
	}
	l := newRateLimiter(10, 1)
	now := time.Now()
	if ok, _ := l.allow("a", now); !ok {
		t.Fatal("first token")
	}
	if ok, wait := l.allow("a", now); ok || wait <= 0 {
		t.Fatal("bucket should be empty")
	}
	if ok, _ := l.allow("a", now.Add(150*time.Millisecond)); !ok {
		t.Fatal("token should refill at 10/s")
	}
	if ok, _ := l.allow("b", now); !ok {
		t.Fatal("clients must have separate buckets")
	}
}

func TestAPIKeys(t *testing.T) {
	h := newTestServer(Config{APIKeys: []string{"k1", "k2"}}).Handler()
	if c := do(h, "GET", "/openapi.yaml", nil, "").Code; c != 401 {
		t.Fatalf("no key: %d", c)
	}
	if c := do(h, "GET", "/openapi.yaml", map[string]string{"X-API-Key": "nope"}, "").Code; c != 401 {
		t.Fatalf("bad key: %d", c)
	}
	if c := do(h, "GET", "/openapi.yaml", map[string]string{"X-API-Key": "k2"}, "").Code; c != 200 {
		t.Fatalf("good key: %d", c)
	}
	if c := do(h, "GET", "/openapi.yaml", map[string]string{"Authorization": "Bearer k1"}, "").Code; c != 200 {
		t.Fatalf("bearer key: %d", c)
	}
	if c := do(h, "GET", "/metrics", nil, "").Code; c != 200 {
		t.Fatalf("/metrics must be exempt: %d", c)
	}
}

func TestLabelWritePolicy(t *testing.T) {
	body := `{"address":"GBTB3MJPQKPIWLYX34IKBE7ENT46ZDVQGPAVHGBDOMNA4HP6Q2KKJFLH","tag":"has space"}`
	// Closed by default.
	rec := do(newTestServer(Config{}).Handler(), "POST", "/labels", nil, body)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "LABELS_TOKEN") {
		t.Fatalf("closed: %d %s", rec.Code, rec.Body)
	}
	if c := do(newTestServer(Config{}).Handler(), "DELETE", "/labels/GX/x", nil, "").Code; c != 403 {
		t.Fatalf("delete closed: %d", c)
	}
	// Token configured: missing or wrong token → 401; right token reaches validation (400 for the bad tag).
	tok := newTestServer(Config{LabelsToken: "s3cret"}).Handler()
	if c := do(tok, "POST", "/labels", nil, body).Code; c != 401 {
		t.Fatalf("no token: %d", c)
	}
	if c := do(tok, "POST", "/labels", map[string]string{"Authorization": "Bearer wrong"}, body).Code; c != 401 {
		t.Fatalf("wrong token: %d", c)
	}
	if c := do(tok, "POST", "/labels", map[string]string{"Authorization": "Bearer s3cret"}, body).Code; c != 400 {
		t.Fatalf("token + invalid tag: %d", c)
	}
	// Open: validation applies.
	open := newTestServer(Config{LabelsOpen: true}).Handler()
	if c := do(open, "POST", "/labels", nil, "not json").Code; c != 400 {
		t.Fatalf("bad json: %d", c)
	}
	if c := do(open, "POST", "/labels", nil, body).Code; c != 400 {
		t.Fatalf("bad tag: %d", c)
	}
}

func TestValidationErrors(t *testing.T) {
	h := newTestServer(Config{}).Handler()
	for _, path := range []string{
		"/price?base=XLM", "/price?base=native&counter=xlm", "/candles?base=a&counter=b&interval=7m",
		"/transactions?successful=maybe", "/transactions?before=abc", "/search", "/ledgers/abc", "/trades?base=only",
		"/operations?successful=x", "/ledgers?before=x",
	} {
		rec := do(h, "GET", path, nil, "")
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if c := do(h, "GET", "/nope", nil, "").Code; c != 404 {
		t.Errorf("unknown route: %d", c)
	}
}

func TestMetricsUseRoutePatterns(t *testing.T) {
	h := newTestServer(Config{}).Handler()
	do(h, "GET", "/ledgers/abc", nil, "")
	body := do(h, "GET", "/metrics", nil, "").Body.String()
	if !strings.Contains(body, `route="GET /ledgers/{seq}"`) || strings.Contains(body, `route="/ledgers/abc"`) {
		t.Fatal("http metrics must be labelled by route pattern")
	}
}

func TestResponseCache(t *testing.T) {
	var calls atomic.Int32
	status := 200
	c := newResponseCache(time.Minute, 2)
	h := c.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"n":1}`))
	}))
	if rec := do(h, "GET", "/x?a=1", nil, ""); rec.Header().Get("X-Cache") != "MISS" || rec.Body.String() != `{"n":1}` {
		t.Fatalf("first: %v %s", rec.Header(), rec.Body)
	}
	if rec := do(h, "GET", "/x?a=1", nil, ""); rec.Header().Get("X-Cache") != "HIT" || rec.Header().Get("Content-Type") != "application/json" || calls.Load() != 1 {
		t.Fatalf("second: %v calls=%d", rec.Header(), calls.Load())
	}
	do(h, "GET", "/x?a=2", nil, "") // other query string = other entry
	if calls.Load() != 2 {
		t.Fatalf("query string must be part of the key: %d", calls.Load())
	}
	status = 500
	do(h, "GET", "/err", nil, "")
	do(h, "GET", "/err", nil, "")
	if calls.Load() != 4 {
		t.Fatalf("errors must not be cached: %d", calls.Load())
	}
	// LRU bound: max 2 entries.
	do(h, "GET", "/x?a=3", nil, "")
	if len(c.entries) > 2 {
		t.Fatalf("cache exceeds bound: %d", len(c.entries))
	}

	// Concurrent misses for one key share a single upstream call.
	var upstream atomic.Int32
	release := make(chan struct{})
	slow := newResponseCache(time.Minute, 10).wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		<-release
		_, _ = w.Write([]byte("ok"))
	}))
	var wg sync.WaitGroup
	codes := make([]string, 5)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = do(slow, "GET", "/slow", nil, "").Body.String()
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if upstream.Load() != 1 {
		t.Fatalf("dedupe: %d upstream calls", upstream.Load())
	}
	for _, b := range codes {
		if b != "ok" {
			t.Fatalf("waiter got %q", b)
		}
	}
}
