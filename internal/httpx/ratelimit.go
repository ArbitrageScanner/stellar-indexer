package httpx

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
)

// RateLimiter is a per-client token bucket. Idle buckets are swept so the
// map stays bounded by the set of recently active clients.
type RateLimiter struct {
	mu        sync.Mutex
	rate      float64 // tokens per second
	burst     float64
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter allows rps requests per second per client with the given burst.
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	return &RateLimiter{rate: rps, burst: float64(burst), buckets: map[string]*bucket{}}
}

// Allow takes a token for client at now; when empty it reports how long
// until the next token.
func (l *RateLimiter) Allow(client string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b := l.buckets[client]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[client] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// RateLimit rejects requests over the limit with 429 and Retry-After.
// client extracts the client key; skip exempts paths (probes, static files).
func RateLimit(l *RateLimiter, client func(*http.Request) string, skip func(string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip != nil && skip(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if ok, wait := l.Allow(client(r), time.Now()); !ok {
				metrics.RateLimited.Inc()
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
				WriteError(w, r, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
