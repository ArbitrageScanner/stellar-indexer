package analytics

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestComputePnL(t *testing.T) {
	px := Prices{"A": 2}
	fills := []Fill{
		{Tx: "1", Sold: "native", SoldAmt: 100, Bought: "A", BoughtAmt: 100}, // cost 1.0
		{Tx: "2", Sold: "native", SoldAmt: 200, Bought: "A", BoughtAmt: 100}, // cost 2.0 → avg 1.5
		{Tx: "3", Sold: "A", SoldAmt: 100, Bought: "native", BoughtAmt: 250}, // +250 − 150
		{Tx: "4", Sold: "B", SoldAmt: 5, Bought: "native", BoughtAmt: 9},     // no basis: ignored
	}
	r := ComputePnL(fills, px)
	if !near(r.RealizedXLM, 100) || r.Matched != 1 {
		t.Fatalf("realized %v matched %d", r.RealizedXLM, r.Matched)
	}
	if !near(r.Open["A"], 100) || !near(r.UnrealizedXLM, 100*2-150) {
		t.Fatalf("open %v unrealized %v", r.Open, r.UnrealizedXLM)
	}
}

func TestFindCycles(t *testing.T) {
	at := time.Unix(0, 0)
	fills := []Fill{
		{Tx: "a", At: at, Sold: "native", SoldAmt: 10, Bought: "X", BoughtAmt: 20},
		{Tx: "a", At: at, Sold: "X", SoldAmt: 20, Bought: "Y", BoughtAmt: 5},
		{Tx: "a", At: at, Sold: "Y", SoldAmt: 5, Bought: "native", BoughtAmt: 12},
		{Tx: "b", At: at, Sold: "native", SoldAmt: 10, Bought: "X", BoughtAmt: 20}, // single fill
		{Tx: "c", At: at, Sold: "X", SoldAmt: 1, Bought: "Y", BoughtAmt: 1},        // not a round trip
		{Tx: "c", At: at, Sold: "Y", SoldAmt: 1, Bought: "native", BoughtAmt: 1},
	}
	c := FindCycles(fills, Prices{"native": 1})
	if len(c) != 1 || c[0].Tx != "a" || c[0].Hops != 3 || !near(c[0].Profit, 2) || !near(c[0].ProfitXLM, 2) {
		t.Fatalf("cycles %+v", c)
	}
	s := SummarizeTrading(fills, Prices{"native": 1, "X": 0.5}, false)
	if s.Cycles != 1 || s.Trades != 6 || len(s.Venues) != 1 || s.VolumeXLM <= 0 {
		t.Fatalf("summary %+v", s)
	}
}

func TestPortfolioValueAndHistory(t *testing.T) {
	in := PortfolioInput{
		Account: "GTEST", NativeKnown: true, Native: 100, SeqNum: int64(1000) << 32,
		Trustlines: []TrustlineHolding{
			{Asset: "USD:G", Amount: 10},
			{Asset: "pool:ab", Amount: 5, Pool: &PoolShare{AssetA: "native", AssetB: "USD:G", Fraction: 0.1, ReserveA: 1000, ReserveB: 100}},
		},
		NetFlows: map[string]float64{"CTOKEN": 3, "USD:G": 99, "CNEG": -1},
		Blend:    []BlendPosition{{Contract: "CPOOL", Asset: "USD:G", Supplied: 4, Borrowed: 1}},
	}
	hs := Holdings(in)
	kinds := map[string]int{}
	for _, h := range hs {
		kinds[h.Kind]++
	}
	// native, trustline, pool_share, token (CTOKEN only: USD:G has a trustline, CNEG is negative), supply, borrow
	if kinds["native"] != 1 || kinds["trustline"] != 1 || kinds["pool_share"] != 1 || kinds["token"] != 1 || kinds["blend_supply"] != 1 || kinds["blend_borrow"] != 1 {
		t.Fatalf("kinds %v", kinds)
	}
	now := Prices{"native": 1, "USD:G": 10} // 1 USD:G = 10 XLM → $0.1 per XLM
	p := Value(in, hs, PriceSet{Now: now, Ago24h: Prices{"native": 1, "USD:G": 5}, Ago7d: Prices{}}, "USD:G")
	// assets: 100 + 10*10 + pool(100*1 + 10*10) + supply 4*10 = 440; debt 1*10
	if !near(p.AssetsXLM, 440) || !near(p.DebtXLM, 10) || !near(p.ValueXLM, 430) || p.Unpriced != 1 {
		t.Fatalf("value %+v", p)
	}
	if p.CreatedLedger == nil || *p.CreatedLedger != 1000 || p.USDPerXLM == nil || !near(*p.USDPerXLM, 0.1) {
		t.Fatalf("created/usd %+v", p)
	}
	if p.Holdings[0].Asset != "pool:ab" || p.Holdings[len(p.Holdings)-1].ValueXLM != nil {
		t.Fatalf("holdings not sorted by value (unpriced last): first %s", p.Holdings[0].Asset)
	}
	// History: 100 XLM now, +50 XLM arrived in the current hour → 50 an hour earlier.
	nowT := time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC)
	h := &Portfolio{Holdings: []*Holding{{Asset: "native", Kind: "native", Amount: 100}}}
	History(h, HourlySeries{"native": {nowT.Truncate(time.Hour): 50}}, HourlySeries{}, nowT.Add(-2*time.Hour), nowT, "")
	if len(h.History) != 3 || !near(h.History[2].ValueXLM, 100) || !near(h.History[1].ValueXLM, 50) || !near(h.History[0].ValueXLM, 50) {
		t.Fatalf("history %+v", h.History)
	}
}

