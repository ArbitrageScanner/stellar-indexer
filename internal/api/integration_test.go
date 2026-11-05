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
	"strconv"
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
		admin.Close(context.Background())
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
	defer f.Close()
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

func TestWriteAndQuery(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	reg := processor.NewRegistry(network.PublicNetworkPassphrase)
	p := processor.New(network.PublicNetworkPassphrase, reg, processor.Options{Operations: true, State: true})

	var batches []*processor.Batch
	for _, seq := range []string{"65311337", "65312802"} {
		res, err := p.Process(fixture(t, seq))
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, res.Batch)
	}
	st, err := db.Write(ctx, "test", batches)
	if err != nil {
		t.Fatal(err)
	}
	if st.Ledgers != 2 || st.Rows == 0 {
		t.Fatalf("write stats %+v", st)
	}
	// Re-writing the same ledgers is a no-op (no double-counted candles).
	var vol1, vol2 float64
	_ = db.Pool.QueryRow(ctx, "SELECT sum(base_volume) FROM candles_1m").Scan(&vol1)
	st, err = db.Write(ctx, "test", batches)
	if err != nil || st.Skipped != 2 || st.Ledgers != 0 {
		t.Fatalf("rewrite: %+v %v", st, err)
	}
	_ = db.Pool.QueryRow(ctx, "SELECT sum(base_volume) FROM candles_1m").Scan(&vol2)
	if vol1 != vol2 || vol1 == 0 {
		t.Fatalf("candle volume changed on rewrite: %v -> %v", vol1, vol2)
	}
	if cur, _ := db.Cursor(ctx, "test"); cur != 65312802 {
		t.Fatalf("cursor %d", cur)
	}
	// A fresh registry learns SAC mappings from the DB.
	reg2 := processor.NewRegistry(network.PublicNetworkPassphrase)
	if n, err := db.LoadAssets(ctx, reg2); err != nil || n == 0 {
		t.Fatalf("load assets: %d %v", n, err)
	}
	if got := reg2.Lookup("CAS3J7GYLGXMF6TDJBBYYSE3HQ6BBSMLNUQ34T6TZMYMW2EVH34XOWMA"); got != "native" {
		t.Fatalf("XLM SAC lookup = %q", got)
	}

	a := api.New(db.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, "pubnet", "test")
	a.UI = web.FS()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	get := func(path string, want int) any {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s: status %d: %s", path, resp.StatusCode, body)
		}
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return v
	}
	get("/", 200)
	get("/health", 200)
	get("/status", 200)
	get("/ledgers", 200)
	led := get("/ledgers/65311337", 200).(map[string]any)
	if len(led["transactions"].([]any)) == 0 {
		t.Fatal("ledger has no transactions")
	}
	get("/ledgers/1", 404)
	tx := get("/transactions/04165e297a4c79ce29ab848d320698f12c3504f6521eb2b2182a28c54f12aff8", 200).(map[string]any)
	if n := len(tx["trades"].([]any)); n != 4 {
		t.Fatalf("arb tx trades = %d, want 4", n)
	}
	if len(tx["protocol_events"].([]any)) == 0 || len(tx["transfers"].([]any)) == 0 {
		t.Fatal("arb tx missing protocol events / transfers")
	}
	get("/accounts/CBNVK5PE7JCL773P5SHWE3YUCHQVVVPVOG72SKCEFVTZOLLQWVTCPLZ3", 200)
	get("/accounts/CBNVK5PE7JCL773P5SHWE3YUCHQVVVPVOG72SKCEFVTZOLLQWVTCPLZ3/trades", 200)
	get("/accounts/top", 200)
	get("/assets?hours=100000", 200)
	get("/assets/native", 200)
	get("/pairs?hours=100000", 200)
	get("/trades?venue=aquarius", 200)
	get("/trades?base=XLM", 400)
	price := get("/price?base=native&counter=AQUA:GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA", 200).(map[string]any)
	if last, _ := price["last"].(float64); last < 100 || last > 10000 {
		t.Fatalf("XLM/AQUA price implausible: %v", price)
	}
	inv := get("/price?base=AQUA:GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA&counter=native", 200).(map[string]any)
	if l1, l2 := price["last"].(float64), inv["last"].(float64); l1*l2 < 0.999 || l1*l2 > 1.001 {
		t.Fatalf("inverted price mismatch %v * %v", l1, l2)
	}
	c := get("/candles?base=native&counter=AQUA:GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA&interval=1h&from=2026-11-01T00:00:00Z&to=2026-11-06T00:00:00Z", 200).(map[string]any)
	if len(c["candles"].([]any)) == 0 {
		t.Fatal("no candles")
	}
	get("/candles?base=a&counter=b&interval=7m", 400)
	get("/pools", 200)
	get("/protocols?hours=100000", 200)
	if ev := get("/protocols/blend/events", 200).([]any); len(ev) == 0 {
		t.Fatal("no blend events")
	}
	get("/contracts/CA4HTZNY2RBZWEQE5GBMNREZMFRPAZSVJ6OGPC7T3VM7NHRJYFAVID2S/events", 200)
	get("/stats/network?hours=100000", 200)
	get("/stats/operations?hours=100000", 200)

	// Explorer feeds: keyset pagination never repeats or skips rows.
	page1 := get("/transactions?limit=3", 200).([]any)
	last := page1[2].(map[string]any)["id"].(string)
	page2 := get("/transactions?limit=3&before="+last, 200).([]any)
	lastID, _ := strconv.ParseInt(last, 10, 64)
	nextID, _ := strconv.ParseInt(page2[0].(map[string]any)["id"].(string), 10, 64)
	if len(page1) != 3 || len(page2) != 3 || nextID >= lastID {
		t.Fatalf("transactions pagination: %d then %d", lastID, nextID)
	}
	if n := len(get("/transactions?soroban=true&successful=true&limit=500", 200).([]any)); n == 0 {
		t.Fatal("no soroban txs")
	}
	get("/operations?type=invoke_host_function&limit=2", 200)
	tr := get("/trades?limit=2", 200).([]any)
	tr0 := tr[1].(map[string]any)
	cur := fmt.Sprintf("%.0f.%.0f.%.0f.%.0f", tr0["ledger"], tr0["tx_index"], tr0["op_index"], tr0["trade_index"])
	if next := get("/trades?limit=1&before="+cur, 200).([]any); len(next) != 1 || next[0].(map[string]any)["trade_index"] == tr0["trade_index"] && next[0].(map[string]any)["tx_index"] == tr0["tx_index"] {
		t.Fatalf("trades pagination repeated row %v", cur)
	}
	get("/trades?before=1.2", 400)
	get("/transfers?limit=3&type=transfer", 200)
	get("/events?limit=3", 200)
	get("/events?protocol=none&limit=3", 200)
	get("/contracts?hours=2160", 200)
	get("/contracts/CA4HTZNY2RBZWEQE5GBMNREZMFRPAZSVJ6OGPC7T3VM7NHRJYFAVID2S", 200)
	blend := get("/protocols/blend/summary?hours=2160", 200).(map[string]any)
	if len(blend["by_asset"].([]any)) == 0 || len(blend["timeline"].([]any)) == 0 {
		t.Fatalf("blend summary empty: %v", blend)
	}
	if aq := get("/protocols/aquarius/summary?hours=2160", 200).(map[string]any); len(aq["pools"].([]any)) == 0 {
		t.Fatal("aquarius summary has no pools")
	}
	get("/accounts/CBNVK5PE7JCL773P5SHWE3YUCHQVVVPVOG72SKCEFVTZOLLQWVTCPLZ3/events", 200)
	get("/assets/native/holders", 200)
	get("/assets/AQUA:GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA/holders", 200)
	get("/stats/tables", 200)
	for q, kind := range map[string]string{
		"65311337": "ledger", "04165e297a4c79ce29ab848d320698f12c3504f6521eb2b2182a28c54f12aff8": "transaction",
		"AQUA": "asset", "CA4HTZNY2RBZWEQE5GBMNREZMFRPAZSVJ6OGPC7T3VM7NHRJYFAVID2S": "contract",
	} {
		res := get("/search?q="+q, 200).([]any)
		if len(res) == 0 || res[0].(map[string]any)["kind"] != kind {
			t.Fatalf("search %s: %v", q, res)
		}
	}
	get("/search", 400)

	// Portfolio, indicators, tags. Prices depend on wall-clock windows, so only
	// structure and trade-derived numbers are asserted here.
	arb := "CBNVK5PE7JCL773P5SHWE3YUCHQVVVPVOG72SKCEFVTZOLLQWVTCPLZ3"
	pf := get("/accounts/"+arb+"/portfolio", 200).(map[string]any)
	if _, ok := pf["holdings"].([]any); !ok {
		t.Fatalf("portfolio: %v", pf)
	}
	stats := get("/accounts/"+arb+"/stats?hours=2160", 200).(map[string]any)
	trd := stats["trading"].(map[string]any)
	if trd["trades"].(float64) < 4 || trd["cycles"].(float64) < 1 {
		t.Fatalf("arb stats: trades %v cycles %v", trd["trades"], trd["cycles"])
	}
	for _, k := range []string{"heatmap", "counterparties", "largest_transfers", "flows", "protocols"} {
		if _, ok := stats[k].([]any); !ok {
			t.Fatalf("stats missing %s", k)
		}
	}
	get("/accounts/"+arb+"/tags", 200)
	lb := get("/leaderboards?hours=2160", 200).(map[string]any)
	if arbs := lb["arbitrageurs"].([]any); len(arbs) == 0 {
		t.Fatal("no arbitrageurs on fixtures")
	}
	al := get("/alerts?hours=2160&min_xlm=1", 200).(map[string]any)
	if al["counts"].(map[string]any)["flash_loan"].(float64) == 0 {
		t.Fatalf("alerts: %v", al["counts"])
	}

	// Labels: CRUD, validation, optional bearer token, CORS preflight.
	post := func(body string, auth string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/labels", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post(`{"address":"`+arb+`","tag":"Arb-Bot","note":"flash loan arb"}`, ""); c != 200 {
		t.Fatalf("post label: %d", c)
	}
	if c := post(`{"address":"nope","tag":"x"}`, ""); c != 400 {
		t.Fatalf("bad address: %d", c)
	}
	if c := post(`{"address":"`+arb+`","tag":"has space"}`, ""); c != 400 {
		t.Fatalf("bad tag: %d", c)
	}
	if l := get("/labels?tag=arb-bot", 200).([]any); len(l) != 1 {
		t.Fatalf("labels by tag: %v", l)
	}
	tags := get("/accounts/"+arb+"/tags", 200).([]any)
	found := false
	for _, tg := range tags {
		found = found || tg.(map[string]any)["id"] == "arb-bot"
	}
	if !found {
		t.Fatalf("user tag missing: %v", tags)
	}
	if res := get("/search?q=%23arb", 200).([]any); len(res) == 0 || res[0].(map[string]any)["kind"] != "tag" {
		t.Fatalf("tag search: %v", res)
	}
	if lb := get("/leaderboards?hours=2160", 200).(map[string]any); !strings.Contains(fmt.Sprint(lb["arbitrageurs"]), "arb-bot") {
		t.Fatal("leaderboard rows lack labels")
	}
	get("/labels/tags", 200)
	del := func(path string) int {
		req, _ := http.NewRequest("DELETE", srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := del("/labels/" + arb + "/arb-bot"); c != 200 {
		t.Fatalf("delete: %d", c)
	}
	if c := del("/labels/" + arb + "/arb-bot"); c != 404 {
		t.Fatalf("delete again: %d", c)
	}
	a.LabelsToken = "s3cret"
	if c := post(`{"address":"`+arb+`","tag":"x"}`, ""); c != 401 {
		t.Fatalf("no token: %d", c)
	}
	if c := post(`{"address":"`+arb+`","tag":"x"}`, "s3cret"); c != 200 {
		t.Fatalf("with token: %d", c)
	}
	req, _ := http.NewRequest("OPTIONS", srv.URL+"/labels", nil)
	pre, err := http.DefaultClient.Do(req)
	if err != nil || pre.StatusCode != 204 || !strings.Contains(pre.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight: %v %v", pre, err)
	}

	// Embedded UI.
	for path, want := range map[string]string{"/ui/": "Stellar Indexer", "/ui/js/app.js": "hashchange", "/ui/css/app.css": "--surface"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), want) {
			t.Fatalf("%s: %d, missing %q", path, resp.StatusCode, want)
		}
	}
	req, _ = http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/" {
		t.Fatalf("browser redirect: %v %v", resp, err)
	}

	// Retention: keep only the last ledger's raw data.
	pruned, err := db.Prune(ctx, "test", store.Retention{Raw: 1, Markets: 1})
	if err != nil {
		t.Fatal(err)
	}
	var left int
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM transactions WHERE ledger < 65312801").Scan(&left)
	if left != 0 || pruned["transactions"] == 0 || pruned["operations"] == 0 {
		t.Fatalf("prune: %v, left %d", pruned, left)
	}
}
