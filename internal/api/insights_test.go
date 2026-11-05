package api

import (
	"math"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestComputePnL(t *testing.T) {
	px := map[string]float64{"A": 2}
	trades := []tradeRow{
		{Tx: "1", Sold: "native", SoldAmt: 100, Bought: "A", BoughtAmt: 100}, // cost 1.0
		{Tx: "2", Sold: "native", SoldAmt: 200, Bought: "A", BoughtAmt: 100}, // cost 2.0 → avg 1.5
		{Tx: "3", Sold: "A", SoldAmt: 100, Bought: "native", BoughtAmt: 250}, // +250 − 150
		{Tx: "4", Sold: "B", SoldAmt: 5, Bought: "native", BoughtAmt: 9},     // no basis: ignored
	}
	r := computePnL(trades, px)
	if !near(r.RealizedXLM, 100) || r.Matched != 1 {
		t.Fatalf("realized %v matched %d", r.RealizedXLM, r.Matched)
	}
	if !near(r.Open["A"], 100) || !near(r.UnrealizedXLM, 100*2-150) {
		t.Fatalf("open %v unrealized %v", r.Open, r.UnrealizedXLM)
	}
}

func TestFindCycles(t *testing.T) {
	at := time.Unix(0, 0)
	trades := []tradeRow{
		{Tx: "a", At: at, Sold: "native", SoldAmt: 10, Bought: "X", BoughtAmt: 20},
		{Tx: "a", At: at, Sold: "X", SoldAmt: 20, Bought: "Y", BoughtAmt: 5},
		{Tx: "a", At: at, Sold: "Y", SoldAmt: 5, Bought: "native", BoughtAmt: 12},
		{Tx: "b", At: at, Sold: "native", SoldAmt: 10, Bought: "X", BoughtAmt: 20}, // single fill
		{Tx: "c", At: at, Sold: "X", SoldAmt: 1, Bought: "Y", BoughtAmt: 1},        // not a round trip
		{Tx: "c", At: at, Sold: "Y", SoldAmt: 1, Bought: "native", BoughtAmt: 1},
	}
	c := findCycles(trades, map[string]float64{"native": 1})
	if len(c) != 1 || c[0].Tx != "a" || c[0].Hops != 3 || !near(c[0].Profit, 2) || !near(c[0].ProfitXLM, 2) {
		t.Fatalf("cycles %+v", c)
	}
}
