package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func do(h http.Handler, method, target string, body io.Reader, _ string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, body))
	return rec
}

func TestRateLimiter(t *testing.T) {
	l := NewRateLimiter(10, 1)
	now := time.Now()
	if ok, _ := l.Allow("a", now); !ok {
		t.Fatal("first token")
	}
	if ok, wait := l.Allow("a", now); ok || wait <= 0 {
		t.Fatal("bucket should be empty")
	}
	if ok, _ := l.Allow("a", now.Add(150*time.Millisecond)); !ok {
		t.Fatal("token should refill at 10/s")
	}
	if ok, _ := l.Allow("b", now); !ok {
		t.Fatal("clients must have separate buckets")
	}
}

func TestResponseCache(t *testing.T) {
	var calls atomic.Int32
	status := 200
	c := NewCache(time.Minute, 2)
	h := c.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	slow := NewCache(time.Minute, 10).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestKeysAndClientIP(t *testing.T) {
	k := NewKeys([]string{"a1", "", "b2"})
	if !k.Match("b2") || k.Match("") || k.Match("a") || len(k) != 2 {
		t.Fatal("key matching")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.9:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	if ClientIP(r, false) != "10.0.0.9" || ClientIP(r, true) != "203.0.113.7" {
		t.Fatal("client ip")
	}
	h := RequireKey(k, func(p string) bool { return strings.HasPrefix(p, "/ui") })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if do(h, "GET", "/x", nil, "").Code != 401 || do(h, "GET", "/ui/", nil, "").Code != 200 {
		t.Fatal("RequireKey")
	}
}
