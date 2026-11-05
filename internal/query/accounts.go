package query

import (
	"context"
	"time"
)

// TopAccounts lists the largest indexed XLM balances.
func (r *Repo) TopAccounts(ctx context.Context, limit int) (Rows, error) {
	return r.rows(ctx, `SELECT account_id, balance / 1e7::float8 AS xlm, num_subentries, home_domain, last_modified
		FROM accounts WHERE NOT deleted ORDER BY balance DESC LIMIT $1`, limit)
}

// Account returns account state (or a placeholder when not indexed), its
// trustlines, offers and activity since a time.
func (r *Repo) Account(ctx context.Context, id string, since time.Time) (Row, error) {
	row, err := r.row(ctx, `SELECT a.*, a.balance / 1e7::float8 AS xlm FROM accounts a WHERE account_id = $1`, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		row = Row{"account_id": id, "state": "not touched since indexing began"}
	}
	if row["trustlines"], err = r.rows(ctx, `SELECT asset, balance / 1e7::float8 AS balance, trust_limit / 1e7::float8 AS trust_limit, flags, last_modified
		FROM trustlines WHERE account_id = $1 ORDER BY balance DESC`, id); err != nil {
		return nil, err
	}
	if row["offers"], err = r.rows(ctx, `SELECT offer_id::text, selling, buying, amount / 1e7::float8 AS amount, price, passive, last_modified
		FROM offers WHERE seller = $1 ORDER BY offer_id DESC LIMIT 200`, id); err != nil {
		return nil, err
	}
	row["activity_24h"], err = r.row(ctx, `SELECT
		(SELECT count(*) FROM transactions WHERE source = $1 AND closed_at > $2) AS txs,
		(SELECT count(*) FROM transfers WHERE (from_addr = $1 OR to_addr = $1) AND closed_at > $2) AS transfers,
		(SELECT count(*) FROM trades WHERE taker = $1 AND closed_at > $2) AS trades,
		(SELECT count(*) FROM protocol_events WHERE user_addr = $1 AND closed_at > $2) AS protocol_events`, id, since)
	return row, err
}

// AccountOperations lists operations sourced by an account below before (TOID).
func (r *Repo) AccountOperations(ctx context.Context, id string, before int64, limit int) (Rows, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	return r.rows(ctx, `SELECT id::text, tx_hash, ledger, closed_at, type, successful, details
		FROM operations WHERE source = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, id, before, limit)
}

// AccountTransfers lists transfers in and out of an address.
func (r *Repo) AccountTransfers(ctx context.Context, id string, limit int) (Rows, error) {
	return r.rows(ctx, `SELECT * FROM (
		  (SELECT ledger, closed_at, tx_hash, type, from_addr, to_addr, asset, amount::text FROM transfers WHERE from_addr = $1 ORDER BY ledger DESC LIMIT $2)
		  UNION ALL
		  (SELECT ledger, closed_at, tx_hash, type, from_addr, to_addr, asset, amount::text FROM transfers WHERE to_addr = $1 ORDER BY ledger DESC LIMIT $2)
		) t ORDER BY ledger DESC LIMIT $2`, id, limit)
}

// AccountTrades lists fills taken by an address.
func (r *Repo) AccountTrades(ctx context.Context, id string, limit int) (Rows, error) {
	return r.rows(ctx, `SELECT closed_at, tx_hash, venue, pool, sold_asset, sold_amount::text, bought_asset, bought_amount::text,
		price, base, counter, verified FROM trades WHERE taker = $1 ORDER BY closed_at DESC LIMIT $2`, id, limit)
}

// AccountEvents lists protocol events where the address is the user.
func (r *Repo) AccountEvents(ctx context.Context, id string, limit int) (Rows, error) {
	return r.rows(ctx, `SELECT ledger, closed_at, tx_hash, protocol, contract_id, action, asset, amount0::text, amount1::text, verified
		FROM protocol_events WHERE user_addr = $1 ORDER BY closed_at DESC LIMIT $2`, id, limit)
}
