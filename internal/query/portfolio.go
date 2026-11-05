package query

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
)

// PortfolioInput loads an address's raw positions: XLM, trustlines (with
// pool reserves for pool shares), net transfer flows, and Blend positions
// (only verified Blend pools count).
func (r *Repo) PortfolioInput(ctx context.Context, id string) (analytics.PortfolioInput, error) {
	in := analytics.PortfolioInput{Account: id, NetFlows: map[string]float64{}}
	var bal, seq int64
	err := r.db.QueryRow(ctx, `SELECT balance, seq_num FROM accounts WHERE account_id = $1 AND NOT deleted`, id).Scan(&bal, &seq)
	switch {
	case err == nil:
		in.NativeKnown, in.Native, in.SeqNum = true, float64(bal)/1e7, seq
	case !errors.Is(err, pgx.ErrNoRows):
		return in, err
	}
	if err := r.db.QueryRow(ctx, `SELECT coalesce(min(sequence), 0) FROM ledgers`).Scan(&in.SinceLedger); err != nil {
		return in, err
	}

	rows, err := r.db.Query(ctx, `SELECT t.asset, t.balance / 1e7::float8, lp.asset_a, lp.asset_b,
		CASE WHEN lp.total_shares > 0 THEN t.balance::float8 / lp.total_shares END, lp.reserve_a / 1e7::float8, lp.reserve_b / 1e7::float8
		FROM trustlines t LEFT JOIN liquidity_pools lp ON t.asset = 'pool:' || lp.pool_id
		WHERE t.account_id = $1 AND t.balance > 0`, id)
	if err != nil {
		return in, err
	}
	in.Trustlines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (analytics.TrustlineHolding, error) {
		var h analytics.TrustlineHolding
		var a, b *string
		var frac, ra, rb *float64
		if err := row.Scan(&h.Asset, &h.Amount, &a, &b, &frac, &ra, &rb); err != nil {
			return h, err
		}
		if a != nil && b != nil && frac != nil && ra != nil && rb != nil {
			h.Pool = &analytics.PoolShare{AssetA: *a, AssetB: *b, Fraction: *frac, ReserveA: *ra, ReserveB: *rb}
		}
		return h, nil
	})
	if err != nil {
		return in, err
	}

	rows, err = r.db.Query(ctx, `SELECT asset, (sum(CASE WHEN to_addr = $1 THEN amount ELSE 0 END) - sum(CASE WHEN from_addr = $1 THEN amount ELSE 0 END)) / 1e7::float8
		FROM transfers WHERE from_addr = $1 OR to_addr = $1 GROUP BY asset`, id)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var a string
		var v float64
		if err := rows.Scan(&a, &v); err != nil {
			rows.Close()
			return in, err
		}
		in.NetFlows[a] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	rows, err = r.db.Query(ctx, `SELECT contract_id, asset,
		coalesce((sum(amount0) FILTER (WHERE action IN ('supply', 'supply_collateral')) - coalesce(sum(amount0) FILTER (WHERE action IN ('withdraw', 'withdraw_collateral')), 0)) / 1e7::float8, 0),
		coalesce((sum(amount0) FILTER (WHERE action = 'borrow') - coalesce(sum(amount0) FILTER (WHERE action = 'repay'), 0)) / 1e7::float8, 0)
		FROM protocol_events WHERE protocol = 'blend' AND verified AND user_addr = $1 AND asset IS NOT NULL GROUP BY 1, 2`, id)
	if err != nil {
		return in, err
	}
	in.Blend, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (analytics.BlendPosition, error) {
		var b analytics.BlendPosition
		err := row.Scan(&b.Contract, &b.Asset, &b.Supplied, &b.Borrowed)
		return b, err
	})
	return in, err
}

// IndexStart is the close time of the oldest indexed ledger (zero when empty).
func (r *Repo) IndexStart(ctx context.Context) (time.Time, error) {
	var t *time.Time
	if err := r.db.QueryRow(ctx, `SELECT min(closed_at) FROM ledgers`).Scan(&t); err != nil || t == nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// HourlyFlows returns an address's net transfer flow per asset per hour since a time.
func (r *Repo) HourlyFlows(ctx context.Context, id string, since time.Time) (analytics.HourlySeries, error) {
	out := analytics.HourlySeries{}
	rows, err := r.db.Query(ctx, `SELECT asset, date_trunc('hour', closed_at),
		(sum(CASE WHEN to_addr = $1 THEN amount ELSE 0 END) - sum(CASE WHEN from_addr = $1 THEN amount ELSE 0 END)) / 1e7::float8
		FROM transfers WHERE (from_addr = $1 OR to_addr = $1) AND closed_at >= $2 GROUP BY 1, 2`, id, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		var h time.Time
		var v float64
		if err := rows.Scan(&a, &h, &v); err != nil {
			return nil, err
		}
		if out[a] == nil {
			out[a] = map[time.Time]float64{}
		}
		out[a][h.UTC()] += v
	}
	return out, rows.Err()
}
