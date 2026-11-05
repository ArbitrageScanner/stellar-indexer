package query

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
)

// TagSignals loads the facts behind an address's automatic tags, with
// 24h/7d windows ending at anchor. Arbitrage and protocol signals use
// verified fills and events only.
func (r *Repo) TagSignals(ctx context.Context, id string, anchor time.Time) (analytics.Signals, error) {
	var s analytics.Signals
	var created, tip *int64
	var start *time.Time
	err := r.db.QueryRow(ctx, `SELECT
		(SELECT balance / 1e7::float8 FROM accounts WHERE account_id = $1 AND NOT deleted),
		(SELECT seq_num >> 32 FROM accounts WHERE account_id = $1),
		(SELECT count(*) FROM offers WHERE seller = $1),
		(SELECT count(*) FROM offers o1 WHERE seller = $1 AND EXISTS (
		   SELECT 1 FROM offers o2 WHERE o2.seller = $1 AND o2.selling = o1.buying AND o2.buying = o1.selling)),
		(SELECT count(*) FROM transactions WHERE source = $1 AND closed_at > $2::timestamptz - interval '24 hours'),
		(SELECT count(*) FROM transactions WHERE source = $1 AND NOT successful AND closed_at > $2::timestamptz - interval '24 hours'),
		(SELECT count(*) FROM trades WHERE taker = $1 AND closed_at > $2::timestamptz - interval '24 hours'),
		(SELECT count(*) FROM operations WHERE source = $1 AND closed_at > $2::timestamptz - interval '24 hours'
		   AND type IN ('manage_sell_offer', 'manage_buy_offer', 'create_passive_sell_offer')),
		(SELECT count(*) FROM (SELECT tx_hash FROM trades WHERE taker = $1 AND verified AND closed_at > $2::timestamptz - interval '7 days' GROUP BY tx_hash
		   HAVING count(*) >= 2 AND (array_agg(sold_asset ORDER BY op_index, trade_index))[1] = (array_agg(bought_asset ORDER BY op_index DESC, trade_index DESC))[1]) c),
		(SELECT count(*) FROM protocol_events WHERE user_addr = $1 AND verified AND action = 'flash_loan'),
		coalesce((SELECT array_agg(DISTINCT protocol) FROM protocol_events WHERE user_addr = $1 AND verified), '{}'),
		(SELECT count(*) FROM assets WHERE issuer = $1),
		coalesce((SELECT array_agg(code) FROM (SELECT code FROM assets WHERE issuer = $1 AND code IS NOT NULL ORDER BY first_seen LIMIT 4) a), '{}'),
		coalesce((SELECT asset_key FROM assets WHERE contract_id = $1 LIMIT 1), ''),
		coalesce((SELECT protocol FROM protocol_events WHERE contract_id = $1 AND verified LIMIT 1), ''),
		coalesce((SELECT array_agg(DISTINCT action) FROM (SELECT action FROM protocol_events WHERE contract_id = $1 AND verified LIMIT 1000) a), '{}'),
		(SELECT count(*) FROM trustlines WHERE account_id = $1 AND asset LIKE 'pool:%' AND balance > 0),
		(SELECT count(*) FROM protocol_events WHERE user_addr = $1 AND verified AND action IN ('deposit', 'deposit_liquidity')),
		(SELECT count(*) FROM protocol_events WHERE user_addr = $1 AND verified AND protocol = 'blend' AND action = 'borrow'),
		greatest((SELECT max(closed_at) FROM transactions WHERE source = $1), (SELECT max(closed_at) FROM transfers WHERE from_addr = $1),
		         (SELECT max(closed_at) FROM transfers WHERE to_addr = $1)),
		(SELECT max(sequence) FROM ledgers), (SELECT min(closed_at) FROM ledgers)`, id, anchor).Scan(
		&s.XLM, &created, &s.Offers, &s.TwoSidedOffer, &s.Txs24h, &s.Failed24h, &s.Fills24h, &s.OfferOps24h, &s.Cycles7d,
		&s.FlashLoans, &s.Protocols, &s.IssuedCount, &s.Issued, &s.Token, &s.ProtoContract, &s.ProtoActions,
		&s.LPShares, &s.LPDeposits, &s.Borrows, &s.LastActive, &tip, &start)
	if created != nil {
		s.CreatedLedger = *created
	}
	if tip != nil {
		s.Tip = *tip
	}
	if start != nil {
		s.IndexStart = *start
	}
	return s, err
}

// UserTags returns an address's user labels as tags.
func (r *Repo) UserTags(ctx context.Context, id string) ([]analytics.Tag, error) {
	rows, err := r.db.Query(ctx, `SELECT tag, note FROM labels WHERE address = $1 ORDER BY created_at`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (analytics.Tag, error) {
		var t analytics.Tag
		err := row.Scan(&t.ID, &t.Note)
		t.Label, t.Group, t.Kind, t.Reason = t.ID, "user", "user", t.Note
		return t, err
	})
}
