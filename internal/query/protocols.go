package query

import (
	"context"
	"time"
)

// Protocol statistics count verified events (contracts whose code is
// allowlisted for the protocol) unless includeUnverified; unverified counts
// are always reported alongside.

// ProtocolOverview is per-protocol activity plus fills per venue.
type ProtocolOverview struct {
	Protocols   Rows `json:"protocols"`
	TradeVenues Rows `json:"trade_venues"`
}

// Protocols summarises protocol events and venues since a time.
func (r *Repo) Protocols(ctx context.Context, since time.Time, includeUnverified bool) (ProtocolOverview, error) {
	var o ProtocolOverview
	var err error
	o.Protocols, err = r.rows(ctx, `SELECT protocol,
		count(*) FILTER (WHERE verified OR $2) AS events,
		count(*) FILTER (WHERE NOT verified) AS unverified_events,
		count(DISTINCT user_addr) FILTER (WHERE verified OR $2) AS users,
		count(DISTINCT contract_id) FILTER (WHERE verified OR $2) AS contracts,
		count(DISTINCT tx_hash) FILTER (WHERE verified OR $2) AS txs,
		coalesce((SELECT jsonb_object_agg(action, n) FROM (
		   SELECT action, count(*) AS n FROM protocol_events a
		   WHERE a.protocol = e.protocol AND a.closed_at > $1 AND (a.verified OR $2) GROUP BY action) x), '{}'::jsonb) AS actions
		FROM protocol_events e WHERE closed_at > $1
		GROUP BY protocol ORDER BY events DESC, unverified_events DESC`, since, includeUnverified)
	if err != nil {
		return o, err
	}
	o.TradeVenues, err = r.rows(ctx, `SELECT venue, count(*) FILTER (WHERE verified OR $2) AS trades,
		count(*) FILTER (WHERE NOT verified) AS unverified_trades,
		count(DISTINCT pool) FILTER (WHERE verified OR $2) AS pools, count(DISTINCT taker) FILTER (WHERE verified OR $2) AS takers
		FROM trades WHERE closed_at > $1 GROUP BY venue ORDER BY trades DESC`, since, includeUnverified)
	return o, err
}

// ProtocolSummary is one protocol's activity breakdown since a time.
type ProtocolSummary struct {
	Unverified int64 `json:"unverified_events"`
	Actions    Rows  `json:"actions"`
	ByAsset    Rows  `json:"by_asset"`
	Contracts  Rows  `json:"contracts"`
	Users      Rows  `json:"users"`
	Pools      Rows  `json:"pools"`
	Timeline   Rows  `json:"timeline"`
}

// ProtocolSummary loads actions, per-asset amounts, contracts, users, pools
// (fills on the protocol's venue) and an hourly timeline.
func (r *Repo) ProtocolSummary(ctx context.Context, name string, since time.Time, includeUnverified bool) (ProtocolSummary, error) {
	var s ProtocolSummary
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM protocol_events WHERE protocol = $1 AND closed_at > $2 AND NOT verified`, name, since).Scan(&s.Unverified); err != nil {
		return s, err
	}
	const scope = `protocol = $1 AND closed_at > $2 AND (verified OR $3)`
	parts := []struct {
		dst *Rows
		sql string
	}{
		{&s.Actions, `SELECT action, count(*) AS events, count(DISTINCT user_addr) AS users, count(DISTINCT tx_hash) AS txs
			FROM protocol_events WHERE ` + scope + ` GROUP BY 1 ORDER BY 2 DESC`},
		{&s.ByAsset, `SELECT action, asset, count(*) AS events, count(DISTINCT user_addr) AS users,
			sum(amount0) / 1e7::float8 AS amount0, sum(amount1) / 1e7::float8 AS amount1
			FROM protocol_events WHERE ` + scope + ` AND asset IS NOT NULL GROUP BY 1, 2 ORDER BY 3 DESC LIMIT 200`},
		{&s.Contracts, `SELECT contract_id, count(*) AS events, count(DISTINCT user_addr) AS users, array_agg(DISTINCT action) AS actions,
			max(closed_at) AS last_seen, bool_and(verified) AS verified
			FROM protocol_events WHERE ` + scope + ` GROUP BY 1 ORDER BY 2 DESC LIMIT 50`},
		{&s.Users, `SELECT user_addr, count(*) AS events, count(DISTINCT tx_hash) AS txs, array_agg(DISTINCT action) AS actions
			FROM protocol_events WHERE ` + scope + ` AND user_addr IS NOT NULL GROUP BY 1 ORDER BY 2 DESC LIMIT 20`},
		{&s.Pools, `SELECT pool, base, counter, count(*) AS trades, sum(base_volume) AS base_volume, sum(counter_volume) AS counter_volume,
			(array_agg(price ORDER BY ledger DESC, tx_index DESC, op_index DESC, trade_index DESC))[1] AS last_price,
			count(DISTINCT taker) AS takers, bool_and(verified) AS verified
			FROM trades WHERE venue = $1 AND closed_at > $2 AND (verified OR $3) GROUP BY 1, 2, 3 ORDER BY 4 DESC LIMIT 50`},
		{&s.Timeline, `SELECT date_trunc('hour', closed_at) AS t, count(*) AS events, count(DISTINCT user_addr) AS users
			FROM protocol_events WHERE ` + scope + ` GROUP BY 1 ORDER BY 1`},
	}
	for _, p := range parts {
		rows, err := r.rows(ctx, p.sql, name, since, includeUnverified)
		if err != nil {
			return s, err
		}
		*p.dst = rows
	}
	return s, nil
}

// ProtocolEventFilter selects decoded protocol events.
type ProtocolEventFilter struct {
	Protocol                      string
	Before                        string // ledger.tx_index.op_index.event_index
	Action, User, Contract, Asset string
	IncludeUnverified             bool
	Limit                         int
}

// ProtocolEvents is a protocol's event feed, newest first.
func (r *Repo) ProtocolEvents(ctx context.Context, f ProtocolEventFilter) (Rows, error) {
	w := &where{}
	w.add("protocol = ?", f.Protocol)
	if err := w.position(f.Before, "ledger", "tx_index", "op_index", "event_index"); err != nil {
		return nil, err
	}
	if !f.IncludeUnverified {
		w.raw("verified")
	}
	if f.Action != "" {
		w.add("action = ?", f.Action)
	}
	if f.User != "" {
		w.add("user_addr = ?", f.User)
	}
	if f.Contract != "" {
		w.add("contract_id = ?", f.Contract)
	}
	if f.Asset != "" {
		w.add("asset = ?", f.Asset)
	}
	lim := w.arg(f.Limit)
	return r.rows(ctx, `SELECT ledger, tx_index, op_index, event_index, closed_at, tx_hash, contract_id, action, user_addr,
		asset, amount0::text, amount1::text, data, verified
		FROM protocol_events WHERE `+w.sql()+` ORDER BY ledger DESC, tx_index DESC, op_index DESC, event_index DESC LIMIT `+lim, w.args...)
}
