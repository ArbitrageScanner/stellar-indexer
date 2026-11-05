package api

import (
	"bytes"
	"container/list"
	"net/http"
	"sync"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
)

// responseCache keeps successful GET responses of heavy analytics endpoints
// for a short TTL. Concurrent misses for the same URL share one upstream
// computation; the size is bounded with LRU eviction.
type responseCache struct {
	ttl time.Duration
	max int

	mu       sync.Mutex
	entries  map[string]*list.Element
	lru      *list.List // front = most recent; values are *cached
	inflight map[string]*flight
}

type cached struct {
	key         string
	contentType string
	body        []byte
	expires     time.Time
}

type flight struct {
	done chan struct{}
	resp *cached // nil when the leader's response was not cacheable
}

func newResponseCache(ttl time.Duration, max int) *responseCache {
	return &responseCache{ttl: ttl, max: max, entries: map[string]*list.Element{}, lru: list.New(), inflight: map[string]*flight{}}
}

func (c *responseCache) get(key string, now time.Time) *cached {
	if e, ok := c.entries[key]; ok {
		v := e.Value.(*cached)
		if now.Before(v.expires) {
			c.lru.MoveToFront(e)
			return v
		}
		c.lru.Remove(e)
		delete(c.entries, key)
	}
	return nil
}

func (c *responseCache) put(v *cached) {
	if e, ok := c.entries[v.key]; ok {
		c.lru.Remove(e)
	}
	c.entries[v.key] = c.lru.PushFront(v)
	for c.lru.Len() > c.max {
		old := c.lru.Back()
		c.lru.Remove(old)
		delete(c.entries, old.Value.(*cached).key)
	}
}

func serveCached(w http.ResponseWriter, v *cached) {
	w.Header().Set("Content-Type", v.contentType)
	w.Header().Set("X-Cache", "HIT")
	_, _ = w.Write(v.body)
}

// bufferWriter buffers a response so it can be both sent and cached.
type bufferWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufferWriter) Header() http.Header         { return b.header }
func (b *bufferWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufferWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (c *responseCache) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.RequestURI()
		c.mu.Lock()
		if v := c.get(key, time.Now()); v != nil {
			c.mu.Unlock()
			metrics.CacheHits.WithLabelValues("hit").Inc()
			serveCached(w, v)
			return
		}
		if f, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			select {
			case <-f.done:
			case <-r.Context().Done():
				return
			}
			if f.resp != nil {
				metrics.CacheHits.WithLabelValues("shared").Inc()
				serveCached(w, f.resp)
				return
			}
			next.ServeHTTP(w, r) // leader's response was an error: compute our own
			return
		}
		f := &flight{done: make(chan struct{})}
		c.inflight[key] = f
		c.mu.Unlock()
		metrics.CacheHits.WithLabelValues("miss").Inc()

		bw := &bufferWriter{header: http.Header{}}
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			if f.resp != nil {
				c.put(f.resp)
			}
			c.mu.Unlock()
			close(f.done)
		}()
		next.ServeHTTP(bw, r)
		if bw.status == 0 {
			bw.status = http.StatusOK
		}
		if bw.status == http.StatusOK {
			f.resp = &cached{key: key, contentType: bw.header.Get("Content-Type"), body: bytes.Clone(bw.buf.Bytes()), expires: time.Now().Add(c.ttl)}
		}
		for k, v := range bw.header {
			w.Header()[k] = v
		}
		w.Header().Set("X-Cache", "MISS")
		w.WriteHeader(bw.status)
		_, _ = w.Write(bw.buf.Bytes())
	})
}
