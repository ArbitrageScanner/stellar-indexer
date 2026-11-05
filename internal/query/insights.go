package query

import (
	"context"
	"fmt"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
)

// cyclesCTE finds same-transaction round trips among verified fills after
// the since placeholder: fills of one taker in one tx whose first sold asset
// is the last bought one; net is the gain in that asset. One sort pass.
func cyclesCTE(since string) string {
	return fmt.Sprintf(`legs AS (
	  SELECT ledger, closed_at, taker, tx_hash, sold_asset, sold_amount, bought_asset, bought_amount,
	         first_value(sold_asset) OVER w AS a0, last_value(bought_asset) OVER w AS a1, count(*) OVER w AS hops
	  FROM trades WHERE verified AND closed_at > %s
	  WINDOW w AS (PARTITION BY taker, tx_hash ORDER BY op_index, trade_index ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)),
	cyc AS (
	  SELECT taker, tx_hash, min(ledger) AS ledger, min(closed_at) AS closed_at, max(hops) AS hops, a0 AS asset,
	         (coalesce(sum(bought_amount) FILTER (WHERE bought_asset = a0), 0) - coalesce(sum(sold_amount) FILTER (WHERE sold_asset = a0), 0)) / 1e7::float8 AS net
	  FROM legs WHERE hops >= 2 AND a0 = a1 GROUP BY taker, tx_hash, a0)`, since)
}

// Board is one leaderboard and the column holding its address.
type Board struct {
	Name, Key string
	Rows      Rows
}

// Leaderboards ranks addresses and pools since a time ($1). pxSince starts
// the price window ($3). Fills and protocol events are verified only.
func (r *Repo) Leaderboards(ctx context.Context, since, pxSince time.Time, limit int) ([]Board, error) {
	px := pxCTE("$3")
	boards := []struct{ name, key, sql string }{
		{"traders", "address", `WITH ` + px + `
			SELECT taker AS address, count(*) AS trades, count(DISTINCT tx_hash) AS txs, array_agg(DISTINCT venue) AS venues,
			  sum(CASE WHEN sold_asset = 'native' THEN sold_amount / 1e7::float8 WHEN bought_asset = 'native' THEN bought_amount / 1e7::float8
			           ELSE (sold_amount / 1e7::float8) * px.p END) AS volume_xlm
			FROM trades LEFT JOIN px ON px.asset = trades.sold_asset
			WHERE verified AND closed_at > $1 GROUP BY taker ORDER BY volume_xlm DESC NULLS LAST LIMIT $2`},
		{"arbitrageurs", "address", `WITH ` + px + `, ` + cyclesCTE("$1") + `
			SELECT taker AS address, count(*) AS cycles, round(avg(hops), 1) AS avg_hops,
			       count(*) FILTER (WHERE net > 0) AS profitable, sum(net * px.p) AS profit_xlm
			FROM cyc LEFT JOIN px ON px.asset = cyc.asset GROUP BY taker ORDER BY cycles DESC LIMIT $2`},
		{"active", "address", `SELECT source AS address, count(*) AS txs, count(*) FILTER (WHERE NOT successful) AS failed,
			count(*) FILTER (WHERE is_soroban) AS soroban, sum(fee_charged) / 1e7::float8 AS fees_xlm, sum(op_count) AS ops
			FROM transactions WHERE ledger >= ` + ledgerSince("$1") + ` AND $3::timestamptz IS NOT NULL GROUP BY source ORDER BY txs DESC LIMIT $2`},
		{"fee_payers", "address", `SELECT fee_account AS address, sum(fee_charged) / 1e7::float8 AS fees_xlm, count(*) AS txs,
			max(fee_charged) / 1e7::float8 AS max_fee_xlm
			FROM transactions WHERE ledger >= ` + ledgerSince("$1") + ` AND $3::timestamptz IS NOT NULL GROUP BY fee_account ORDER BY fees_xlm DESC LIMIT $2`},
		{"pools", "pool", `WITH ` + px + `
			SELECT venue, pool, min(base) AS base, min(counter) AS counter, count(*) AS trades, count(DISTINCT taker) AS takers,
			  sum(CASE WHEN base = 'native' THEN base_volume WHEN counter = 'native' THEN counter_volume ELSE base_volume * px.p END) AS volume_xlm
			FROM trades LEFT JOIN px ON px.asset = trades.base
			WHERE verified AND closed_at > $1 AND pool <> '' GROUP BY venue, pool ORDER BY volume_xlm DESC NULLS LAST LIMIT $2`},
		{"protocol_users", "address", `SELECT user_addr AS address, count(*) AS events, count(DISTINCT tx_hash) AS txs,
			array_agg(DISTINCT protocol) AS protocols, array_agg(DISTINCT action) AS actions
			FROM protocol_events WHERE verified AND user_addr IS NOT NULL AND closed_at > $1 AND $3::timestamptz IS NOT NULL
			GROUP BY user_addr ORDER BY events DESC LIMIT $2`},
		{"whales", "address", `SELECT account_id AS address, balance / 1e7::float8 AS xlm, num_subentries, home_domain, last_modified
			FROM accounts WHERE NOT deleted AND $1::timestamptz IS NOT NULL AND $3::timestamptz IS NOT NULL ORDER BY balance DESC LIMIT $2`},
	}
	out := make([]Board, 0, len(boards))
	for _, b := range boards {
		rows, err := r.rows(ctx, b.sql, since, limit, pxSince)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.name, err)
		}
		out = append(out, Board{Name: b.name, Key: b.key, Rows: rows})
	}
	return out, nil
}

