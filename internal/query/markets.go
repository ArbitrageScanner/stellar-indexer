package query

import (
	"context"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
	"github.com/jackc/pgx/v5"
)

// Market queries use verified fills only (classic venues plus allowlisted
// Soroban pools), the same population that feeds candles.

// Assets ranks assets by verified trades and transfers since a time.
func (r *Repo) Assets(ctx context.Context, since time.Time, limit int) (Rows, error) {
	return r.rows(ctx, `
		WITH legs AS (
		  SELECT base AS asset, base_volume AS vol FROM trades WHERE verified AND closed_at > $1
		  UNION ALL
		  SELECT counter, counter_volume FROM trades WHERE verified AND closed_at > $1
		), vol AS (SELECT asset, count(*) AS trades, sum(vol) AS volume FROM legs GROUP BY asset),
		tr AS (SELECT asset, count(*) AS transfers FROM transfers WHERE ledger >= `+ledgerSince("$1")+` GROUP BY asset)
		SELECT a.asset_key, a.type, a.code, a.issuer, a.contract_id, coalesce(v.trades, 0) AS trades,
		       coalesce(v.volume, 0) AS volume, coalesce(tr.transfers, 0) AS transfers,
		       (SELECT count(*) FROM trustlines t WHERE t.asset = a.asset_key) AS holders_indexed
		FROM assets a LEFT JOIN vol v ON v.asset = a.asset_key LEFT JOIN tr ON tr.asset = a.asset_key
		WHERE v.trades IS NOT NULL OR tr.transfers IS NOT NULL
		ORDER BY coalesce(v.trades, 0) + coalesce(tr.transfers, 0) DESC LIMIT $2`, since, limit)
}

// Asset returns an asset with stats since a time and its top pairs, or nil.
func (r *Repo) Asset(ctx context.Context, key string, since time.Time) (Row, error) {
	row, err := r.row(ctx, `SELECT * FROM assets WHERE asset_key = $1 OR contract_id = $1 LIMIT 1`, key)
	if row == nil || err != nil {
		return row, err
	}
	key = row["asset_key"].(string)
	row["stats_24h"], err = r.row(ctx, `SELECT
		(SELECT count(*) FROM transfers WHERE asset = $1 AND closed_at > $2) AS transfers,
		(SELECT coalesce(sum(amount), 0)::text FROM transfers WHERE asset = $1 AND type = 'transfer' AND closed_at > $2) AS transfer_amount,
		(SELECT coalesce(sum(amount), 0)::text FROM transfers WHERE asset = $1 AND type = 'mint' AND closed_at > $2) AS minted,
		(SELECT coalesce(sum(amount), 0)::text FROM transfers WHERE asset = $1 AND type = 'burn' AND closed_at > $2) AS burned,
		(SELECT count(*) FROM trades WHERE verified AND (base = $1 OR counter = $1) AND closed_at > $2) AS trades,
		(SELECT count(*) FROM trustlines WHERE asset = $1) AS trustlines_indexed`, key, since)
	if err != nil {
		return nil, err
	}
	row["top_pairs"], err = r.rows(ctx, `SELECT base, counter, count(*) AS trades, sum(base_volume) AS base_volume
		FROM trades WHERE verified AND (base = $1 OR counter = $1) AND closed_at > $2
		GROUP BY 1, 2 ORDER BY 3 DESC LIMIT 10`, key, since)
	return row, err
}

// AssetHolders lists the largest indexed holders (accounts for native).
func (r *Repo) AssetHolders(ctx context.Context, key string, limit int) (Rows, error) {
	if key == "native" {
		return r.rows(ctx, `SELECT account_id, balance / 1e7::float8 AS balance, last_modified
			FROM accounts WHERE NOT deleted ORDER BY balance DESC LIMIT $1`, limit)
	}
	return r.rows(ctx, `SELECT account_id, balance / 1e7::float8 AS balance, trust_limit / 1e7::float8 AS trust_limit, flags, last_modified
		FROM trustlines WHERE asset = (SELECT asset_key FROM assets WHERE asset_key = $1 OR contract_id = $1 LIMIT 1)
		ORDER BY balance DESC LIMIT $2`, key, limit)
}

// Pairs ranks canonical pairs by verified fills since a time.
func (r *Repo) Pairs(ctx context.Context, since time.Time, limit int) (Rows, error) {
	return r.rows(ctx, `SELECT base, counter, count(*) AS trades, sum(base_volume) AS base_volume,
		sum(counter_volume) AS counter_volume, sum(counter_volume) / nullif(sum(base_volume), 0) AS vwap,
		(array_agg(price ORDER BY ledger DESC, tx_index DESC, op_index DESC, trade_index DESC))[1] AS last_price,
		array_agg(DISTINCT venue) AS venues
		FROM trades WHERE verified AND closed_at > $1 AND base <> ''
		GROUP BY base, counter ORDER BY trades DESC LIMIT $2`, since, limit)
}