func TestAutoTags(t *testing.T) {
	anchor := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	x := 2e6
	last := anchor.Add(-8 * 24 * time.Hour)
	got := map[string]bool{}
	for _, tg := range AutoTags(Signals{
		XLM: &x, Cycles7d: 1, FlashLoans: 2, Offers: 5, TwoSidedOffer: 1, Txs24h: 600, Failed24h: 400,
		IssuedCount: 1, Issued: []string{"FOO"}, ProtoContract: "aquarius", ProtoActions: []string{"router_swap"},
		CreatedLedger: 100, Tip: 100 + 1000, LastActive: &last,
	}, anchor) {
		got[tg.ID] = true
		if tg.Reason == "" || tg.Kind != "auto" {
			t.Fatalf("tag without reason: %+v", tg)
		}
	}
	for _, want := range []string{"whale", "arbitrage_bot", "flash_loans", "market_maker", "hft", "failing_bot", "issuer", "protocol_aquarius", "new_account", "dormant"} {
		if !got[want] {
			t.Errorf("missing tag %s (got %v)", want, got)
		}
	}
	if tags := AutoTags(Signals{}, anchor); len(tags) != 0 {
		t.Fatalf("empty signals produced %v", tags)
	}
}

func TestLabels(t *testing.T) {
	addr := "GBTB3MJPQKPIWLYX34IKBE7ENT46ZDVQGPAVHGBDOMNA4HP6Q2KKJFLH"
	l, err := NormalizeLabel(Label{Address: " " + addr, Tag: "#Arb-Bot", Note: "a\x00b\nc"})
	if err != nil || l.Tag != "arb-bot" || l.Note != "ab\nc" || l.Address != addr {
		t.Fatalf("%+v %v", l, err)
	}
	for _, bad := range []Label{{Address: "nope", Tag: "x"}, {Address: addr, Tag: "has space"}, {Address: addr, Tag: "x", Note: strings.Repeat("a", 300)}} {
		if _, err := NormalizeLabel(bad); !errors.Is(err, ErrInvalidLabel) {
			t.Errorf("%+v accepted", bad)
		}
	}
	rows := []map[string]any{{"address": addr}, {"address": "other"}}
	AttachLabels(rows, "address", map[string][]string{addr: {"a"}})
	if rows[0]["labels"] == nil || rows[1]["labels"] != nil {
		t.Fatal("attach labels")
	}
}

func TestMergeAlerts(t *testing.T) {
	t0 := time.Unix(100, 0)
	all, counts := MergeAlerts(map[string][]map[string]any{
		"flash_loan": {{"closed_at": t0}},
		"arbitrage":  {{"closed_at": t0.Add(time.Second)}, {"closed_at": t0.Add(-time.Second)}},
	}, 2)
	if len(all) != 2 || counts["arbitrage"] != 2 || all[0]["kind"] != "arbitrage" || all[1]["kind"] != "flash_loan" {
		t.Fatalf("%v %v", all, counts)
	}
}

func TestMarket(t *testing.T) {
	b, c, inv, err := CanonicalPair("xlm", "USDC:G")
	if err != nil || b != "USDC:G" || c != "native" || !inv {
		t.Fatalf("%s %s %v %v", b, c, inv, err)
	}
	if _, _, _, err := CanonicalPair("a", "a"); err == nil {
		t.Fatal("degenerate pair accepted")
	}
	last, open, hi, lo := 2.0, 4.0, 5.0, 1.0
	tk := Ticker{Base: "A", Counter: "B", Last: &last, Open24h: &open, High24h: &hi, Low24h: &lo}.Orient(true)
	if tk.Base != "B" || *tk.Last != 0.5 || *tk.High24h != 1 || *tk.Low24h != 0.2 || !near(*tk.Change24hPct, 100) {
		t.Fatalf("%+v", tk)
	}
	cd := Candle{Open: 2, High: 4, Low: 1, Close: 2, BaseVolume: 1, CounterVolume: 3}.Invert()
	if cd.High != 1 || cd.Low != 0.25 || cd.BaseVolume != 3 {
		t.Fatalf("%+v", cd)
	}
}
