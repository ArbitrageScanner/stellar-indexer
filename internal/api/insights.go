package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tags, user labels, leaderboards and notable-event alerts.

type tag struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Reason string `json:"reason"`
	Group  string `json:"group"` // size | behavior | role | defi | lifecycle | user
	Kind   string `json:"kind"`  // auto | user
	Note   string `json:"note,omitempty"`
}

// Heuristic thresholds; documented in the README and shown in the UI.
const (
	whaleXLM      = 1_000_000
	sharkXLM      = 100_000
	hftTxs24h     = 500
	hftTrades24h  = 1000
	mmOfferOps24h = 100
	arbCycles7d   = 3
	newAccountAge = 7 * 24 * time.Hour
	dormantAfter  = 7 * 24 * time.Hour
	ledgerSeconds = 5.5
)

// autoTags derives behavioural tags for an address from the index.
func (s *Server) autoTags(ctx context.Context, id string) ([]tag, error) {
	row, err := s.one(ctx, `SELECT
		(SELECT balance / 1e7::float8 FROM accounts WHERE account_id = $1 AND NOT deleted) AS xlm,
		(SELECT seq_num >> 32 FROM accounts WHERE account_id = $1) AS created,
		(SELECT count(*) FROM offers WHERE seller = $1) AS offers,
		(SELECT count(*) FROM offers o1 WHERE seller = $1 AND EXISTS (
		   SELECT 1 FROM offers o2 WHERE o2.seller = $1 AND o2.selling = o1.buying AND o2.buying = o1.selling)) AS two_sided,
		(SELECT count(*) FROM transactions WHERE source = $1 AND closed_at > now() - interval '24 hours') AS txs24,
		(SELECT count(*) FROM transactions WHERE source = $1 AND NOT successful AND closed_at > now() - interval '24 hours') AS failed24,
		(SELECT count(*) FROM trades WHERE taker = $1 AND closed_at > now() - interval '24 hours') AS trades24,
		(SELECT count(*) FROM operations WHERE source = $1 AND closed_at > now() - interval '24 hours'
		   AND type IN ('manage_sell_offer', 'manage_buy_offer', 'create_passive_sell_offer')) AS offer_ops24,
		(SELECT count(*) FROM (SELECT tx_hash FROM trades WHERE taker = $1 AND closed_at > now() - interval '7 days' GROUP BY tx_hash
		   HAVING count(*) >= 2 AND (array_agg(sold_asset ORDER BY op_index, trade_index))[1] = (array_agg(bought_asset ORDER BY op_index DESC, trade_index DESC))[1]) c) AS cycles7d,
		(SELECT count(*) FROM protocol_events WHERE user_addr = $1 AND action = 'flash_loan') AS flash_loans,
		(SELECT array_agg(DISTINCT protocol) FROM protocol_events WHERE user_addr = $1) AS protocols_used,
		(SELECT count(*) FROM assets WHERE issuer = $1) AS issued_n,
		(SELECT array_agg(code) FROM (SELECT code FROM assets WHERE issuer = $1 ORDER BY first_seen LIMIT 4) a) AS issued,
		(SELECT asset_key FROM assets WHERE contract_id = $1 LIMIT 1) AS token,
		(SELECT protocol FROM protocol_events WHERE contract_id = $1 LIMIT 1) AS proto_contract,
		(SELECT array_agg(DISTINCT action) FROM (SELECT action FROM protocol_events WHERE contract_id = $1 LIMIT 1000) a) AS proto_actions,
		(SELECT count(*) FROM trustlines WHERE account_id = $1 AND asset LIKE 'pool:%' AND balance > 0) AS lp_shares,
		(SELECT count(*) FROM protocol_events WHERE user_addr = $1 AND action IN ('deposit', 'deposit_liquidity')) AS lp_deposits,
		(SELECT count(*) FROM protocol_events WHERE user_addr = $1 AND protocol = 'blend' AND action = 'borrow') AS borrows,
		greatest((SELECT max(closed_at) FROM transactions WHERE source = $1), (SELECT max(closed_at) FROM transfers WHERE from_addr = $1),
		         (SELECT max(closed_at) FROM transfers WHERE to_addr = $1)) AS last_active,
		(SELECT max(sequence) FROM ledgers) AS tip, (SELECT min(closed_at) FROM ledgers) AS index_start`, id)
	if err != nil || row == nil {
		return nil, err
	}
	n := func(k string) int64 {
		switch v := row[k].(type) {
		case int64:
			return v
		case int32:
			return int64(v)
		}
		return 0
	}
	strs := func(k string) []string {
		var out []string
		if a, ok := row[k].([]any); ok {
			for _, v := range a {
				if s, ok := v.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}
	var tags []tag
	add := func(id, label, group, reason string, args ...any) {
		tags = append(tags, tag{ID: id, Label: label, Group: group, Kind: "auto", Reason: fmt.Sprintf(reason, args...)})
	}
	if x, ok := row["xlm"].(float64); ok {
		switch {
		case x >= whaleXLM:
			add("whale", "Whale", "size", "Holds %s XLM (≥ %s).", human(x), human(whaleXLM))
		case x >= sharkXLM:
			add("shark", "Large holder", "size", "Holds %s XLM (≥ %s).", human(x), human(sharkXLM))
		}
	}
	if c := n("cycles7d"); c >= arbCycles7d || (c >= 1 && n("flash_loans") > 0) {
		add("arbitrage_bot", "Arbitrage bot", "behavior", "%d same-transaction round trips (first sold asset = last bought) in 7 days.", c)
	}
	if f := n("flash_loans"); f > 0 {
		add("flash_loans", "Flash loans", "defi", "Took %d Blend flash loans.", f)
	}
	if two, off := n("two_sided"), n("offers"); (two > 0 && off >= 4) || n("offer_ops24") >= mmOfferOps24h {
		add("market_maker", "Market maker", "behavior", "%d open SDEX offers (%d two-sided) and %d offer updates in 24h.", off, two, n("offer_ops24"))
	}
	if t, tr := n("txs24"), n("trades24"); t >= hftTxs24h || tr >= hftTrades24h {
		add("hft", "High-frequency", "behavior", "%d transactions and %d fills in the last 24h.", t, tr)
	}
	if t, f := n("txs24"), n("failed24"); t >= 50 && f*2 >= t {
		add("failing_bot", "Mostly failing", "behavior", "%d of %d transactions in 24h failed (typical of competing arbitrage bots).", f, t)
	}
	if k := n("issued_n"); k > 0 {
		add("issuer", "Asset issuer", "role", "Issuer of %d asset(s): %s.", k, strings.Join(strs("issued"), ", "))
	}
	if tok, ok := row["token"].(string); ok {
		add("token_contract", "Token contract", "role", "Stellar Asset Contract / token for %s.", tok)
	}
	if p, ok := row["proto_contract"].(string); ok {
		acts := strs("proto_actions")
		role := map[string]string{"blend": "lending pool", "aquarius": "pool", "soroswap": "pair", "clmm": "pool", "amm_generic": "pool"}[p]
		for _, a := range acts {
			if strings.Contains(strings.ToLower(a), "router") {
				role = "router"
			}
		}
		if role == "" {
			role = "contract"
		}
		add("protocol_"+p, titleCase(p)+" "+role, "role", "Emits %s events: %s.", p, strings.Join(acts[:min(len(acts), 6)], ", "))
	}
	if n("lp_shares") > 0 || n("lp_deposits") > 0 {
		add("lp", "Liquidity provider", "defi", "Holds %d classic pool share(s); %d Soroban liquidity deposits.", n("lp_shares"), n("lp_deposits"))
	}
	if n("borrows") > 0 {
		add("borrower", "Blend borrower", "defi", "%d borrow events on Blend.", n("borrows"))
	}
	if ps := strs("protocols_used"); len(ps) > 0 {
		add("defi_user", "DeFi user", "defi", "Active on %s.", strings.Join(ps, ", "))
	}
	tip := n("tip")
	if c := n("created"); c > 0 && tip > 0 {
		age := time.Duration(float64(tip-c)*ledgerSeconds) * time.Second
		if age >= 0 && age < newAccountAge {
			add("new_account", "New account", "lifecycle", "Created ~%s ago (ledger %d, from its starting sequence number).", roundDur(age), c)
		}
	}
	if _, known := row["xlm"].(float64); known {
		last, ok := row["last_active"].(time.Time)
		start, _ := row["index_start"].(time.Time)
		if !ok {
			add("dormant", "Dormant", "lifecycle", "No transactions or transfers since indexing began (%s).", start.UTC().Format("Jan 2 15:04 UTC"))
		} else if time.Since(last) > dormantAfter {
			add("dormant", "Dormant", "lifecycle", "Last active %s ago.", roundDur(time.Since(last)))
		}
	}
	return tags, nil
}

func human(v float64) string {
	switch {
	case v >= 1e9:
		return strconv.FormatFloat(v/1e9, 'f', 1, 64) + "B"
	case v >= 1e6:
		return strconv.FormatFloat(v/1e6, 'f', 1, 64) + "M"
	case v >= 1e3:
		return strconv.FormatFloat(v/1e3, 'f', 1, 64) + "K"
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}

func roundDur(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func titleCase(s string) string {
	if s == "amm_generic" {
		return "Generic AMM"
	}
	if s == "clmm" {
		return "CLMM"
	}
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Server) accountTags(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	tags, err := s.autoTags(ctx, id)
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	user, err := s.query(ctx, `SELECT tag, note, created_at FROM labels WHERE address = $1 ORDER BY created_at`, id)
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	for _, u := range user {
		note, _ := u["note"].(string)
		tags = append(tags, tag{ID: u["tag"].(string), Label: u["tag"].(string), Group: "user", Kind: "user", Note: note, Reason: note})
	}
	if tags == nil {
		tags = []tag{}
	}
	s.reply(w, tags, nil)
}

// ---- user labels ----------------------------------------------------------------

var reTag = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

// writeAllowed enforces LABELS_TOKEN (when set) on label writes.
func (s *Server) writeAllowed(w http.ResponseWriter, r *http.Request) bool {
	if s.LabelsToken == "" {
		return true
	}
	if r.Header.Get("Authorization") == "Bearer "+s.LabelsToken {
		return true
	}
	writeErr(w, http.StatusUnauthorized, fmt.Errorf("label writes require Authorization: Bearer <LABELS_TOKEN>"))
	return false
}

func (s *Server) listLabels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := &filters{}
	if v := q.Get("tag"); v != "" {
		f.add("tag = ?", strings.ToLower(strings.TrimPrefix(v, "#")))
	}
	if v := q.Get("address"); v != "" {
		f.add("address = ?", v)
	}
	f.args = append(f.args, limit(r, 200, 2000))
	rows, err := s.query(r.Context(), `SELECT l.address, l.tag, l.note, l.created_at, a.balance / 1e7::float8 AS xlm
		FROM labels l LEFT JOIN accounts a ON a.account_id = l.address
		WHERE `+f.sql()+fmt.Sprintf(` ORDER BY l.created_at DESC LIMIT $%d`, len(f.args)), f.args...)
	s.reply(w, rows, err)
}

func (s *Server) labelTags(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `SELECT tag, count(*) AS addresses, max(created_at) AS last_used FROM labels GROUP BY tag ORDER BY 2 DESC, 1`)
	s.reply(w, rows, err)
}

func (s *Server) putLabel(w http.ResponseWriter, r *http.Request) {
	if !s.writeAllowed(w, r) {
		return
	}
	var in struct{ Address, Tag, Note string }
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err == nil {
		err = json.Unmarshal(body, &in)
	}
	if err != nil {
		s.reply(w, nil, badRequest{fmt.Errorf("body must be JSON {address, tag, note}: %v", err)})
		return
	}
	in.Tag = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(in.Tag), "#"))
	in.Address = strings.TrimSpace(in.Address)
	switch {
	case !reStrkey.MatchString(in.Address):
		s.reply(w, nil, badRequest{fmt.Errorf("address must be a G…, M… or C… strkey")})
		return
	case !reTag.MatchString(in.Tag):
		s.reply(w, nil, badRequest{fmt.Errorf("tag must match %s", reTag)})
		return
	case len(in.Note) > 280:
		s.reply(w, nil, badRequest{fmt.Errorf("note is limited to 280 characters")})
		return
	}
	row, err := s.one(r.Context(), `INSERT INTO labels (address, tag, note) VALUES ($1, $2, $3)
		ON CONFLICT (address, tag) DO UPDATE SET note = EXCLUDED.note RETURNING address, tag, note, created_at`, in.Address, in.Tag, sanitizeText(in.Note))
	s.reply(w, row, err)
}

