package analytics

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Portfolio valuation. Balances come from indexed ledger state (accounts,
// trustlines, pool shares); Soroban token balances, and every balance of a
// contract address, are net token transfers since indexing began and are
// flagged approximate. Blend positions are net supply/borrow flows.

// PoolShare is a classic liquidity-pool position's underlying reserves.
type PoolShare struct {
	AssetA, AssetB     string
	Fraction           float64 // account shares / total shares
	ReserveA, ReserveB float64
}

// TrustlineHolding is one positive trustline balance (or pool share).
type TrustlineHolding struct {
	Asset  string
	Amount float64
	Pool   *PoolShare // set for "pool:<id>" trustlines
}

// BlendPosition is a net lending position in one Blend pool and asset.
type BlendPosition struct {
	Contract, Asset    string
	Supplied, Borrowed float64
}

// PortfolioInput is everything the index knows about an address's holdings.
type PortfolioInput struct {
	Account     string
	NativeKnown bool
	Native      float64 // XLM, when NativeKnown
	SeqNum      int64
	Trustlines  []TrustlineHolding
	NetFlows    map[string]float64 // asset → net transfers in − out since indexing began
	Blend       []BlendPosition
	SinceLedger int64
}

// Part is one underlying leg of a pool share.
type Part struct {
	Asset  string  `json:"asset"`
	Amount float64 `json:"amount"`
}

// Holding is one valued position.
type Holding struct {
	Asset     string   `json:"asset"`
	Kind      string   `json:"kind"` // native | trustline | token | pool_share | blend_supply | blend_borrow
	Amount    float64  `json:"amount"`
	Contract  string   `json:"contract,omitempty"`
	PriceXLM  *float64 `json:"price_xlm"`
	ValueXLM  *float64 `json:"value_xlm"`
	ValueUSD  *float64 `json:"value_usd"`
	Change24h *float64 `json:"change_24h_pct"`
	Change7d  *float64 `json:"change_7d_pct"`
	Share     float64  `json:"share"`
	Approx    bool     `json:"approx,omitempty"`
	Parts     []Part   `json:"parts,omitempty"`
}

// Point is one hourly value sample.
type Point struct {
	T        time.Time `json:"t"`
	ValueXLM float64   `json:"value_xlm"`
	ValueUSD *float64  `json:"value_usd"`
}

// Portfolio is the valued holdings of an address.
type Portfolio struct {
	Account       string     `json:"account"`
	Known         bool       `json:"known"`
	CreatedLedger *int64     `json:"created_ledger"`
	SinceLedger   int64      `json:"since_ledger"`
	USDAsset      string     `json:"usd_asset"`
	USDPerXLM     *float64   `json:"usd_per_xlm"`
	ValueXLM      float64    `json:"value_xlm"`
	ValueUSD      *float64   `json:"value_usd"`
	AssetsXLM     float64    `json:"assets_xlm"`
	DebtXLM       float64    `json:"debt_xlm"`
	Change24hPct  *float64   `json:"change_24h_pct"`
	Change7dPct   *float64   `json:"change_7d_pct"`
	Unpriced      int        `json:"unpriced"`
	Holdings      []*Holding `json:"holdings"`
	History       []Point    `json:"history"`
}

// PriceSet holds XLM prices now, 24h and 7d before the anchor.
type PriceSet struct {
	Now, Ago24h, Ago7d Prices
}

func ptr[T any](v T) *T { return &v }

// Holdings turns raw positions into unvalued holdings. For G accounts only
// Soroban tokens (C… keys) come from transfer flows; for contract addresses
// every asset does, since contracts have no trustlines.
func Holdings(in PortfolioInput) []*Holding {
	var out []*Holding
	if in.NativeKnown {
		out = append(out, &Holding{Asset: "native", Kind: "native", Amount: in.Native})
	}
	have := map[string]bool{"native": in.NativeKnown}
	for _, t := range in.Trustlines {
		h := &Holding{Asset: t.Asset, Kind: "trustline", Amount: t.Amount}
		if strings.HasPrefix(t.Asset, "pool:") {
			h.Kind, h.Contract = "pool_share", strings.TrimPrefix(t.Asset, "pool:")
			if p := t.Pool; p != nil {
				h.Parts = []Part{{p.AssetA, p.Fraction * p.ReserveA}, {p.AssetB, p.Fraction * p.ReserveB}}
			}
		}
		have[t.Asset] = true
		out = append(out, h)
	}
	contract := strings.HasPrefix(in.Account, "C")
	flowAssets := make([]string, 0, len(in.NetFlows))
	for a := range in.NetFlows {
		flowAssets = append(flowAssets, a)
	}
	sort.Strings(flowAssets)
	for _, a := range flowAssets {
		v := in.NetFlows[a]
		if have[a] || v <= 1e-7 {
			continue
		}
		if contract || strings.HasPrefix(a, "C") {
			out = append(out, &Holding{Asset: a, Kind: "token", Amount: v, Approx: true})
		}
	}
	for _, b := range in.Blend {
		if b.Supplied > 1e-7 {
			out = append(out, &Holding{Asset: b.Asset, Kind: "blend_supply", Amount: b.Supplied, Contract: b.Contract, Approx: true})
		}
		if b.Borrowed > 1e-7 {
			out = append(out, &Holding{Asset: b.Asset, Kind: "blend_borrow", Amount: b.Borrowed, Contract: b.Contract, Approx: true})
		}
	}
	return out
}

// PricedAssets lists the assets whose prices valuation needs (pool shares
// contribute their underlyings), plus extra (e.g. the USD asset).
func PricedAssets(hs []*Holding, extra ...string) []string {
	set := map[string]bool{}
	for _, h := range hs {
		if h.Kind == "pool_share" {
			for _, p := range h.Parts {
				set[p.Asset] = true
			}
		} else {
			set[h.Asset] = true
		}
	}
	for _, e := range extra {
		if e != "" {
			set[e] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func holdingValue(h *Holding, px Prices) (float64, bool) {
	if h.Kind == "pool_share" {
		v, ok := 0.0, len(h.Parts) > 0
		for _, p := range h.Parts {
			pr, has := px[p.Asset]
			ok = ok && has
			v += p.Amount * pr
		}
		return v, ok
	}
	pr, ok := px[h.Asset]
	return h.Amount * pr, ok
}

// Value prices holdings and computes totals and price-only changes (same
// amounts at older prices); History refines the changes when available.
func Value(in PortfolioInput, hs []*Holding, px PriceSet, usdAsset string) *Portfolio {
	p := &Portfolio{Account: in.Account, Known: in.NativeKnown, SinceLedger: in.SinceLedger, USDAsset: usdAsset, Holdings: hs, History: []Point{}}
	if p.Holdings == nil {
		p.Holdings = []*Holding{}
	}
	if in.NativeKnown && in.SeqNum>>32 > 0 {
		p.CreatedLedger = ptr(in.SeqNum >> 32) // starting sequence number = creation ledger << 32
	}
	usd := 0.0
	if u := px.Now[usdAsset]; u > 0 {
		usd = 1 / u
		p.USDPerXLM = ptr(usd)
	}
	var v24, v7 float64
	for _, h := range p.Holdings {
		v, ok := holdingValue(h, px.Now)
		if !ok {
			p.Unpriced++
			continue
		}
		sign := 1.0
		if h.Kind == "blend_borrow" {
			sign = -1
			p.DebtXLM += v
		} else {
			p.AssetsXLM += v
		}
		h.ValueXLM = ptr(sign * v)
		if h.Amount > 0 {
			h.PriceXLM = ptr(v / h.Amount)
		}
		if usd > 0 {
			h.ValueUSD = ptr(sign * v * usd)
		}
		if o, ok := holdingValue(h, px.Ago24h); ok && o > 0 {
			h.Change24h = ptr((v/o - 1) * 100)
			v24 += sign * o
		} else {
			v24 += sign * v
		}
		if o, ok := holdingValue(h, px.Ago7d); ok && o > 0 {
			h.Change7d = ptr((v/o - 1) * 100)
			v7 += sign * o
		} else {
			v7 += sign * v
		}
	}
	p.ValueXLM = p.AssetsXLM - p.DebtXLM
	if usd > 0 {
		p.ValueUSD = ptr(p.ValueXLM * usd)
	}
	for _, h := range p.Holdings {
		if h.ValueXLM != nil && p.AssetsXLM > 0 && *h.ValueXLM > 0 {
			h.Share = *h.ValueXLM / p.AssetsXLM
		}
	}
	sort.SliceStable(p.Holdings, func(i, j int) bool { return valueOf(p.Holdings[i]) > valueOf(p.Holdings[j]) })
	if v24 != 0 {
		p.Change24hPct = ptr((p.ValueXLM/v24 - 1) * 100)
	}
	if v7 != 0 {
		p.Change7dPct = ptr((p.ValueXLM/v7 - 1) * 100)
	}
	return p
}

func valueOf(h *Holding) float64 {
	if h.ValueXLM == nil {
		return math.Inf(-1)
	}
	return *h.ValueXLM
}

// HourlySeries maps asset → hour (UTC, truncated) → value.
type HourlySeries map[string]map[time.Time]float64

// History reconstructs hourly value between start and now: the amount at the
// end of each hour is the current amount minus the net flows after it,
// valued at that hour's price (nearest earlier, else earliest later). Only
// assets priced now take part; pool shares and Blend positions are held
// constant. It also replaces the change figures with flow-aware ones.
func History(p *Portfolio, flows, hourly HourlySeries, start, now time.Time, usdAsset string) {
	start, end := start.UTC().Truncate(time.Hour), now.UTC().Truncate(time.Hour)
	if !end.After(start) {
		return
	}
	cur := map[string]float64{}
	static := 0.0
	for _, h := range p.Holdings {
		switch h.Kind {
		case "native", "trustline", "token":
			cur[h.Asset] += h.Amount
		default:
			if h.ValueXLM != nil {
				static += *h.ValueXLM
			}
		}
	}
	var hours []time.Time
	for t := end; !t.Before(start); t = t.Add(-time.Hour) {
		hours = append(hours, t)
	}
	priceAt := func(a string, i int) (float64, bool) {
		if a == "native" {
			return 1, true
		}
		m, ok := hourly[a]
		if !ok {
			return 0, false
		}
		for j := i; j < len(hours); j++ {
			if v, ok := m[hours[j]]; ok {
				return v, true
			}
		}
		for j := i - 1; j >= 0; j-- {
			if v, ok := m[hours[j]]; ok {
				return v, true
			}
		}
		return 0, false
	}
	amt := map[string]float64{}
	for a, v := range cur {
		amt[a] = v
	}
	pts := make([]Point, len(hours))
	for i, t := range hours {
		if i > 0 { // undo the flows of the hour after t
			for a, f := range flows {
				amt[a] -= f[hours[i-1]]
			}
		}
		v := static
		for a, q := range amt {
			if q == 0 {
				continue
			}
			if pr, ok := priceAt(a, i); ok {
				v += math.Max(q, 0) * pr
			}
		}
		pt := Point{T: t.Add(time.Hour), ValueXLM: v} // amounts hold at the end of hour t
		if i == 0 {
			pt.T = now
		}
		if usdAsset != "" {
			if u, ok := priceAt(usdAsset, i); ok && u > 0 {
				pt.ValueUSD = ptr(v / u)
			}
		}
		pts[len(hours)-1-i] = pt
	}
	p.History = pts
	if len(pts) > 24 && pts[len(pts)-25].ValueXLM > 0 {
		p.Change24hPct = ptr((pts[len(pts)-1].ValueXLM/pts[len(pts)-25].ValueXLM - 1) * 100)
	}
	if len(pts) >= 24*7 && pts[0].ValueXLM > 0 {
		p.Change7dPct = ptr((pts[len(pts)-1].ValueXLM/pts[0].ValueXLM - 1) * 100)
	}
}