// AlertQuery parameterises Alerts. Since/PxSince/MoveSince are window starts
// for events, the price window and the price-move window (1 h).
type AlertQuery struct {
	Since, PxSince, MoveSince time.Time
	Thresholds                analytics.AlertThresholds
	Kinds                     map[string]bool // empty = all
	IndexStartLedger          int64
}

// Alerts loads notable events per kind: large transfers and swaps (XLM
// valued), flash loans, profitable round trips (verified fills), sharp
// hourly moves on liquid XLM pairs, and genuinely new assets.
func (r *Repo) Alerts(ctx context.Context, q AlertQuery) (map[string][]map[string]any, error) {
	px := pxCTE("$2")
	th := q.Thresholds
	kinds := []struct {
		kind, sql string
		args      []any
	}{
		{"large_transfer", `WITH ` + px + `
			SELECT t.ledger, t.closed_at, t.tx_hash, t.type AS action, t.from_addr, t.to_addr, t.asset, t.amount / 1e7::float8 AS amount,
			       (t.amount / 1e7::float8) * px.p AS value_xlm
			FROM transfers t JOIN px ON px.asset = t.asset
			WHERE t.ledger >= ` + ledgerSince("$1") + ` AND (t.amount / 1e7::float8) * px.p >= $3 ORDER BY value_xlm DESC LIMIT 60`,
			[]any{q.Since, q.PxSince, th.MinXLM}},
		{"large_trade", `WITH ` + px + `
			SELECT * FROM (SELECT t.ledger, t.closed_at, t.tx_hash, t.venue AS action, t.taker AS from_addr, t.pool AS to_addr, t.sold_asset AS asset,
			       t.sold_amount / 1e7::float8 AS amount, t.bought_asset AS asset2, t.bought_amount / 1e7::float8 AS amount2,
			       CASE WHEN t.sold_asset = 'native' THEN t.sold_amount / 1e7::float8 WHEN t.bought_asset = 'native' THEN t.bought_amount / 1e7::float8
			            ELSE (t.sold_amount / 1e7::float8) * px.p END AS value_xlm
			FROM trades t LEFT JOIN px ON px.asset = t.sold_asset WHERE t.verified AND t.closed_at > $1) x
			WHERE value_xlm >= $3 ORDER BY value_xlm DESC LIMIT 60`,
			[]any{q.Since, q.PxSince, th.MinXLM}},
		{"flash_loan", `WITH ` + px + `
			SELECT e.ledger, e.closed_at, e.tx_hash, e.action, e.user_addr AS from_addr, e.contract_id AS to_addr, e.asset,
			       e.amount0 / 1e7::float8 AS amount, (e.amount0 / 1e7::float8) * px.p AS value_xlm
			FROM protocol_events e LEFT JOIN px ON px.asset = e.asset
			WHERE e.verified AND e.action = 'flash_loan' AND e.closed_at > $1 ORDER BY e.closed_at DESC LIMIT 60`,
			[]any{q.Since, q.PxSince}},
		{"arbitrage", `WITH ` + px + `, ` + cyclesCTE("$1") + `
			SELECT cyc.ledger, cyc.closed_at, cyc.tx_hash, cyc.hops::text || ' hops' AS action, cyc.taker AS from_addr, NULL AS to_addr,
			       cyc.asset, cyc.net AS amount, cyc.net * px.p AS value_xlm
			FROM cyc LEFT JOIN px ON px.asset = cyc.asset
			WHERE cyc.net * px.p >= $3 ORDER BY value_xlm DESC LIMIT 60`,
			[]any{q.Since, q.PxSince, th.MinProfit}},
		{"price_move", `SELECT NULL::bigint AS ledger, max(bucket) AS closed_at, NULL AS tx_hash, base AS asset, counter AS asset2,
			(array_agg(open ORDER BY bucket))[1] AS open, (array_agg(close ORDER BY bucket DESC))[1] AS close, sum(trades) AS trades,
			((array_agg(close ORDER BY bucket DESC))[1] / nullif((array_agg(open ORDER BY bucket))[1], 0) - 1) * 100 AS change_pct,
			CASE WHEN counter = 'native' THEN sum(counter_volume) WHEN base = 'native' THEN sum(base_volume) END AS value_xlm
			FROM candles_1m WHERE bucket > $1 AND (base = 'native' OR counter = 'native')
			GROUP BY base, counter HAVING sum(trades) >= 10
			  AND CASE WHEN counter = 'native' THEN sum(counter_volume) ELSE sum(base_volume) END >= $3
			  AND abs((array_agg(close ORDER BY bucket DESC))[1] / nullif((array_agg(open ORDER BY bucket))[1], 0) - 1) * 100 >= $2
			ORDER BY abs((array_agg(close ORDER BY bucket DESC))[1] / nullif((array_agg(open ORDER BY bucket))[1], 0) - 1) DESC LIMIT 30`,
			[]any{q.MoveSince, th.MinMove, analytics.MinPriceMoveVolumeXLM}},
		{"new_asset", `SELECT a.first_seen AS ledger, l.closed_at, NULL AS tx_hash, a.asset_key AS asset, a.type AS action, a.issuer AS from_addr,
			a.contract_id AS to_addr, (SELECT coalesce(sum(c.trades), 0) FROM candles_1m c WHERE c.base = a.asset_key OR c.counter = a.asset_key) AS trades
			FROM assets a JOIN ledgers l ON l.sequence = a.first_seen
			WHERE l.closed_at > $1 AND a.first_seen > $2::bigint + $3::bigint
			ORDER BY a.first_seen DESC LIMIT 30`,
			[]any{q.Since, q.IndexStartLedger, int64(analytics.NewAssetGraceLedgers)}},
	}
	out := map[string][]map[string]any{}
	for _, k := range kinds {
		if len(q.Kinds) > 0 && !q.Kinds[k.kind] {
			continue
		}
		rows, err := r.rows(ctx, k.sql, k.args...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k.kind, err)
		}
		out[k.kind] = rows
	}
	return out, nil
}

// FirstLedger is the oldest indexed ledger (0 when empty).
func (r *Repo) FirstLedger(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.QueryRow(ctx, `SELECT coalesce(min(sequence), 0) FROM ledgers`).Scan(&n)
	return n, err
}