// TradeFilter selects fills. Base/Counter must be canonical (base < counter).
type TradeFilter struct {
	Before             string // ledger.tx_index.op_index.trade_index
	Base, Counter      string
	Venue, Taker, Pool string
	Verified           *bool
	Limit              int
}

// Trades is the fill feed, newest first (all fills, each with its verified flag).
func (r *Repo) Trades(ctx context.Context, f TradeFilter) (Rows, error) {
	w := &where{}
	if err := w.position(f.Before, "ledger", "tx_index", "op_index", "trade_index"); err != nil {
		return nil, err
	}
	if f.Base != "" {
		w.add("base = ?", f.Base)
		w.add("counter = ?", f.Counter)
	}
	if f.Venue != "" {
		w.add("venue = ?", f.Venue)
	}
	if f.Taker != "" {
		w.add("taker = ?", f.Taker)
	}
	if f.Pool != "" {
		w.add("pool = ?", f.Pool)
	}
	if f.Verified != nil {
		w.add("verified = ?", *f.Verified)
	}
	lim := w.arg(f.Limit)
	return r.rows(ctx, `SELECT ledger, tx_index, op_index, trade_index, closed_at, tx_hash, venue, pool, maker, taker,
		sold_asset, sold_amount::text, bought_asset, bought_amount::text, base, counter, price, base_volume, counter_volume, verified
		FROM trades WHERE `+w.sql()+`
		ORDER BY ledger DESC, tx_index DESC, op_index DESC, trade_index DESC LIMIT `+lim, w.args...)
}

// Ticker returns a canonical pair's last verified price and stats since a time.
func (r *Repo) Ticker(ctx context.Context, base, counter string, since time.Time) (analytics.Ticker, error) {
	t := analytics.Ticker{Base: base, Counter: counter}
	err := r.db.QueryRow(ctx, `SELECT
		(SELECT price FROM trades WHERE verified AND base = $1 AND counter = $2 ORDER BY ledger DESC, tx_index DESC, op_index DESC, trade_index DESC LIMIT 1),
		(SELECT max(closed_at) FROM trades WHERE verified AND base = $1 AND counter = $2),
		(SELECT open FROM candles_1m WHERE base = $1 AND counter = $2 AND bucket >= $3 ORDER BY bucket LIMIT 1),
		(SELECT max(high) FROM candles_1m WHERE base = $1 AND counter = $2 AND bucket >= $3),
		(SELECT min(low) FROM candles_1m WHERE base = $1 AND counter = $2 AND bucket >= $3),
		(SELECT sum(base_volume) FROM candles_1m WHERE base = $1 AND counter = $2 AND bucket >= $3),
		(SELECT sum(counter_volume) FROM candles_1m WHERE base = $1 AND counter = $2 AND bucket >= $3),
		(SELECT sum(trades) FROM candles_1m WHERE base = $1 AND counter = $2 AND bucket >= $3)`, base, counter, since).
		Scan(&t.Last, &t.LastTradeAt, &t.Open24h, &t.High24h, &t.Low24h, &t.BaseVolume24h, &t.CounterVolume24h, &t.Trades24h)
	return t, err
}

// Candles rolls 1-minute candles of a canonical pair up to step (a Postgres
// interval), newest first.
func (r *Repo) Candles(ctx context.Context, base, counter, step string, from, to time.Time, limit int) ([]analytics.Candle, error) {
	rows, err := r.db.Query(ctx, `SELECT date_bin($3::interval, bucket, '2000-01-01') AS t,
		(array_agg(open ORDER BY bucket))[1], max(high), min(low), (array_agg(close ORDER BY bucket DESC))[1],
		sum(base_volume), sum(counter_volume), sum(trades)
		FROM candles_1m WHERE base = $1 AND counter = $2 AND bucket >= $4 AND bucket < $5
		GROUP BY 1 ORDER BY 1 DESC LIMIT $6`, base, counter, step, from, to, limit)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (analytics.Candle, error) {
		var c analytics.Candle
		err := row.Scan(&c.T, &c.Open, &c.High, &c.Low, &c.Close, &c.BaseVolume, &c.CounterVolume, &c.Trades)
		return c, err
	})
	if out == nil {
		out = []analytics.Candle{}
	}
	return out, err
}

// Pools lists classic AMM pools by participants.
func (r *Repo) Pools(ctx context.Context, limit int) (Rows, error) {
	return r.rows(ctx, `SELECT pool_id, asset_a, asset_b, reserve_a / 1e7::float8 AS reserve_a, reserve_b / 1e7::float8 AS reserve_b,
		CASE WHEN reserve_a > 0 THEN reserve_b::float8 / reserve_a END AS price_b_per_a, total_shares, trustline_count, fee_bp, last_modified
		FROM liquidity_pools WHERE NOT deleted ORDER BY trustline_count DESC LIMIT $1`, limit)
}
