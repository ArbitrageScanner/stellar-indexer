package api_test

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/ArbitrageScanner/stellar-indexer/internal/api"
	"github.com/ArbitrageScanner/stellar-indexer/internal/processor"
	"github.com/ArbitrageScanner/stellar-indexer/internal/store"
	"github.com/ArbitrageScanner/stellar-indexer/web"
)

// Runs only with TEST_DATABASE_URL set, e.g.
//
//	TEST_DATABASE_URL=postgres://indexer:indexer@localhost:5432/indexer?sslmode=disable go test ./...
//
// Everything happens inside a throwaway schema that is dropped afterwards.
func setup(t *testing.T) *store.Store {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("itest_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func fixture(t *testing.T, seq string) xdr.LedgerCloseMeta {
	f, err := os.Open("../processor/testdata/lcm_" + seq + ".b64.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, _ := gzip.NewReader(f)
	raw, _ := io.ReadAll(zr)
	bin, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var lcm xdr.LedgerCloseMeta
	if err := xdr.SafeUnmarshal(bin, &lcm); err != nil {
		t.Fatal(err)
	}
	return lcm
}

// verify stands in for the protocol verifier: Aquarius and Blend code is
// "allowlisted", Soroswap is not, so both paths are exercised.
func verify(b *processor.Batch) {
	ok := map[string]bool{"aquarius": true, "blend": true}
	for i := range b.Trades {
		if t := &b.Trades[i]; t.Venue != "sdex" && t.Venue != "amm" {
			t.Verified = ok[t.Venue]
		}
	}
	for i := range b.ProtocolEvents {
		b.ProtocolEvents[i].Verified = ok[b.ProtocolEvents[i].Protocol]
	}
	for i := range b.ContractEvents {
		b.ContractEvents[i].Verified = ok[b.ContractEvents[i].Protocol]
	}
}

const (
	arbTx   = "04165e297a4c79ce29ab848d320698f12c3504f6521eb2b2182a28c54f12aff8"
	arbAcct = "CBNVK5PE7JCL773P5SHWE3YUCHQVVVPVOG72SKCEFVTZOLLQWVTCPLZ3"
	aqua    = "AQUA:GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"
)

func TestAPIIntegration(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	reg := processor.NewRegistry(network.PublicNetworkPassphrase)
	p := processor.New(network.PublicNetworkPassphrase, reg, processor.Options{Operations: true, State: true, Codes: true})
	var batches []*processor.Batch
	for _, seq := range []string{"65311337", "65312802"} {
		res, err := p.Process(fixture(t, seq))
		if err != nil {
			t.Fatal(err)
		}
		verify(res.Batch)
		batches = append(batches, res.Batch)
	}

	t.Run("write", func(t *testing.T) {
		st, err := db.Write(ctx, "test", batches)
		if err != nil || st.Ledgers != 2 || st.Rows == 0 {
			t.Fatalf("write stats %+v %v", st, err)
		}
		var vol1, vol2 float64
		_ = db.Pool.QueryRow(ctx, "SELECT sum(base_volume) FROM candles_1m").Scan(&vol1)
		if st, err = db.Write(ctx, "test", batches); err != nil || st.Skipped != 2 || st.Ledgers != 0 {
			t.Fatalf("rewrite: %+v %v", st, err)
		}
		_ = db.Pool.QueryRow(ctx, "SELECT sum(base_volume) FROM candles_1m").Scan(&vol2)
		if vol1 != vol2 || vol1 == 0 {
			t.Fatalf("candle volume changed on rewrite: %v -> %v", vol1, vol2)
		}
	})

	cfg := api.Config{Network: "pubnet", CursorName: "test", UI: web.FS(), LabelsOpen: true, CacheTTL: time.Minute}
	srv := httptest.NewServer(api.New(db.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg).Handler())
	defer srv.Close()
	call := func(t *testing.T, method, path, body string, want int) any {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status %d (want %d): %s", method, path, resp.StatusCode, want, raw)
		}
		var v any
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
		return v
	}
	get := func(t *testing.T, path string, want int) any { t.Helper(); return call(t, "GET", path, "", want) }
	obj := func(v any) map[string]any { m, _ := v.(map[string]any); return m }
	arr := func(v any) []any { a, _ := v.([]any); return a }

	t.Run("status", func(t *testing.T) {
		get(t, "/", 200)
		get(t, "/health", 200)
		get(t, "/ready", 200)
		st := obj(get(t, "/status", 200))
		if st["cursor"].(float64) != 65312802 || st["gaps"].(float64) != 1 || len(arr(st["components"])) == 0 {
			t.Fatalf("status: %v", st)
		}
		if res := arr(get(t, "/search?q=65311337", 200)); len(res) == 0 || obj(res[0])["kind"] != "ledger" {
			t.Fatalf("search ledger: %v", res)
		}
		get(t, "/stats/tables", 200)
		get(t, "/openapi.yaml", 200)
	})

	t.Run("explorer", func(t *testing.T) {
		led := obj(get(t, "/ledgers/65311337", 200))
		if len(arr(led["transactions"])) == 0 {
			t.Fatal("ledger has no transactions")
		}
		get(t, "/ledgers/1", 404)
		get(t, "/ledgers", 200)
		tx := obj(get(t, "/transactions/"+arbTx, 200))
		if n := len(arr(tx["trades"])); n != 4 {
			t.Fatalf("arb tx trades = %d, want 4", n)
		}
		if obj(arr(tx["trades"])[0])["verified"] != true || len(arr(tx["protocol_events"])) == 0 || len(arr(tx["transfers"])) == 0 {
			t.Fatal("arb tx trades unverified or missing events/transfers")
		}
		page1 := arr(get(t, "/transactions?limit=3", 200))
		last := obj(page1[2])["id"].(string)
		page2 := arr(get(t, "/transactions?limit=3&before="+last, 200))
		if len(page2) != 3 || obj(page2[0])["id"].(string) == last {
			t.Fatal("transactions pagination repeated a row")
		}
		get(t, "/operations?type=invoke_host_function&limit=2", 200)
		get(t, "/transfers?limit=3&type=transfer", 200)
		get(t, "/transfers?before=1.2", 400)
		get(t, "/events?protocol=none&limit=3", 200)
		get(t, "/contracts?hours=2160", 200)
		c := obj(get(t, "/contracts/CA4HTZNY2RBZWEQE5GBMNREZMFRPAZSVJ6OGPC7T3VM7NHRJYFAVID2S", 200))
		if _, ok := c["verification"]; !ok {
			t.Fatalf("contract summary lacks verification: %v", c)
		}
		get(t, "/contracts/CA4HTZNY2RBZWEQE5GBMNREZMFRPAZSVJ6OGPC7T3VM7NHRJYFAVID2S/events?limit=2", 200)
	})

	t.Run("markets", func(t *testing.T) {
		get(t, "/assets?hours=2160", 200)
		get(t, "/assets/native", 200)
		get(t, "/assets/"+aqua+"/holders", 200)
		get(t, "/pairs?hours=2160", 200)
		tr := arr(get(t, "/trades?limit=2", 200))
		t0 := obj(tr[1])
		cur := fmt.Sprintf("%.0f.%.0f.%.0f.%.0f", t0["ledger"], t0["tx_index"], t0["op_index"], t0["trade_index"])
		if next := arr(get(t, "/trades?limit=1&before="+cur, 200)); len(next) != 1 {
			t.Fatal("trades pagination")
		}
		get(t, "/trades?base=XLM", 400)
		if un := arr(get(t, "/trades?verified=false&venue=soroswap", 200)); len(un) == 0 {
			t.Fatal("soroswap fills should be stored (unverified)")
		}
		price := obj(get(t, "/price?base=native&counter="+aqua, 200))
		if last, _ := price["last"].(float64); last < 100 || last > 10000 {
			t.Fatalf("XLM/AQUA price implausible: %v", price)
		}
		inv := obj(get(t, "/price?base="+aqua+"&counter=native", 200))
		if l1, l2 := price["last"].(float64), inv["last"].(float64); l1*l2 < 0.999 || l1*l2 > 1.001 {
			t.Fatalf("inverted price mismatch %v * %v", l1, l2)
		}
		cs := obj(get(t, "/candles?base=native&counter="+aqua+"&interval=1h", 200))
		if len(arr(cs["candles"])) == 0 {
			t.Fatal("no candles (windows must be anchored to the index, not the wall clock)")
		}
		get(t, "/candles?base=a&counter=b&interval=7m", 400)
		get(t, "/pools", 200)
	})

	t.Run("protocols", func(t *testing.T) {
		pr := obj(get(t, "/protocols?hours=2160", 200))
		var soroswap map[string]any
		for _, p := range arr(pr["protocols"]) {
			if obj(p)["protocol"] == "soroswap" {
				soroswap = obj(p)
			}
		}
		if soroswap == nil || soroswap["events"].(float64) != 0 || soroswap["unverified_events"].(float64) == 0 {
			t.Fatalf("soroswap must count as unverified by default: %v", soroswap)
		}
		if n := len(arr(get(t, "/protocols/soroswap/events", 200))); n != 0 {
			t.Fatalf("unverified events listed by default: %d", n)
		}
		if n := len(arr(get(t, "/protocols/soroswap/events?verified=all", 200))); n == 0 {
			t.Fatal("verified=all must include unverified events")
		}
		if ev := arr(get(t, "/protocols/blend/events", 200)); len(ev) == 0 {
			t.Fatal("no blend events")
		}
		blend := obj(get(t, "/protocols/blend/summary?hours=2160", 200))
		if len(arr(blend["by_asset"])) == 0 || len(arr(blend["timeline"])) == 0 {
			t.Fatalf("blend summary empty: %v", blend)
		}
		if aq := obj(get(t, "/protocols/aquarius/summary?hours=2160", 200)); len(arr(aq["pools"])) == 0 {
			t.Fatal("aquarius summary has no pools")
		}
		get(t, "/stats/network?hours=2160", 200)
		get(t, "/stats/operations?hours=2160", 200)
	})

	t.Run("accounts", func(t *testing.T) {
		get(t, "/accounts/"+arbAcct, 200)
		get(t, "/accounts/"+arbAcct+"/trades", 200)
		get(t, "/accounts/"+arbAcct+"/events", 200)
		get(t, "/accounts/"+arbAcct+"/operations", 200)
		get(t, "/accounts/"+arbAcct+"/transfers", 200)
		get(t, "/accounts/top", 200)
		pf := obj(get(t, "/accounts/"+arbAcct+"/portfolio", 200))
		if _, ok := pf["holdings"].([]any); !ok {
			t.Fatalf("portfolio: %v", pf)
		}
		st := obj(get(t, "/accounts/"+arbAcct+"/stats?hours=2160", 200))
		trd := obj(st["trading"])
		if trd["trades"].(float64) < 4 || trd["cycles"].(float64) < 1 {
			t.Fatalf("arb stats: trades %v cycles %v", trd["trades"], trd["cycles"])
		}
		for _, k := range []string{"heatmap", "counterparties", "largest_transfers", "flows", "protocols"} {
			if _, ok := st[k].([]any); !ok {
				t.Fatalf("stats missing %s", k)
			}
		}
		tags := arr(get(t, "/accounts/"+arbAcct+"/tags", 200))
		found := false
		for _, tg := range tags {
			found = found || obj(tg)["id"] == "arbitrage_bot" || obj(tg)["id"] == "flash_loans"
		}
		if !found {
			t.Fatalf("arb contract lacks behavioural tags: %v", tags)
		}
	})

	t.Run("labels", func(t *testing.T) {
		call(t, "POST", "/labels", `{"address":"`+arbAcct+`","tag":"Arb-Bot","note":"flash loan arb"}`, 200)
		call(t, "POST", "/labels", `{"address":"nope","tag":"x"}`, 400)
		if l := arr(get(t, "/labels?tag=%23arb-bot", 200)); len(l) != 1 {
			t.Fatalf("labels by tag: %v", l)
		}
		found := false
		for _, tg := range arr(get(t, "/accounts/"+arbAcct+"/tags", 200)) {
			found = found || obj(tg)["id"] == "arb-bot"
		}
		if !found {
			t.Fatal("user tag missing")
		}
		if res := arr(get(t, "/search?q=%23arb", 200)); len(res) == 0 || obj(res[0])["kind"] != "tag" {
			t.Fatalf("tag search: %v", res)
		}
		get(t, "/labels/tags", 200)
		call(t, "DELETE", "/labels/"+arbAcct+"/arb-bot", "", 200)
		call(t, "DELETE", "/labels/"+arbAcct+"/arb-bot", "", 404)
	})

	t.Run("insights", func(t *testing.T) {
		call(t, "POST", "/labels", `{"address":"`+arbAcct+`","tag":"watched"}`, 200)
		lb := obj(get(t, "/leaderboards?hours=2160", 200))
		if len(arr(lb["arbitrageurs"])) == 0 || !strings.Contains(fmt.Sprint(lb["arbitrageurs"]), "watched") {
			t.Fatalf("arbitrageurs board missing or unlabelled: %v", lb["arbitrageurs"])
		}
		al := obj(get(t, "/alerts?hours=2160&min_xlm=1", 200))
		if obj(al["counts"])["flash_loan"].(float64) == 0 {
			t.Fatalf("alerts: %v", al["counts"])
		}
		// Cached endpoints serve the second identical request from cache.
		req, _ := http.NewRequest("GET", srv.URL+"/alerts?hours=2160&min_xlm=1", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.Header.Get("X-Cache") != "HIT" {
			t.Fatalf("X-Cache = %q", resp.Header.Get("X-Cache"))
		}
	})

	t.Run("ui", func(t *testing.T) {
		for path, want := range map[string]string{"/ui/": "Stellar Indexer", "/ui/js/app.js": "hashchange", "/ui/css/app.css": "--surface"} {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 200 || !strings.Contains(string(body), want) {
				t.Fatalf("%s: %d, missing %q", path, resp.StatusCode, want)
			}
		}
		req, _ := http.NewRequest("GET", srv.URL+"/", nil)
		req.Header.Set("Accept", "text/html")
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/" {
			t.Fatalf("browser redirect: %v %v", resp, err)
		}
	})
}
