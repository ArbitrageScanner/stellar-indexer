package query

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
)

// MaxFills caps the fills loaded for one account's trading statistics.
const MaxFills = 50000

// TakerFills loads an address's verified fills since a time in execution
// order; truncated reports whether MaxFills was hit.
func (r *Repo) TakerFills(ctx context.Context, id string, since time.Time) (fills []analytics.Fill, truncated bool, err error) {
	rows, err := r.db.Query(ctx, `SELECT tx_hash, closed_at, venue, pool, sold_asset, sold_amount / 1e7::float8, bought_asset, bought_amount / 1e7::float8
		FROM trades WHERE taker = $1 AND verified AND closed_at > $2 ORDER BY ledger, tx_index, op_index, trade_index LIMIT $3`, id, since, MaxFills)
	if err != nil {
		return nil, false, err
	}
	fills, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (analytics.Fill, error) {
		var f analytics.Fill
		err := row.Scan(&f.Tx, &f.At, &f.Venue, &f.Pool, &f.Sold, &f.SoldAmt, &f.Bought, &f.BoughtAmt)
		return f, err
	})
	return fills, len(fills) == MaxFills, err
}

// AccountActivity is everything but trading in an address's indicators.
type AccountActivity struct {
	Txs              Row  `json:"txs"`
	Seen             Row  `json:"seen"`
	Protocols        Rows `json:"protocols"`
	Heatmap          Rows `json:"heatmap"`
	LargestTransfers Rows `json:"largest_transfers"`
	Counterparties   Rows `json:"counterparties"`
	Flows            Rows `json:"flows"`
}

// AccountActivity loads transaction totals, first/last seen, protocol mix,
// a weekday × hour heatmap (UTC, distinct transactions), the largest priced
// transfers, top counterparties and net flows since a time. pxSince starts
// the price window used to value transfers.
func (r *Repo) AccountActivity(ctx context.Context, id string, since, pxSince time.Time) (AccountActivity, error) {
	var a AccountActivity
	var err error
	if a.Txs, err = r.row(ctx, `SELECT count(*) AS txs, count(*) FILTER (WHERE successful) AS successful, coalesce(sum(fee_charged), 0) / 1e7::float8 AS fees_xlm,
		count(*) FILTER (WHERE is_soroban) AS soroban, count(DISTINCT contract_id) AS contracts_called
		FROM transactions WHERE source = $1 AND closed_at > $2`, id, since); err != nil {
		return a, err
	}
	if a.Seen, err = r.row(ctx, `SELECT least((SELECT min(closed_at) FROM transactions WHERE source = $1), (SELECT min(closed_at) FROM transfers WHERE from_addr = $1),
		(SELECT min(closed_at) FROM transfers WHERE to_addr = $1), (SELECT min(closed_at) FROM trades WHERE taker = $1)) AS first_seen,
		greatest((SELECT max(closed_at) FROM transactions WHERE source = $1), (SELECT max(closed_at) FROM transfers WHERE from_addr = $1),
		(SELECT max(closed_at) FROM transfers WHERE to_addr = $1), (SELECT max(closed_at) FROM trades WHERE taker = $1)) AS last_seen,
		$2::timestamptz AS window_start`, id, since); err != nil {
		return a, err
	}
	px := pxCTE("$3")
	parts := []struct {
		dst *Rows
		sql string
	}{
		{&a.Protocols, `SELECT protocol, action, count(*) AS events, count(DISTINCT tx_hash) AS txs FROM protocol_events
			WHERE user_addr = $1 AND closed_at > $2 AND verified AND $3::timestamptz IS NOT NULL GROUP BY 1, 2 ORDER BY 3 DESC`},
		{&a.Heatmap, `SELECT extract(isodow FROM t)::int AS dow, extract(hour FROM t)::int AS hour, count(DISTINCT h) AS txs FROM (
			SELECT closed_at AS t, hash AS h FROM transactions WHERE source = $1 AND closed_at > $2
			UNION ALL SELECT closed_at, tx_hash FROM transfers WHERE from_addr = $1 AND closed_at > $2
			UNION ALL SELECT closed_at, tx_hash FROM transfers WHERE to_addr = $1 AND closed_at > $2
			UNION ALL SELECT closed_at, tx_hash FROM trades WHERE taker = $1 AND closed_at > $2) x
			WHERE $3::timestamptz IS NOT NULL GROUP BY 1, 2`},
		{&a.LargestTransfers, `WITH ` + px + `
			SELECT t.closed_at, t.tx_hash, t.type, t.from_addr, t.to_addr, t.asset, t.amount / 1e7::float8 AS amount,
			       (t.amount / 1e7::float8) * px.p AS value_xlm,
			       CASE WHEN t.to_addr = $1 THEN 'in' ELSE 'out' END AS direction
			FROM transfers t JOIN px ON px.asset = t.asset
			WHERE (t.from_addr = $1 OR t.to_addr = $1) AND t.closed_at > $2 ORDER BY value_xlm DESC NULLS LAST LIMIT 10`},
		{&a.Counterparties, `WITH ` + px + `
			SELECT cp AS address, count(*) AS transfers, count(DISTINCT asset) AS assets,
			       coalesce(sum(v) FILTER (WHERE dir = 'in'), 0) AS in_xlm, coalesce(sum(v) FILTER (WHERE dir = 'out'), 0) AS out_xlm, max(closed_at) AS last
			FROM (SELECT to_addr AS cp, t.asset, 'out' AS dir, t.closed_at, (t.amount / 1e7::float8) * px.p AS v FROM transfers t LEFT JOIN px ON px.asset = t.asset
			        WHERE from_addr = $1 AND to_addr IS NOT NULL AND to_addr <> $1 AND t.closed_at > $2
			      UNION ALL
			      SELECT from_addr, t.asset, 'in', t.closed_at, (t.amount / 1e7::float8) * px.p FROM transfers t LEFT JOIN px ON px.asset = t.asset
			        WHERE to_addr = $1 AND from_addr IS NOT NULL AND from_addr <> $1 AND t.closed_at > $2) x
			GROUP BY cp ORDER BY count(*) DESC LIMIT 10`},
		{&a.Flows, `WITH ` + px + `
			SELECT f.asset, f.in_amount, f.out_amount, f.in_amount * px.p AS in_xlm, f.out_amount * px.p AS out_xlm FROM (
			  SELECT asset, coalesce(sum(amount) FILTER (WHERE to_addr = $1), 0) / 1e7::float8 AS in_amount,
			         coalesce(sum(amount) FILTER (WHERE from_addr = $1), 0) / 1e7::float8 AS out_amount
			  FROM transfers WHERE (from_addr = $1 OR to_addr = $1) AND closed_at > $2 GROUP BY asset) f
			LEFT JOIN px ON px.asset = f.asset ORDER BY greatest(f.in_amount, f.out_amount) * coalesce(px.p, 0) DESC LIMIT 15`},
	}
	for _, p := range parts {
		rows, err := r.rows(ctx, p.sql, id, since, pxSince)
		if err != nil {
			return a, err
		}
		*p.dst = rows
	}
	return a, nil
}
