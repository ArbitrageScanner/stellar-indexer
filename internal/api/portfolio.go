package api

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Address portfolio and indicators. Balances come from the indexed ledger
// state (accounts, trustlines, pool shares); Soroban token balances, and every
// balance of a contract address, are derived from token transfers seen since
// indexing began and are flagged approximate. Blend positions are net
// supply/borrow flows. History is reconstructed backwards from the current
// balances and hourly transfer flows, valued at hourly VWAPs.

type holding struct {
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
	Parts     []part   `json:"parts,omitempty"`
}

type part struct {
	Asset  string  `json:"asset"`
	Amount float64 `json:"amount"`
}

type point struct {
	T        time.Time `json:"t"`
	ValueXLM float64   `json:"value_xlm"`
	ValueUSD *float64  `json:"value_usd"`
}

type portfolio struct {
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
	Holdings      []*holding `json:"holdings"`
	History       []point    `json:"history"`

	prices map[string]float64
}

func fp(v float64) *float64 { return &v }

func (s *Server) portfolioH(w http.ResponseWriter, r *http.Request) {
	p, err := s.buildPortfolio(r.Context(), r.PathValue("id"), r.URL.Query().Get("history") != "false")
	s.reply(w, p, err)
}

func (s *Server) buildPortfolio(ctx context.Context, id string, withHistory bool) (*portfolio, error) {
	p := &portfolio{Account: id, USDAsset: s.USDAsset, Holdings: []*holding{}, History: []point{}}
	isContract := strings.HasPrefix(id, "C")
	var bal, seq *int64
	_ = s.db.QueryRow(ctx, `SELECT balance, seq_num FROM accounts WHERE account_id=$1 AND NOT deleted`, id).Scan(&bal, &seq)
	_ = s.db.QueryRow(ctx, `SELECT coalesce(min(sequence), 0) FROM ledgers`).Scan(&p.SinceLedger)
	if bal != nil {
		p.Known = true
		p.Holdings = append(p.Holdings, &holding{Asset: "native", Kind: "native", Amount: float64(*bal) / 1e7})
		if seq != nil && *seq>>32 > 0 {
			p.CreatedLedger = fp64(*seq >> 32)
		}
	}

	// Classic trustlines and pool shares.
	rows, err := s.db.Query(ctx, `SELECT t.asset, t.balance / 1e7::float8, lp.asset_a, lp.asset_b,
		CASE WHEN lp.total_shares > 0 THEN t.balance::float8 / lp.total_shares END, lp.reserve_a / 1e7::float8, lp.reserve_b / 1e7::float8
		FROM trustlines t LEFT JOIN liquidity_pools lp ON t.asset = 'pool:' || lp.pool_id
		WHERE t.account_id = $1 AND t.balance > 0`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var asset string
		var amount float64
		var a, b *string
		var frac, ra, rb *float64
		if err := rows.Scan(&asset, &amount, &a, &b, &frac, &ra, &rb); err != nil {
			rows.Close()
			return nil, err
		}
		h := &holding{Asset: asset, Kind: "trustline", Amount: amount}
		if strings.HasPrefix(asset, "pool:") {
			h.Kind = "pool_share"
			h.Contract = strings.TrimPrefix(asset, "pool:")
			if a != nil && frac != nil {
				h.Parts = []part{{*a, *frac * *ra}, {*b, *frac * *rb}}
			}
		}
		p.Holdings = append(p.Holdings, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Net transfer flows per asset: balances of Soroban tokens (and of every
	// asset for contract addresses), and the basis for the history.
	flows := map[string]float64{}
	rows, err = s.db.Query(ctx, `SELECT asset, (sum(CASE WHEN to_addr = $1 THEN amount ELSE 0 END) - sum(CASE WHEN from_addr = $1 THEN amount ELSE 0 END)) / 1e7::float8
		FROM transfers WHERE from_addr = $1 OR to_addr = $1 GROUP BY asset`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a string
		var v float64
		if err := rows.Scan(&a, &v); err != nil {
			rows.Close()
			return nil, err
		}
		flows[a] = v
	}
	rows.Close()
	have := map[string]bool{}
	for _, h := range p.Holdings {
		have[h.Asset] = true
	}
	for a, v := range flows {
		if have[a] || v <= 1e-7 {
			continue
		}
		if isContract || strings.HasPrefix(a, "C") {
			p.Holdings = append(p.Holdings, &holding{Asset: a, Kind: "token", Amount: v, Approx: true})
		}
	}

	// Blend lending positions (net flows since indexing began).
	rows, err = s.db.Query(ctx, `SELECT contract_id, asset,
		(sum(amount0) FILTER (WHERE action IN ('supply', 'supply_collateral')) - coalesce(sum(amount0) FILTER (WHERE action IN ('withdraw', 'withdraw_collateral')), 0)) / 1e7::float8,
		(sum(amount0) FILTER (WHERE action = 'borrow') - coalesce(sum(amount0) FILTER (WHERE action = 'repay'), 0)) / 1e7::float8
		FROM protocol_events WHERE protocol = 'blend' AND user_addr = $1 AND asset IS NOT NULL GROUP BY 1, 2`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c, a string
		var sup, bor *float64
		if err := rows.Scan(&c, &a, &sup, &bor); err != nil {
			rows.Close()
			return nil, err
		}
		if sup != nil && *sup > 1e-7 {
			p.Holdings = append(p.Holdings, &holding{Asset: a, Kind: "blend_supply", Amount: *sup, Contract: c, Approx: true})
		}
		if bor != nil && *bor > 1e-7 {
			p.Holdings = append(p.Holdings, &holding{Asset: a, Kind: "blend_borrow", Amount: *bor, Contract: c, Approx: true})
		}
	}
	rows.Close()

	// Valuation now, 24h and 7d ago.
	set := map[string]bool{}
	for _, h := range p.Holdings {
		if h.Kind == "pool_share" {
			for _, pt := range h.Parts {
				set[pt.Asset] = true
			}
		} else {
			set[h.Asset] = true
		}
	}
	if s.USDAsset != "" {
		set[s.USDAsset] = true
	}
	assets := keys(set)
	now := time.Now().UTC()
	px := make([]map[string]float64, 3)
	for i, at := range []time.Time{now, now.Add(-24 * time.Hour), now.Add(-7 * 24 * time.Hour)} {
		if px[i], err = s.xlmPrices(ctx, assets, at); err != nil {
			return nil, err
		}
	}
	p.prices = px[0]
	usd := 0.0
	if u := px[0][s.USDAsset]; u > 0 {
		usd = 1 / u
		p.USDPerXLM = fp(usd)
	}
	valueAt := func(h *holding, m map[string]float64) (float64, bool) {
		if h.Kind == "pool_share" {
			v, ok := 0.0, len(h.Parts) > 0
			for _, pt := range h.Parts {
				pr, has := m[pt.Asset]
				ok = ok && has
				v += pt.Amount * pr
			}
			return v, ok
		}
		pr, ok := m[h.Asset]
		return h.Amount * pr, ok
	}
	var v24, v7 float64
	for _, h := range p.Holdings {
		v, ok := valueAt(h, px[0])
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
		h.ValueXLM = fp(sign * v)
		if h.Amount > 0 {
			h.PriceXLM = fp(v / h.Amount)
		}
		if usd > 0 {
			h.ValueUSD = fp(sign * v * usd)
		}
		if o, ok := valueAt(h, px[1]); ok && o > 0 {
			h.Change24h = fp((v/o - 1) * 100)
			v24 += sign * o
		} else {
			v24 += sign * v
		}
		if o, ok := valueAt(h, px[2]); ok && o > 0 {
			h.Change7d = fp((v/o - 1) * 100)
			v7 += sign * o
		} else {
			v7 += sign * v
		}
	}
	p.ValueXLM = p.AssetsXLM - p.DebtXLM
	if usd > 0 {
		p.ValueUSD = fp(p.ValueXLM * usd)
	}
	for _, h := range p.Holdings {
		if h.ValueXLM != nil && p.AssetsXLM > 0 && *h.ValueXLM > 0 {
			h.Share = *h.ValueXLM / p.AssetsXLM
		}
	}
	sort.SliceStable(p.Holdings, func(i, j int) bool { return val(p.Holdings[i]) > val(p.Holdings[j]) })
	// Price-only change (same amounts, older prices) as a fallback.
	if v24 != 0 {
		p.Change24hPct = fp((p.ValueXLM/v24 - 1) * 100)
	}
	if v7 != 0 {
		p.Change7dPct = fp((p.ValueXLM/v7 - 1) * 100)
	}
	if withHistory {
		if err := s.portfolioHistory(ctx, p, assets, now); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func fp64(v int64) *int64 { return &v }

func val(h *holding) float64 {
	if h.ValueXLM == nil {
		return math.Inf(-1)
	}
	return *h.ValueXLM
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// portfolioHistory rebuilds hourly value for up to 7 days (bounded by the
// indexed range): amount(h) = current amount − net flows after h.
func (s *Server) portfolioHistory(ctx context.Context, p *portfolio, assets []string, now time.Time) error {
	var first time.Time
	if err := s.db.QueryRow(ctx, `SELECT coalesce(min(closed_at), now()) FROM ledgers`).Scan(&first); err != nil {
		return err
	}
	start := now.Add(-7 * 24 * time.Hour)
	if first.After(start) {
		start = first
	}
	start = start.Truncate(time.Hour)
	end := now.Truncate(time.Hour)
	if !end.After(start) {
		return nil
	}
	flow := map[string]map[time.Time]float64{}
	rows, err := s.db.Query(ctx, `SELECT asset, date_trunc('hour', closed_at),
		(sum(CASE WHEN to_addr = $1 THEN amount ELSE 0 END) - sum(CASE WHEN from_addr = $1 THEN amount ELSE 0 END)) / 1e7::float8
		FROM transfers WHERE (from_addr = $1 OR to_addr = $1) AND closed_at >= $2 GROUP BY 1, 2`, p.Account, start)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a string
		var h time.Time
		var v float64
		if err := rows.Scan(&a, &h, &v); err != nil {
			rows.Close()
			return err
		}
		if flow[a] == nil {
			flow[a] = map[time.Time]float64{}
		}
		flow[a][h.UTC()] += v
	}
	rows.Close()
	// Only assets that are priced now (liquid) contribute to the history.
	var liquid []string
	for _, a := range assets {
		if _, ok := p.prices[a]; ok {
			liquid = append(liquid, a)
		}
	}
	hp, err := s.hourlyPrices(ctx, liquid, start)
	if err != nil {
		return err
	}
	cur := map[string]float64{}
	static := 0.0 // pool shares and Blend positions are held constant
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
	// Walk backwards from the current hour.
	var hours []time.Time
	for t := end; !t.Before(start); t = t.Add(-time.Hour) {
		hours = append(hours, t)
	}
	priceAt := func(a string, i int) (float64, bool) {
		if a == "native" {
			return 1, true
		}
		m, ok := hp[a]
		if !ok {
			return 0, false
		}
		// nearest known price at or before hours[i], else the earliest after it
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
	pts := make([]point, len(hours))
	for i, t := range hours {
		if i > 0 { // undo the flows of the hour after t
			for a, f := range flow {
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
		// amounts at index i hold at the end of hour t (the newest point is now)
		pt := point{T: t.Add(time.Hour), ValueXLM: v}
		if i == 0 {
			pt.T = now
		}
		if u, ok := priceAt(s.USDAsset, i); ok && u > 0 && s.USDAsset != "" {
			pt.ValueUSD = fp(v / u)
		}
		pts[len(hours)-1-i] = pt
	}
	p.History = pts
	if len(pts) > 24 && pts[len(pts)-25].ValueXLM > 0 {
		p.Change24hPct = fp((pts[len(pts)-1].ValueXLM/pts[len(pts)-25].ValueXLM - 1) * 100)
	}
	if pts[0].ValueXLM > 0 && len(pts) >= 24*7 {
		p.Change7dPct = fp((pts[len(pts)-1].ValueXLM/pts[0].ValueXLM - 1) * 100)
	}
	return nil
}

// ---- indicators ---------------------------------------------------------------

type tradeRow struct {
	Tx        string
	At        time.Time
	Venue     string
	Pool      string
	Sold      string
	SoldAmt   float64
	Bought    string
	BoughtAmt float64
}

// tradeXLM values one fill in XLM: by its native leg when it has one,
// otherwise at current prices.
func tradeXLM(t tradeRow, px map[string]float64) (float64, bool) {
	switch {
	case t.Sold == "native":
		return t.SoldAmt, true
	case t.Bought == "native":
		return t.BoughtAmt, true
	}
	if p, ok := px[t.Sold]; ok {
		return t.SoldAmt * p, true
	}
	if p, ok := px[t.Bought]; ok {
		return t.BoughtAmt * p, true
	}
	return 0, false
}

type pnlResult struct {
	RealizedXLM   float64            `json:"realized_xlm"`
	UnrealizedXLM float64            `json:"unrealized_xlm"`
	PerAsset      map[string]float64 `json:"per_asset_realized_xlm"`
	Open          map[string]float64 `json:"open_qty"`
	Matched       int                `json:"matched_sells"`
}

// computePnL applies average-cost accounting in XLM terms to trades in
// chronological order. Every fill is a sale of the sold asset and a purchase
// of the bought one at the fill's XLM value; sales without a recorded cost
// basis (positions opened before the window) realise nothing.
func computePnL(trades []tradeRow, px map[string]float64) pnlResult {
	type pos struct{ qty, cost float64 }
	book := map[string]*pos{}
	res := pnlResult{PerAsset: map[string]float64{}, Open: map[string]float64{}}
	get := func(a string) *pos {
		if book[a] == nil {
			book[a] = &pos{}
		}
		return book[a]
	}
	for _, t := range trades {
		v, ok := tradeXLM(t, px)
		if !ok {
			continue
		}
		if t.Sold != "native" && t.SoldAmt > 0 {
			b := get(t.Sold)
			if b.qty > 0 {
				q := math.Min(t.SoldAmt, b.qty)
				avg := b.cost / b.qty
				gain := v*(q/t.SoldAmt) - avg*q
				res.RealizedXLM += gain
				res.PerAsset[t.Sold] += gain
				b.qty -= q
				b.cost -= avg * q
				res.Matched++
			}
		}
		if t.Bought != "native" && t.BoughtAmt > 0 {
			b := get(t.Bought)
			b.qty += t.BoughtAmt
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

type cycle struct {
	Tx        string  `json:"tx_hash"`
	At        string  `json:"closed_at"`
	Asset     string  `json:"asset"`
	Hops      int     `json:"hops"`
	Profit    float64 `json:"profit"`
	ProfitXLM float64 `json:"profit_xlm"`
}

// findCycles detects same-transaction round trips: two or more fills whose
// first sold asset is the last bought one. Profit is the net of that asset.
func findCycles(trades []tradeRow, px map[string]float64) []cycle {
	var out []cycle
	for i := 0; i < len(trades); {
		j := i
		for j < len(trades) && trades[j].Tx == trades[i].Tx {
			j++
		}
		group := trades[i:j]
		if len(group) >= 2 && group[0].Sold == group[len(group)-1].Bought {
			a := group[0].Sold
			net := 0.0
			for _, t := range group {
				if t.Bought == a {
					net += t.BoughtAmt
				}
				if t.Sold == a {
					net -= t.SoldAmt
				}
			}
			c := cycle{Tx: group[0].Tx, At: group[0].At.Format(time.RFC3339), Asset: a, Hops: len(group), Profit: net}
			if p, ok := px[a]; ok {
				c.ProfitXLM = net * p
			}
			out = append(out, c)
		}
		i = j
	}
	return out
}

func (s *Server) accountStats(w http.ResponseWriter, r *http.Request) {
	ctx, id, h := r.Context(), r.PathValue("id"), hours(r, 24*7)
	since := time.Now().Add(-time.Duration(h) * time.Hour)
	out := map[string]any{"account": id, "hours": h}

	rows, err := s.db.Query(ctx, `SELECT tx_hash, closed_at, venue, pool, sold_asset, sold_amount / 1e7::float8, bought_asset, bought_amount / 1e7::float8
		FROM trades WHERE taker = $1 AND closed_at > $2 ORDER BY ledger, tx_index, op_index, trade_index LIMIT 50000`, id, since)
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	var trades []tradeRow
	set := map[string]bool{}
	for rows.Next() {
		var t tradeRow
		if err := rows.Scan(&t.Tx, &t.At, &t.Venue, &t.Pool, &t.Sold, &t.SoldAmt, &t.Bought, &t.BoughtAmt); err != nil {
			rows.Close()
			s.reply(w, nil, err)
			return
		}
		trades = append(trades, t)
		set[t.Sold], set[t.Bought] = true, true
	}
	rows.Close()
	px, err := s.xlmPrices(ctx, keys(set), time.Now())
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	type agg struct {
		Venue  string  `json:"venue"`
		Trades int     `json:"trades"`
		XLM    float64 `json:"volume_xlm"`
	}
	venues := map[string]*agg{}
	pairs := map[[2]string]*struct {
		Base, Counter string
		Trades        int
		XLM           float64
	}{}
	vol := 0.0
	for _, t := range trades {
		v, _ := tradeXLM(t, px)
		vol += v
		a := venues[t.Venue]
		if a == nil {
			a = &agg{Venue: t.Venue}
			venues[t.Venue] = a
		}
		a.Trades++
		a.XLM += v
		k := [2]string{t.Sold, t.Bought}
		if k[0] > k[1] {
			k[0], k[1] = k[1], k[0]
		}
		pp := pairs[k]
		if pp == nil {
			pp = &struct {
				Base, Counter string
				Trades        int
				XLM           float64
			}{Base: k[0], Counter: k[1]}
			pairs[k] = pp
		}
		pp.Trades++
		pp.XLM += v
	}
	vs := make([]*agg, 0, len(venues))
	for _, a := range venues {
		vs = append(vs, a)
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].XLM > vs[j].XLM })
	ps := make([]map[string]any, 0, len(pairs))
	for _, pp := range pairs {
		ps = append(ps, map[string]any{"base": pp.Base, "counter": pp.Counter, "trades": pp.Trades, "volume_xlm": pp.XLM})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i]["volume_xlm"].(float64) > ps[j]["volume_xlm"].(float64) })
	if len(ps) > 10 {
		ps = ps[:10]
	}
	cycles := findCycles(trades, px)
	cycProfit := 0.0
	for _, c := range cycles {
		cycProfit += c.ProfitXLM
	}
	sort.Slice(cycles, func(i, j int) bool { return cycles[i].ProfitXLM > cycles[j].ProfitXLM })
	topCycles := cycles
	if len(topCycles) > 10 {
		topCycles = topCycles[:10]
	}
	out["trading"] = map[string]any{
		"trades": len(trades), "truncated": len(trades) == 50000, "volume_xlm": vol, "venues": vs, "pairs": ps,
		"pnl": computePnL(trades, px), "cycles": len(cycles), "cycle_profit_xlm": cycProfit, "top_cycles": topCycles,
	}
	if usd := s.usdPerXLM(ctx, time.Now()); usd > 0 {
		out["usd_per_xlm"] = usd
	}

	for k, q := range map[string]string{
		"txs": `SELECT count(*) AS txs, count(*) FILTER (WHERE successful) AS successful, coalesce(sum(fee_charged), 0) / 1e7::float8 AS fees_xlm,
			count(*) FILTER (WHERE is_soroban) AS soroban, count(DISTINCT contract_id) AS contracts_called
			FROM transactions WHERE source = $1 AND closed_at > $2`,
		"seen": `SELECT least((SELECT min(closed_at) FROM transactions WHERE source = $1), (SELECT min(closed_at) FROM transfers WHERE from_addr = $1),
			(SELECT min(closed_at) FROM transfers WHERE to_addr = $1), (SELECT min(closed_at) FROM trades WHERE taker = $1)) AS first_seen,
			greatest((SELECT max(closed_at) FROM transactions WHERE source = $1), (SELECT max(closed_at) FROM transfers WHERE from_addr = $1),
			(SELECT max(closed_at) FROM transfers WHERE to_addr = $1), (SELECT max(closed_at) FROM trades WHERE taker = $1)) AS last_seen,
			$2::timestamptz AS window_start`,
	} {
		if out[k], err = s.one(ctx, q, id, since); err != nil {
			s.reply(w, nil, err)
			return
		}
	}
	for k, q := range map[string]string{
		"protocols": `SELECT protocol, action, count(*) AS events, count(DISTINCT tx_hash) AS txs FROM protocol_events
			WHERE user_addr = $1 AND closed_at > $2 GROUP BY 1, 2 ORDER BY 3 DESC`,
		"heatmap": `SELECT extract(isodow FROM t)::int AS dow, extract(hour FROM t)::int AS hour, count(DISTINCT h) AS txs FROM (
			SELECT closed_at AS t, hash AS h FROM transactions WHERE source = $1 AND closed_at > $2
			UNION ALL SELECT closed_at, tx_hash FROM transfers WHERE from_addr = $1 AND closed_at > $2
			UNION ALL SELECT closed_at, tx_hash FROM transfers WHERE to_addr = $1 AND closed_at > $2
			UNION ALL SELECT closed_at, tx_hash FROM trades WHERE taker = $1 AND closed_at > $2) x GROUP BY 1, 2`,
		"largest_transfers": `WITH ` + latestPricesCTE + `
			SELECT t.closed_at, t.tx_hash, t.type, t.from_addr, t.to_addr, t.asset, t.amount / 1e7::float8 AS amount,
			       (t.amount / 1e7::float8) * px.p AS value_xlm,
			       CASE WHEN t.to_addr = $1 THEN 'in' ELSE 'out' END AS direction
			FROM transfers t JOIN px ON px.asset = t.asset
			WHERE (t.from_addr = $1 OR t.to_addr = $1) AND t.closed_at > $2 ORDER BY value_xlm DESC NULLS LAST LIMIT 10`,
		"counterparties": `WITH ` + latestPricesCTE + `
			SELECT cp AS address, count(*) AS transfers, count(DISTINCT asset) AS assets,
			       coalesce(sum(v) FILTER (WHERE dir = 'in'), 0) AS in_xlm, coalesce(sum(v) FILTER (WHERE dir = 'out'), 0) AS out_xlm, max(closed_at) AS last
			FROM (SELECT to_addr AS cp, t.asset, 'out' AS dir, t.closed_at, (t.amount / 1e7::float8) * px.p AS v FROM transfers t LEFT JOIN px ON px.asset = t.asset
			        WHERE from_addr = $1 AND to_addr IS NOT NULL AND to_addr <> $1 AND t.closed_at > $2
			      UNION ALL
			      SELECT from_addr, t.asset, 'in', t.closed_at, (t.amount / 1e7::float8) * px.p FROM transfers t LEFT JOIN px ON px.asset = t.asset
			        WHERE to_addr = $1 AND from_addr IS NOT NULL AND from_addr <> $1 AND t.closed_at > $2) x
			GROUP BY cp ORDER BY count(*) DESC LIMIT 10`,
		"flows": `WITH ` + latestPricesCTE + `
			SELECT f.asset, f.in_amount, f.out_amount, f.in_amount * px.p AS in_xlm, f.out_amount * px.p AS out_xlm FROM (
			  SELECT asset, coalesce(sum(amount) FILTER (WHERE to_addr = $1), 0) / 1e7::float8 AS in_amount,
			         coalesce(sum(amount) FILTER (WHERE from_addr = $1), 0) / 1e7::float8 AS out_amount
			  FROM transfers WHERE (from_addr = $1 OR to_addr = $1) AND closed_at > $2 GROUP BY asset) f
			LEFT JOIN px ON px.asset = f.asset ORDER BY greatest(f.in_amount, f.out_amount) * coalesce(px.p, 0) DESC LIMIT 15`,
	} {
		if out[k], err = s.query(ctx, q, id, since); err != nil {
			s.reply(w, nil, err)
			return
		}
	}
	s.reply(w, out, nil)
}
