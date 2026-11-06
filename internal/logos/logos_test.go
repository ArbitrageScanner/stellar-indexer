package logos

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/stellartoml"
)

var png1 = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")

// pngOf encodes a real w×h image.
func pngOf(t *testing.T, w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.NRGBA{R: 200, A: 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestNormalize(t *testing.T) {
	big := pngOf(t, 600, 300)
	out, err := normalize(append(big, []byte("<script>trailing payload</script>")...))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != LogoSize || cfg.Height != LogoSize/2 {
		t.Fatalf("normalized to %dx%d (%v), want %dx%d", cfg.Width, cfg.Height, err, LogoSize, LogoSize/2)
	}
	if bytes.Contains(out, []byte("script")) {
		t.Error("trailing bytes survived re-encoding")
	}
	small, _ := normalize(pngOf(t, 32, 32))
	if c, _ := png.DecodeConfig(bytes.NewReader(small)); c.Width != 32 {
		t.Errorf("small image resized to %d, want it kept at 32", c.Width)
	}
	if _, err := normalize(png1); err == nil {
		t.Error("a truncated PNG was accepted")
	}
}

func TestSniff(t *testing.T) {
	cases := map[string]string{
		string(png1):                   "image/png",
		"\xff\xd8\xff\xe0\x00\x10JFIF": "image/jpeg",
		"GIF89a\x01\x00":               "image/gif",
		"RIFF\x10\x00\x00\x00WEBPVP8 ": "image/webp",
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`: "",
		`<?xml version="1.0"?><svg/>`:  "",
		"<html><body>hi</body></html>": "",
		"":                             "",
	}
	for in, want := range cases {
		if got := Sniff([]byte(in)); got != want {
			t.Errorf("Sniff(%q) = %q, want %q", trim(in), got, want)
		}
	}
}

func TestPublicIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "224.0.0.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1"} {
		if publicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s treated as public", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s treated as non-public", s)
		}
	}
}

func TestSafeClientRefusesLoopback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(png1) }))
	defer ts.Close()
	_, err := SafeClient(5 * time.Second).Get(ts.URL)
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("expected a non-public refusal, got %v", err)
	}
}

func TestCheckImageURL(t *testing.T) {
	for _, bad := range []string{"http://example.com/a.png", "https://127.0.0.1/a.png", "https://[::1]/a.png",
		"https://example.com:8443/a.png", "https://user@example.com/a.png", "https://localhost/a.png", "javascript:alert(1)", "data:image/png;base64,AA"} {
		if checkImageURL(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := checkImageURL("https://cdn.example.com/icons/usdc.png"); err != nil {
		t.Errorf("rejected a good URL: %v", err)
	}
}

// fakes

type memStore struct {
	due     []Asset
	domains map[string]string
	saved   map[string]Result
}

func (m *memStore) Due(context.Context, int, time.Duration, time.Duration) ([]Asset, error) {
	return m.due, nil
}
func (m *memStore) HomeDomains(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if d := m.domains[id]; d != "" {
			out[id] = d
		}
	}
	return out, nil
}
func (m *memStore) Save(_ context.Context, r Result) error { m.saved[r.Key] = r; return nil }

type fakeToml struct {
	byDomain map[string]*stellartoml.Response
	calls    map[string]int
}

func (f *fakeToml) GetStellarToml(domain string) (*stellartoml.Response, error) {
	f.calls[domain]++
	if r, ok := f.byDomain[domain]; ok {
		return r, nil
	}
	return nil, errors.New("http request failed with non-200 status code")
}

func TestResolverStep(t *testing.T) {
	logo := pngOf(t, 256, 256)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/usdc.png":
			_, _ = w.Write(logo)
		case "/evil.svg":
			w.Header().Set("Content-Type", "image/png") // lies; the sniff decides
			_, _ = io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
		case "/huge.png":
			_, _ = w.Write(append(append([]byte{}, png1...), make([]byte, MaxDownloadBytes)...))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	const iss, other, nodomain, rpcOnly = "GISSUER", "GOTHER", "GNODOMAIN", "GRPC"
	st := &memStore{
		due: []Asset{
			{Key: "USDC:" + iss, Code: "USDC", Issuer: iss},
			{Key: "EVIL:" + iss, Code: "EVIL", Issuer: iss},
			{Key: "HUGE:" + iss, Code: "HUGE", Issuer: iss},
			{Key: "USDC:" + other, Code: "USDC", Issuer: other}, // claims the same domain, not listed there
			{Key: "X:" + nodomain, Code: "X", Issuer: nodomain},
			{Key: "R:" + rpcOnly, Code: "R", Issuer: rpcOnly},
		},
		domains: map[string]string{iss: "issuer.example", other: "Issuer.Example"},
		saved:   map[string]Result{},
	}
	toml := &fakeToml{calls: map[string]int{}, byDomain: map[string]*stellartoml.Response{
		"issuer.example": {Currencies: []stellartoml.Currency{
			{Code: "USDC", Issuer: iss, Image: ts.URL + "/usdc.png"},
			{Code: "EVIL", Issuer: iss, Image: ts.URL + "/evil.svg"},
			{Code: "HUGE", Issuer: iss, Image: ts.URL + "/huge.png"},
		}},
	}}
	lookups := 0
	r := &Resolver{
		Store: st, Toml: toml, HTTP: ts.Client(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Batch: 10,
		Lookup: func(_ context.Context, ids []string) (map[string]string, error) {
			lookups++
			for _, id := range ids {
				if id == iss || id == other {
					t.Errorf("RPC lookup asked for %s, which the index knows", id)
				}
			}
			return map[string]string{rpcOnly: "down.example"}, nil
		},
		checkURL: func(string) error { return nil },
	}
	n, err := r.Step(context.Background())
	if err != nil || n != len(st.due) {
		t.Fatalf("Step = %d, %v", n, err)
	}
	want := map[string]string{
		"USDC:" + iss: StatusOK, "EVIL:" + iss: StatusNone, "HUGE:" + iss: StatusNone,
		"USDC:" + other: StatusNone, "X:" + nodomain: StatusNone, "R:" + rpcOnly: StatusError,
	}
	for k, w := range want {
		if got := st.saved[k]; got.Status != w {
			t.Errorf("%s: status %q (%s), want %q", k, got.Status, got.Detail, w)
		}
	}
	ok := st.saved["USDC:"+iss]
	c, cerr := png.DecodeConfig(bytes.NewReader(ok.Data))
	if ok.ContentType != "image/png" || cerr != nil || c.Width != LogoSize || len(ok.SHA256) != 64 || ok.HomeDomain != "issuer.example" {
		t.Errorf("stored result: %+v", ok)
	}
	if toml.calls["issuer.example"] != 1 {
		t.Errorf("stellar.toml fetched %d times for one domain in a batch", toml.calls["issuer.example"])
	}
	if lookups != 1 {
		t.Errorf("RPC lookups = %d, want one batched call", lookups)
	}
}