func (s *Server) deleteLabel(w http.ResponseWriter, r *http.Request) {
	if !s.writeAllowed(w, r) {
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM labels WHERE address = $1 AND tag = $2`, r.PathValue("address"), strings.ToLower(r.PathValue("tag")))
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, fmt.Errorf("label not found"))
		return
	}
	s.reply(w, map[string]any{"deleted": true}, nil)
}

func sanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0 || (r < 32 && r != '\n') {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
}

// attachLabels adds a "labels" array to rows whose key column is an address.
func (s *Server) attachLabels(ctx context.Context, rows []map[string]any, key string) {
	var addrs []string
	for _, r := range rows {
		if a, ok := r[key].(string); ok {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return
	}
	ls, err := s.query(ctx, `SELECT address, array_agg(tag ORDER BY tag) AS tags FROM labels WHERE address = ANY($1) GROUP BY address`, addrs)
	if err != nil {
		return
	}
	m := map[string]any{}
	for _, l := range ls {
		m[l["address"].(string)] = l["tags"]
	}
	for _, r := range rows {
		if a, ok := r[key].(string); ok && m[a] != nil {
			r["labels"] = m[a]
		}
	}
}

// cyclesCTE finds same-transaction round trips in the last $1 hours: fills
// of one taker in one tx whose first sold asset is the last bought one; net
// is the gain in that asset. One sort pass via window functions.
const cyclesCTE = `legs AS (
	  SELECT ledger, closed_at, taker, tx_hash, sold_asset, sold_amount, bought_asset, bought_amount,
	         first_value(sold_asset) OVER w AS a0, last_value(bought_asset) OVER w AS a1, count(*) OVER w AS hops
	  FROM trades WHERE closed_at > now() - make_interval(hours => $1)
	  WINDOW w AS (PARTITION BY taker, tx_hash ORDER BY op_index, trade_index ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)),
	cyc AS (
	  SELECT taker, tx_hash, min(ledger) AS ledger, min(closed_at) AS closed_at, max(hops) AS hops, a0 AS asset,
	         (coalesce(sum(bought_amount) FILTER (WHERE bought_asset = a0), 0) - coalesce(sum(sold_amount) FILTER (WHERE sold_asset = a0), 0)) / 1e7::float8 AS net
	  FROM legs WHERE hops >= 2 AND a0 = a1 GROUP BY taker, tx_hash, a0)`

// ---- leaderboards ---------------------------------------------------------------

// ledgerFloor is the first ledger of the last $1 hours (by ledger count).
const ledgerFloor = `(SELECT max(sequence) FROM ledgers) - ($1::int * 3600 / 5.5)::bigint`

func (s *Server) leaderboards(w http.ResponseWriter, r *http.Request) {
	ctx, h, n := r.Context(), hours(r, 24), limit(r, 15, 100)
	out := map[string]any{"hours": h}
	boards := []struct{ name, key, sql string }{
		{"traders", "address", `WITH ` + latestPricesCTE + `
			SELECT taker AS address, count(*) AS trades, count(DISTINCT tx_hash) AS txs, array_agg(DISTINCT venue) AS venues,
			  sum(CASE WHEN sold_asset = 'native' THEN sold_amount / 1e7::float8 WHEN bought_asset = 'native' THEN bought_amount / 1e7::float8
			           ELSE (sold_amount / 1e7::float8) * px.p END) AS volume_xlm
			FROM trades LEFT JOIN px ON px.asset = trades.sold_asset
			WHERE closed_at > now() - make_interval(hours => $1) GROUP BY taker ORDER BY volume_xlm DESC NULLS LAST LIMIT $2`},
		{"arbitrageurs", "address", `WITH ` + latestPricesCTE + `, ` + cyclesCTE + `
			SELECT taker AS address, count(*) AS cycles, round(avg(hops), 1) AS avg_hops,
			       count(*) FILTER (WHERE net > 0) AS profitable, sum(net * px.p) AS profit_xlm
			FROM cyc LEFT JOIN px ON px.asset = cyc.asset GROUP BY taker ORDER BY cycles DESC LIMIT $2`},
		{"active", "address", `SELECT source AS address, count(*) AS txs, count(*) FILTER (WHERE NOT successful) AS failed,
			count(*) FILTER (WHERE is_soroban) AS soroban, sum(fee_charged) / 1e7::float8 AS fees_xlm, sum(op_count) AS ops
			FROM transactions WHERE ledger > ` + ledgerFloor + ` GROUP BY source ORDER BY txs DESC LIMIT $2`},
		{"fee_payers", "address", `SELECT fee_account AS address, sum(fee_charged) / 1e7::float8 AS fees_xlm, count(*) AS txs,
			max(fee_charged) / 1e7::float8 AS max_fee_xlm
			FROM transactions WHERE ledger > ` + ledgerFloor + ` GROUP BY fee_account ORDER BY fees_xlm DESC LIMIT $2`},
		{"pools", "pool", `WITH ` + latestPricesCTE + `
			SELECT venue, pool, min(base) AS base, min(counter) AS counter, count(*) AS trades, count(DISTINCT taker) AS takers,
			  sum(CASE WHEN base = 'native' THEN base_volume WHEN counter = 'native' THEN counter_volume ELSE base_volume * px.p END) AS volume_xlm
			FROM trades LEFT JOIN px ON px.asset = trades.base
			WHERE closed_at > now() - make_interval(hours => $1) AND pool <> '' GROUP BY venue, pool ORDER BY volume_xlm DESC NULLS LAST LIMIT $2`},
		{"protocol_users", "address", `SELECT user_addr AS address, count(*) AS events, count(DISTINCT tx_hash) AS txs,
			array_agg(DISTINCT protocol) AS protocols, array_agg(DISTINCT action) AS actions
			FROM protocol_events WHERE user_addr IS NOT NULL AND closed_at > now() - make_interval(hours => $1)
			GROUP BY user_addr ORDER BY events DESC LIMIT $2`},
		{"whales", "address", `SELECT account_id AS address, balance / 1e7::float8 AS xlm, num_subentries, home_domain, last_modified
			FROM accounts WHERE NOT deleted AND $1::int > 0 ORDER BY balance DESC LIMIT $2`},
	}
	for _, b := range boards {
		rows, err := s.query(ctx, b.sql, h, n)
		if err != nil {
			s.reply(w, nil, fmt.Errorf("%s: %w", b.name, err))
			return
		}
		s.attachLabels(ctx, rows, b.key)
		out[b.name] = rows
	}
	s.reply(w, out, nil)
}

// ---- alerts -----------------------------------------------------------------------

// alerts merges notable events in the window: large transfers, swaps and
// flash loans (valued in XLM), profitable arbitrage cycles, sharp price moves
// and newly seen assets.
func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	ctx, q, h := r.Context(), r.URL.Query(), hours(r, 24)
	minXLM, _ := strconv.ParseFloat(q.Get("min_xlm"), 64)
	if minXLM <= 0 {
		minXLM = 25000
	}
	minMove, _ := strconv.ParseFloat(q.Get("min_move"), 64)
	if minMove <= 0 {
		minMove = 5
	}
	minProfit, _ := strconv.ParseFloat(q.Get("min_profit"), 64)
	if minProfit <= 0 {
		minProfit = 5
	}
	kinds := map[string]bool{}
	for _, k := range strings.Split(q.Get("kinds"), ",") {
		if k != "" {
			kinds[k] = true
		}
	}
	queries := []struct {
		kind, sql string
		args      []any
	}{
		{"large_transfer", `WITH ` + latestPricesCTE + `
			SELECT t.ledger, t.closed_at, t.tx_hash, t.type AS action, t.from_addr, t.to_addr, t.asset, t.amount / 1e7::float8 AS amount,
			       (t.amount / 1e7::float8) * px.p AS value_xlm
			FROM transfers t JOIN px ON px.asset = t.asset
			WHERE t.ledger > ` + ledgerFloor + ` AND (t.amount / 1e7::float8) * px.p >= $2 ORDER BY value_xlm DESC LIMIT 60`, []any{h, minXLM}},
		{"large_trade", `WITH ` + latestPricesCTE + `
			SELECT t.ledger, t.closed_at, t.tx_hash, t.venue AS action, t.taker AS from_addr, t.pool AS to_addr, t.sold_asset AS asset,
			       t.sold_amount / 1e7::float8 AS amount, t.bought_asset AS asset2, t.bought_amount / 1e7::float8 AS amount2,
			       CASE WHEN t.sold_asset = 'native' THEN t.sold_amount / 1e7::float8 WHEN t.bought_asset = 'native' THEN t.bought_amount / 1e7::float8
			            ELSE (t.sold_amount / 1e7::float8) * px.p END AS value_xlm
			FROM trades t LEFT JOIN px ON px.asset = t.sold_asset
			WHERE t.closed_at > now() - make_interval(hours => $1)
			  AND CASE WHEN t.sold_asset = 'native' THEN t.sold_amount / 1e7::float8 WHEN t.bought_asset = 'native' THEN t.bought_amount / 1e7::float8
			           ELSE (t.sold_amount / 1e7::float8) * px.p END >= $2
			ORDER BY value_xlm DESC LIMIT 60`, []any{h, minXLM}},
		{"flash_loan", `WITH ` + latestPricesCTE + `
			SELECT e.ledger, e.closed_at, e.tx_hash, e.action, e.user_addr AS from_addr, e.contract_id AS to_addr, e.asset,
			       e.amount0 / 1e7::float8 AS amount, (e.amount0 / 1e7::float8) * px.p AS value_xlm
			FROM protocol_events e LEFT JOIN px ON px.asset = e.asset
			WHERE e.action = 'flash_loan' AND e.closed_at > now() - make_interval(hours => $1)
			ORDER BY e.closed_at DESC LIMIT 60`, []any{h}},
		{"arbitrage", `WITH ` + latestPricesCTE + `, ` + cyclesCTE + `
			SELECT cyc.ledger, cyc.closed_at, cyc.tx_hash, cyc.hops::text || ' hops' AS action, cyc.taker AS from_addr, NULL AS to_addr,
			       cyc.asset, cyc.net AS amount, cyc.net * px.p AS value_xlm
			FROM cyc LEFT JOIN px ON px.asset = cyc.asset
			WHERE cyc.net * px.p >= $2 ORDER BY value_xlm DESC LIMIT 60`, []any{h, minProfit}},
		{"price_move", `SELECT NULL::bigint AS ledger, max(bucket) AS closed_at, NULL AS tx_hash, base AS asset, counter AS asset2,
			(array_agg(open ORDER BY bucket))[1] AS open, (array_agg(close ORDER BY bucket DESC))[1] AS close, sum(trades) AS trades,
			((array_agg(close ORDER BY bucket DESC))[1] / nullif((array_agg(open ORDER BY bucket))[1], 0) - 1) * 100 AS change_pct,
			CASE WHEN counter = 'native' THEN sum(counter_volume) WHEN base = 'native' THEN sum(base_volume) END AS value_xlm
			FROM candles_1m WHERE bucket > now() - interval '1 hour' AND (base = 'native' OR counter = 'native')
			GROUP BY base, counter HAVING sum(trades) >= 10
			  AND CASE WHEN counter = 'native' THEN sum(counter_volume) ELSE sum(base_volume) END >= 250
			  AND abs((array_agg(close ORDER BY bucket DESC))[1] / nullif((array_agg(open ORDER BY bucket))[1], 0) - 1) * 100 >= $1
			ORDER BY abs((array_agg(close ORDER BY bucket DESC))[1] / nullif((array_agg(open ORDER BY bucket))[1], 0) - 1) DESC LIMIT 30`, []any{minMove}},
		{"new_asset", `SELECT a.first_seen AS ledger, l.closed_at, NULL AS tx_hash, a.asset_key AS asset, a.type AS action, a.issuer AS from_addr,
			a.contract_id AS to_addr, (SELECT count(*) FROM trades t WHERE (t.base = a.asset_key OR t.counter = a.asset_key)) AS trades
			FROM assets a JOIN ledgers l ON l.sequence = a.first_seen
			-- assets first seen at least a day after indexing began are likely genuinely new
			WHERE a.first_seen > ` + ledgerFloor + ` AND a.first_seen > (SELECT min(sequence) FROM ledgers) + 15709
			ORDER BY a.first_seen DESC LIMIT 30`, []any{h}},
	}
	all := []map[string]any{}
	counts := map[string]int{}
	for _, aq := range queries {
		if len(kinds) > 0 && !kinds[aq.kind] {
			continue
		}
		rows, err := s.query(ctx, aq.sql, aq.args...)
		if err != nil {
			s.reply(w, nil, fmt.Errorf("%s: %w", aq.kind, err))
			return
		}
		for _, r := range rows {
			r["kind"] = aq.kind
		}
		counts[aq.kind] = len(rows)
		all = append(all, rows...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		ti, _ := all[i]["closed_at"].(time.Time)
		tj, _ := all[j]["closed_at"].(time.Time)
		return ti.After(tj)
	})
	if n := limit(r, 200, 1000); len(all) > n {
		all = all[:n]
	}
	s.reply(w, map[string]any{"hours": h, "min_xlm": minXLM, "min_move_pct": minMove, "min_profit_xlm": minProfit, "counts": counts, "alerts": all}, nil)
}
