package analytics

import (
	"math"
	"sort"
	"time"
)

// Fill is one trade from the taker's point of view, amounts in whole units.
type Fill struct {
	Tx        string
	At        time.Time
	Venue     string
	Pool      string
	Sold      string
	SoldAmt   float64
	Bought    string
	BoughtAmt float64
}

// Prices maps asset key → XLM per unit (native = 1). Missing = unpriced.
type Prices map[string]float64

// FillXLM values a fill in XLM: by its native leg when it has one, else at
// the given prices (sold leg first).
func FillXLM(f Fill, px Prices) (float64, bool) {
	switch {
	case f.Sold == "native":
		return f.SoldAmt, true
	case f.Bought == "native":
		return f.BoughtAmt, true
	}
	if p, ok := px[f.Sold]; ok {
		return f.SoldAmt * p, true
	}
	if p, ok := px[f.Bought]; ok {
		return f.BoughtAmt * p, true
	}
	return 0, false
}

// PnL is the result of average-cost accounting in XLM terms.
type PnL struct {
	RealizedXLM   float64            `json:"realized_xlm"`
	UnrealizedXLM float64            `json:"unrealized_xlm"`
	PerAsset      map[string]float64 `json:"per_asset_realized_xlm"`
	Open          map[string]float64 `json:"open_qty"`
	Matched       int                `json:"matched_sells"`
}

// ComputePnL applies average-cost accounting to fills in chronological
// order. Every fill sells the sold asset and buys the bought one at the
// fill's XLM value; sales without a cost basis inside the window (positions
// opened earlier) realise nothing.
func ComputePnL(fills []Fill, px Prices) PnL {
	type pos struct{ qty, cost float64 }
	book := map[string]*pos{}
	res := PnL{PerAsset: map[string]float64{}, Open: map[string]float64{}}
	get := func(a string) *pos {
		if book[a] == nil {
			book[a] = &pos{}
		}
		return book[a]
	}
	for _, f := range fills {
		v, ok := FillXLM(f, px)
		if !ok {
			continue
		}
		if f.Sold != "native" && f.SoldAmt > 0 {
			b := get(f.Sold)
			if b.qty > 0 {
				q := math.Min(f.SoldAmt, b.qty)
				avg := b.cost / b.qty
				gain := v*(q/f.SoldAmt) - avg*q
				res.RealizedXLM += gain
				res.PerAsset[f.Sold] += gain
				b.qty -= q
				b.cost -= avg * q
				res.Matched++
			}
		}
		if f.Bought != "native" && f.BoughtAmt > 0 {
			b := get(f.Bought)
			b.qty += f.BoughtAmt
			b.cost += v
		}
	}
	for a, b := range book {
		if b.qty <= 1e-9 {
			continue
		}
		res.Open[a] = b.qty
		if p, ok := px[a]; ok {
			res.UnrealizedXLM += b.qty*p - b.cost
		}
	}
	return res
}

// Cycle is a same-transaction round trip: two or more fills whose first sold
// asset is the last bought one. Profit is the net of that asset.
type Cycle struct {
	Tx        string  `json:"tx_hash"`
	At        string  `json:"closed_at"`
	Asset     string  `json:"asset"`
	Hops      int     `json:"hops"`
	Profit    float64 `json:"profit"`
	ProfitXLM float64 `json:"profit_xlm"`
}

// FindCycles detects round trips in fills grouped by transaction (fills of
// one tx must be adjacent, as they are when ordered by position).
func FindCycles(fills []Fill, px Prices) []Cycle {
	var out []Cycle
	for i := 0; i < len(fills); {
		j := i
		for j < len(fills) && fills[j].Tx == fills[i].Tx {
			j++
		}
		group := fills[i:j]
		if len(group) >= 2 && group[0].Sold == group[len(group)-1].Bought {
			a := group[0].Sold
			net := 0.0
			for _, f := range group {
				if f.Bought == a {
					net += f.BoughtAmt
				}
				if f.Sold == a {
					net -= f.SoldAmt
				}
			}
			c := Cycle{Tx: group[0].Tx, At: group[0].At.UTC().Format(time.RFC3339), Asset: a, Hops: len(group), Profit: net}
			if p, ok := px[a]; ok {
				c.ProfitXLM = net * p
			}
			out = append(out, c)
		}
		i = j
	}
	return out
}

// VenueVolume and PairVolume are breakdowns of a taker's fills.
type VenueVolume struct {
	Venue  string  `json:"venue"`
	Trades int     `json:"trades"`
	XLM    float64 `json:"volume_xlm"`
}

type PairVolume struct {
	Base    string  `json:"base"`
	Counter string  `json:"counter"`
	Trades  int     `json:"trades"`
	XLM     float64 `json:"volume_xlm"`
}

// Trading summarises a taker's fills over a window.
type Trading struct {
	Trades         int           `json:"trades"`
	Truncated      bool          `json:"truncated"`
	VolumeXLM      float64       `json:"volume_xlm"`
	Venues         []VenueVolume `json:"venues"`
	Pairs          []PairVolume  `json:"pairs"`
	PnL            PnL           `json:"pnl"`
	Cycles         int           `json:"cycles"`
	CycleProfitXLM float64       `json:"cycle_profit_xlm"`
	TopCycles      []Cycle       `json:"top_cycles"`
}

// SummarizeTrading computes volume, venue/pair breakdowns, PnL and round
// trips for fills in chronological order.
func SummarizeTrading(fills []Fill, px Prices, truncated bool) Trading {
	t := Trading{Trades: len(fills), Truncated: truncated, Venues: []VenueVolume{}, Pairs: []PairVolume{}, TopCycles: []Cycle{}}
	venues := map[string]*VenueVolume{}
	pairs := map[[2]string]*PairVolume{}
	for _, f := range fills {
		v, _ := FillXLM(f, px)
		t.VolumeXLM += v
		vv := venues[f.Venue]
		if vv == nil {
			vv = &VenueVolume{Venue: f.Venue}
			venues[f.Venue] = vv
		}
		vv.Trades++
		vv.XLM += v
		k := [2]string{f.Sold, f.Bought}
		if k[0] > k[1] {
			k[0], k[1] = k[1], k[0]
		}
		pp := pairs[k]
		if pp == nil {
			pp = &PairVolume{Base: k[0], Counter: k[1]}
			pairs[k] = pp
		}
		pp.Trades++
		pp.XLM += v
	}
	for _, v := range venues {
		t.Venues = append(t.Venues, *v)
	}
	sort.Slice(t.Venues, func(i, j int) bool { return t.Venues[i].XLM > t.Venues[j].XLM })
	for _, p := range pairs {
		t.Pairs = append(t.Pairs, *p)
	}
	sort.Slice(t.Pairs, func(i, j int) bool { return t.Pairs[i].XLM > t.Pairs[j].XLM })
	if len(t.Pairs) > 10 {
		t.Pairs = t.Pairs[:10]
	}
	t.PnL = ComputePnL(fills, px)
	cycles := FindCycles(fills, px)
	t.Cycles = len(cycles)
	for _, c := range cycles {
		t.CycleProfitXLM += c.ProfitXLM
	}
	sort.Slice(cycles, func(i, j int) bool { return cycles[i].ProfitXLM > cycles[j].ProfitXLM })
	if len(cycles) > 10 {
		cycles = cycles[:10]
	}
	t.TopCycles = append(t.TopCycles, cycles...)
	return t
}
