package gateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// upstream is a stand-in for the internal API that records what it saw.
type upstream struct {
	calls   atomic.Int32
	lastKey atomic.Value
	lastXFF atomic.Value
	lastAut atomic.Value
	ready   atomic.Bool
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.calls.Add(1)
	u.lastKey.Store(r.Header.Get("X-API-Key"))
	u.lastXFF.Store(r.Header.Get("X-Forwarded-For"))
	u.lastAut.Store(r.Header.Get("Authorization"))
	if r.Header.Get("X-API-Key") != "internal-secret" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"internal detail"}`))
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "https://evil.example") // must be replaced by the gateway's CORS
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/ready":
		if !u.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = w.Write([]byte(`{}`))
	case "/labels":
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	default:
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path, "query": r.URL.RawQuery})
	}
}

func newGateway(t *testing.T, up string, mod func(*Config)) http.Handler {
	t.Helper()
	u, _ := url.Parse(up)
	cfg := Config{Upstream: u, UpstreamKey: "internal-secret", UI: fstest.MapFS{"index.html": {Data: []byte("<html>ui</html>")}},
		CacheTTL: time.Minute, Block: DefaultBlock, MaxBody: 64}
	if mod != nil {
		mod(&cfg)
	}
	g, err := New(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return g.Handler()
}

func send(h http.Handler, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	req.RemoteAddr = "198.51.100.7:5555"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestProxyInjectsKeyAndScrubsClientHeaders(t *testing.T) {
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	h := newGateway(t, srv.URL, nil)

	rec := send(h, "GET", "/ledgers?limit=5", "", map[string]string{
		"X-API-Key": "client-guess", "Authorization": "Bearer x", "X-Forwarded-For": "1.2.3.4", "Origin": "https://site.example"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"/ledgers"`) {
		t.Fatalf("proxy: %d %s", rec.Code, rec.Body)
	}
	if up.lastKey.Load() != "internal-secret" {
		t.Fatalf("upstream key %q", up.lastKey.Load())
	}
	if up.lastAut.Load() != "" {
		t.Fatal("Authorization forwarded on a read")
	}
	if up.lastXFF.Load() != "198.51.100.7" {
		t.Fatalf("spoofable X-Forwarded-For forwarded: %q", up.lastXFF.Load())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("X-Request-ID") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("edge headers: %v", rec.Header())
	}
}

