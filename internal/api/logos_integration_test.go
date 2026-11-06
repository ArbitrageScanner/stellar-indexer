package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/api"
	"github.com/ArbitrageScanner/stellar-indexer/internal/logos"
)

func TestLogosIntegration(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	const (
		usdc = "USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
		sac  = "CCW67TSZV3SSS2HXMBQ5JFGCKJNXKZM7UQUWUZPUTHXSTZLEO7SJMI75"
		none = "FOO:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
	)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	if _, err := db.Pool.Exec(ctx, `INSERT INTO assets (asset_key, type, code, issuer, contract_id, first_seen) VALUES
		($1, 'credit_alphanum4', 'USDC', 'GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN', $2, 1),
		($3, 'credit_alphanum4', 'FOO', 'GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN', NULL, 2),
		('native', 'native', 'XLM', NULL, NULL, 0)`, usdc, sac, none); err != nil {
		t.Fatal(err)
	}

	pg := logos.PG{DB: db.Pool}
	due, err := pg.Due(ctx, 10, time.Hour, time.Hour)
	if err != nil || len(due) != 2 {
		t.Fatalf("Due = %v, %v; want the two issued assets (never native)", due, err)
	}
	ok := logos.Result{Key: usdc, Status: logos.StatusOK, HomeDomain: "centre.io", ImageURL: "https://centre.io/usdc.png",
		ContentType: "image/png", Data: png, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if err := pg.Save(ctx, ok); err != nil {
		t.Fatal(err)
	}
	if err := pg.Save(ctx, logos.Result{Key: none, Status: logos.StatusNone, Detail: "no image"}); err != nil {
		t.Fatal(err)
	}
	// A transient failure on recheck keeps the stored image.
	if err := pg.Save(ctx, logos.Result{Key: usdc, Status: logos.StatusError, Detail: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if due, _ := pg.Due(ctx, 10, time.Hour, time.Hour); len(due) != 0 {
		t.Fatalf("Due after save = %v, want none within the TTL", due)
	}

	h := api.New(db.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Config{}).Handler()
	get := func(path string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/logos", nil)
	var idx struct {
		Logos []struct{ Asset, Contract, V string } `json:"logos"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &idx) != nil || len(idx.Logos) != 1 ||
		idx.Logos[0].Asset != usdc || idx.Logos[0].Contract != sac || idx.Logos[0].V != "0123456789ab" {
		t.Fatalf("/logos = %d %s", rec.Code, rec.Body.String())
	}

	for _, key := range []string{usdc, sac} {
		rec = get("/assets/"+key+"/logo?v=0123456789ab", nil)
		if rec.Code != 200 || rec.Body.String() != string(png) || rec.Header().Get("Content-Type") != "image/png" ||
			rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("logo %s: %d %q %v", key, rec.Code, rec.Body.String(), rec.Header())
		}
	}
	if rec = get("/assets/"+usdc+"/logo", nil); rec.Header().Get("Cache-Control") != "public, max-age=3600" {
		t.Errorf("unversioned logo Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if rec = get("/assets/"+usdc+"/logo", map[string]string{"If-None-Match": rec.Header().Get("ETag")}); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d, want 304", rec.Code)
	}
	for _, key := range []string{none, "native", "NOPE:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"} {
		if rec = get("/assets/"+key+"/logo", nil); rec.Code != http.StatusNotFound {
			t.Errorf("logo %s: %d, want 404", key, rec.Code)
		}
	}
}