func TestTrustedProxyClientIP(t *testing.T) {
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	h := newGateway(t, srv.URL, func(c *Config) { c.TrustProxy = true })
	send(h, "GET", "/x", "", map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.2"})
	if up.lastXFF.Load() != "203.0.113.9" {
		t.Fatalf("xff %q", up.lastXFF.Load())
	}
}

func TestBlockedAndMethods(t *testing.T) {
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	h := newGateway(t, srv.URL, nil)
	for _, p := range []string{"/metrics", "/stats/tables", "/metrics/x"} {
		if rec := send(h, "GET", p, "", nil); rec.Code != 404 {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	if up.calls.Load() != 0 {
		t.Fatal("blocked path reached upstream")
	}
	if rec := send(h, "PUT", "/ledgers", "x", nil); rec.Code != 405 {
		t.Fatalf("PUT: %d", rec.Code)
	}
	if rec := send(h, "POST", "/search", "x", nil); rec.Code != 405 {
		t.Fatalf("POST outside labels: %d", rec.Code)
	}
}

func TestLabelWritesForwardTokenAndLimitBody(t *testing.T) {
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	h := newGateway(t, srv.URL, nil)
	rec := send(h, "POST", "/labels", `{"tag":"x"}`, map[string]string{"Authorization": "Bearer labels-token"})
	if rec.Code != 200 || up.lastAut.Load() != "Bearer labels-token" {
		t.Fatalf("label write: %d auth=%q", rec.Code, up.lastAut.Load())
	}
	if rec := send(h, "POST", "/labels", strings.Repeat("x", 200), nil); rec.Code != http.StatusBadGateway && rec.Code != http.StatusRequestEntityTooLarge {
		// The body is cut at MaxBody; the upstream read fails, so the request never succeeds.
		t.Fatalf("oversized body accepted: %d", rec.Code)
	}
}

func TestEdgeCacheAndUI(t *testing.T) {
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	h := newGateway(t, srv.URL, nil)
	first := send(h, "GET", "/pairs?hours=24", "", nil)
	second := send(h, "GET", "/pairs?hours=24", "", nil)
	if first.Header().Get("X-Cache") != "MISS" || second.Header().Get("X-Cache") != "HIT" || up.calls.Load() != 1 {
		t.Fatalf("cache: %q %q calls=%d", first.Header().Get("X-Cache"), second.Header().Get("X-Cache"), up.calls.Load())
	}
	send(h, "GET", "/status", "", nil)
	send(h, "GET", "/status", "", nil)
	if up.calls.Load() != 3 {
		t.Fatal("/status must not be cached")
	}
	if rec := send(h, "GET", "/ui/", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ui") || rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("ui: %d %v", rec.Code, rec.Header())
	}
	if rec := send(h, "GET", "/ui/config.js", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"/stats/tables"`) {
		t.Fatalf("ui config: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, "GET", "/", "", map[string]string{"Accept": "text/html"}); rec.Code != 302 {
		t.Fatalf("browser root: %d", rec.Code)
	}
}

func TestUpstreamFailures(t *testing.T) {
	up := &upstream{}
	srv := httptest.NewServer(up)
	h := newGateway(t, srv.URL, func(c *Config) { c.UpstreamKey = "wrong" })
	rec := send(h, "GET", "/ledgers", "", nil)
	if rec.Code != 502 || strings.Contains(rec.Body.String(), "internal detail") {
		t.Fatalf("misconfigured gateway key must be a 502 without upstream detail: %d %s", rec.Code, rec.Body)
	}
	// A 401 on a label write is the client's token problem and passes through.
	if rec := send(h, "POST", "/labels", "{}", nil); rec.Code != 401 {
		t.Fatalf("label write 401 must pass through: %d", rec.Code)
	}
	srv.Close()
	h = newGateway(t, srv.URL, nil)
	if rec := send(h, "GET", "/ledgers", "", nil); rec.Code != 502 || !strings.Contains(rec.Body.String(), "unavailable") {
		t.Fatalf("down upstream: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, "GET", "/ready", "", nil); rec.Code != 503 {
		t.Fatalf("ready with upstream down: %d", rec.Code)
	}
	if rec := send(h, "GET", "/health", "", nil); rec.Code != 200 {
		t.Fatalf("health is local: %d", rec.Code)
	}
}

func TestReadyAndRateLimit(t *testing.T) {
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	h := newGateway(t, srv.URL, func(c *Config) { c.RateRPS, c.RateBurst, c.CacheTTL = 1, 2, 0 })
	if rec := send(h, "GET", "/ready", "", nil); rec.Code != 503 {
		t.Fatalf("not ready upstream: %d", rec.Code)
	}
	up.ready.Store(true)
	if rec := send(h, "GET", "/ready", "", nil); rec.Code != 200 {
		t.Fatalf("ready: %d", rec.Code)
	}
	codes := []int{}
	for i := 0; i < 4; i++ {
		codes = append(codes, send(h, "GET", "/ledgers", "", nil).Code)
	}
	if codes[0] != 200 || codes[3] != 429 {
		t.Fatalf("rate limit codes %v", codes)
	}
}

func TestPublicAPIKeys(t *testing.T) {
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	h := newGateway(t, srv.URL, func(c *Config) { c.APIKeys = []string{"public-key"} })
	if rec := send(h, "GET", "/ledgers", "", nil); rec.Code != 401 {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := send(h, "GET", "/ledgers", "", map[string]string{"X-API-Key": "public-key"}); rec.Code != 200 || up.lastKey.Load() != "internal-secret" {
		t.Fatalf("public key must be swapped for the internal one: %d %q", rec.Code, up.lastKey.Load())
	}
	if rec := send(h, "GET", "/ui/", "", nil); rec.Code != 200 {
		t.Fatalf("ui must stay public: %d", rec.Code)
	}
}

// ---- mTLS -------------------------------------------------------------------

type ca struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newCA(t *testing.T) ca {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"}, IsCA: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return ca{cert, key, pool}
}

func (c ca) issue(t *testing.T, cn string, usage x509.ExtKeyUsage) tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		DNSNames: []string{cn}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestMutualTLS(t *testing.T) {
	auth := newCA(t)
	up := &upstream{}
	srv := httptest.NewUnstartedServer(up)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientCAs: auth.pool, ClientAuth: tls.RequireAndVerifyClientCert,
		Certificates: []tls.Certificate{auth.issue(t, "indexer-api", x509.ExtKeyUsageServerAuth)}}
	srv.StartTLS()
	defer srv.Close()

	withCert := newGateway(t, srv.URL, func(c *Config) {
		c.UpstreamTLS = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: auth.pool, ServerName: "indexer-api",
			Certificates: []tls.Certificate{auth.issue(t, "gateway", x509.ExtKeyUsageClientAuth)}}
	})
	if rec := send(withCert, "GET", "/ledgers", "", nil); rec.Code != 200 {
		t.Fatalf("mTLS with a valid client cert: %d %s", rec.Code, rec.Body)
	}
	noCert := newGateway(t, srv.URL, func(c *Config) {
		c.UpstreamTLS = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: auth.pool, ServerName: "indexer-api"}
	})
	if rec := send(noCert, "GET", "/ledgers", "", nil); rec.Code != 502 {
		t.Fatalf("upstream accepted a client without certificate: %d", rec.Code)
	}
	other := newCA(t)
	foreign := newGateway(t, srv.URL, func(c *Config) {
		c.UpstreamTLS = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: auth.pool, ServerName: "indexer-api",
			Certificates: []tls.Certificate{other.issue(t, "gateway", x509.ExtKeyUsageClientAuth)}}
	})
	if rec := send(foreign, "GET", "/ledgers", "", nil); rec.Code != 502 {
		t.Fatalf("upstream accepted a certificate from another CA: %d", rec.Code)
	}
}
