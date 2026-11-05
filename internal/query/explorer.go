package query

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TxFilter selects transactions; zero values mean "any".
type TxFilter struct {
	Before                     int64 // tx id (TOID) keyset cursor
	Source, Contract, Function string
	Successful, Soroban        *bool
	Ledger                     *int64
	Limit                      int
}

// Transactions is the filtered transaction feed, newest first.
func (r *Repo) Transactions(ctx context.Context, f TxFilter) (Rows, error) {
	w := &where{}
	if f.Before > 0 {
		w.add("id < ?", f.Before)
	}
	if f.Source != "" {
		w.add("source = ?", f.Source)
	}
	if f.Contract != "" {
		w.add("contract_id = ?", f.Contract)
	}
	if f.Function != "" {
		w.add("function = ?", f.Function)
	}
	if f.Successful != nil {
		w.add("successful = ?", *f.Successful)
	}
	if f.Soroban != nil {
		w.add("is_soroban = ?", *f.Soroban)
	}
	if f.Ledger != nil {
		w.add("ledger = ?", *f.Ledger)
	}
	lim := w.arg(f.Limit)
	return r.rows(ctx, `SELECT id::text, hash, ledger, tx_index, closed_at, source, successful, result_code,
		op_count, fee_charged, is_soroban, contract_id, function, memo_type, memo
		FROM transactions WHERE `+w.sql()+` ORDER BY id DESC LIMIT `+lim, w.args...)
}

// Transaction returns a transaction with its operations, transfers, trades
// and events, or nil.
func (r *Repo) Transaction(ctx context.Context, hash string) (Row, error) {
	row, err := r.row(ctx, `SELECT * FROM transactions WHERE hash = $1`, hash)
	if row == nil || err != nil {
		return row, err
	}
	parts := []struct{ key, sql string }{
		{"operations", `SELECT id::text, op_index, type, source, successful, result_code, details FROM operations WHERE tx_hash = $1 ORDER BY op_index`},
		{"transfers", `SELECT transfer_index, op_index, type, from_addr, to_addr, asset, amount::text FROM transfers WHERE tx_hash = $1 ORDER BY transfer_index`},
		{"trades", `SELECT op_index, trade_index, venue, pool, maker, taker, sold_asset, sold_amount::text, bought_asset, bought_amount::text,
			base, counter, price, verified FROM trades WHERE tx_hash = $1 ORDER BY op_index, trade_index`},
		{"contract_events", `SELECT op_index, event_index, contract_id, protocol, verified, topics, data FROM contract_events WHERE tx_hash = $1 ORDER BY op_index, event_index`},
		{"protocol_events", `SELECT op_index, event_index, protocol, contract_id, action, user_addr, asset, amount0::text, amount1::text, verified
			FROM protocol_events WHERE tx_hash = $1 ORDER BY op_index, event_index`},
	}
	for _, p := range parts {
		if row[p.key], err = r.rows(ctx, p.sql, hash); err != nil {
			return nil, err
		}
	}
	return row, nil
}

// OpFilter selects operations.
type OpFilter struct {
	Before       int64 // operation id (TOID)
	Type, Source string
	Successful   *bool
	Limit        int
}

// Operations is the typed operation feed, newest first.
func (r *Repo) Operations(ctx context.Context, f OpFilter) (Rows, error) {
	w := &where{}
	if f.Before > 0 {
		w.add("id < ?", f.Before)
	}
	if f.Type != "" {
		w.add("type = ?", f.Type)
	}
	if f.Source != "" {
		w.add("source = ?", f.Source)
	}
	if f.Successful != nil {
		w.add("successful = ?", *f.Successful)
	}
	lim := w.arg(f.Limit)
	return r.rows(ctx, `SELECT id::text, tx_hash, ledger, op_index, closed_at, type, source, successful, result_code, details
		FROM operations WHERE `+w.sql()+` ORDER BY id DESC LIMIT `+lim, w.args...)
}

// TransferFilter selects token transfers.
type TransferFilter struct {
	Before      string // ledger.tx_index.transfer_index
	Asset, Type string
	Limit       int
}

// Transfers is the token-flow feed, newest first.
func (r *Repo) Transfers(ctx context.Context, f TransferFilter) (Rows, error) {
	w := &where{}
	if err := w.position(f.Before, "ledger", "tx_index", "transfer_index"); err != nil {
		return nil, err
	}
	if f.Asset != "" {
		w.add("asset = ?", f.Asset)
	}
	if f.Type != "" {
		w.add("type = ?", f.Type)
	}
	lim := w.arg(f.Limit)
	return r.rows(ctx, `SELECT ledger, tx_index, transfer_index, op_index, closed_at, tx_hash, type, from_addr, to_addr, asset, amount::text
		FROM transfers WHERE `+w.sql()+` ORDER BY ledger DESC, tx_index DESC, transfer_index DESC LIMIT `+lim, w.args...)
}

// EventFilter selects contract events. Protocol "none" = no decoder matched.
type EventFilter struct {
	Before                    string // ledger.tx_index.op_index.event_index
	Contract, Topic, Protocol string
	Limit                     int
}

// Events is the contract-event feed, newest first.
func (r *Repo) Events(ctx context.Context, f EventFilter) (Rows, error) {
	w := &where{}
	if err := w.position(f.Before, "ledger", "tx_index", "op_index", "event_index"); err != nil {
		return nil, err
	}
	if f.Contract != "" {
		w.add("contract_id = ?", f.Contract)
	}
	if f.Topic != "" {
		w.add("topic0 = ?", f.Topic)
	}
	switch f.Protocol {
	case "":
	case "none":
		w.raw("protocol IS NULL")
	default:
		w.add("protocol = ?", f.Protocol)
	}
	lim := w.arg(f.Limit)
	return r.rows(ctx, `SELECT ledger, tx_index, op_index, event_index, closed_at, tx_hash, contract_id, topic0, topics, data, protocol, verified, successful
		FROM contract_events WHERE `+w.sql()+` ORDER BY ledger DESC, tx_index DESC, op_index DESC, event_index DESC LIMIT `+lim, w.args...)
}

// Contracts ranks contracts by direct calls plus emitted events since a time.
func (r *Repo) Contracts(ctx context.Context, since time.Time, limit int) (Rows, error) {
	return r.rows(ctx, `
		WITH ev AS (
		  SELECT contract_id, max(protocol) AS protocol, count(*) AS events, count(DISTINCT tx_hash) AS txs,
		         max(closed_at) AS last_seen, (array_agg(DISTINCT topic0))[1:6] AS topics, bool_or(verified) AS verified
		  FROM contract_events WHERE closed_at > $1 GROUP BY contract_id
		), calls AS (
		  SELECT contract_id, count(*) AS calls, count(*) FILTER (WHERE NOT successful) AS failed_calls,
		         count(DISTINCT source) AS callers, (array_agg(DISTINCT function))[1:6] AS functions
		  FROM transactions WHERE contract_id IS NOT NULL AND ledger >= `+ledgerSince("$1")+` GROUP BY contract_id
		)
		SELECT coalesce(ev.contract_id, calls.contract_id) AS contract_id, ev.protocol, coalesce(ev.verified, false) AS verified,
		       coalesce(ev.events, 0) AS events, coalesce(ev.txs, 0) AS event_txs, ev.last_seen, ev.topics,
		       coalesce(calls.calls, 0) AS calls, coalesce(calls.failed_calls, 0) AS failed_calls,
		       coalesce(calls.callers, 0) AS callers, calls.functions,
		       a.asset_key AS token
		FROM ev FULL JOIN calls ON calls.contract_id = ev.contract_id
		LEFT JOIN assets a ON a.contract_id = coalesce(ev.contract_id, calls.contract_id)
		ORDER BY coalesce(ev.events, 0) + coalesce(calls.calls, 0) DESC LIMIT $2`, since, limit)
}

// Contract summarises one contract: activity, executable and per-protocol
// verification of its events.
func (r *Repo) Contract(ctx context.Context, id string) (Row, error) {
	row, err := r.row(ctx, `SELECT $1::text AS contract_id,
		(SELECT max(protocol) FROM contract_events WHERE contract_id = $1) AS protocol,
		(SELECT count(*) FROM contract_events WHERE contract_id = $1) AS events,
		(SELECT count(*) FROM transactions WHERE contract_id = $1) AS calls,
		(SELECT count(DISTINCT source) FROM transactions WHERE contract_id = $1) AS callers,
		(SELECT min(closed_at) FROM contract_events WHERE contract_id = $1) AS first_seen,
		(SELECT max(closed_at) FROM contract_events WHERE contract_id = $1) AS last_seen,
		(SELECT asset_key FROM assets WHERE contract_id = $1 LIMIT 1) AS token`, id)
	if err != nil {
		return nil, err
	}
	if row["code"], err = r.row(ctx, `SELECT executable, wasm_hash, ledger, checked_at FROM contract_code WHERE contract_id = $1`, id); err != nil {
		return nil, err
	}
	parts := []struct{ key, sql string }{
		{"topics", `SELECT topic0, count(*) AS events FROM contract_events WHERE contract_id = $1 GROUP BY 1 ORDER BY 2 DESC LIMIT 20`},
		{"functions", `SELECT function, count(*) AS calls, count(*) FILTER (WHERE NOT successful) AS failed
			FROM transactions WHERE contract_id = $1 GROUP BY 1 ORDER BY 2 DESC LIMIT 20`},
		{"protocol_actions", `SELECT action, count(*) AS events, count(*) FILTER (WHERE verified) AS verified
			FROM protocol_events WHERE contract_id = $1 GROUP BY 1 ORDER BY 2 DESC`},
		{"verification", `SELECT protocol, count(*) FILTER (WHERE verified) AS verified_events, count(*) FILTER (WHERE NOT verified) AS unverified_events
			FROM protocol_events WHERE contract_id = $1 GROUP BY 1 ORDER BY 1`},
	}
	for _, p := range parts {
		if row[p.key], err = r.rows(ctx, p.sql, id); err != nil {
			return nil, err
		}
	}
	return row, nil
}

// SearchHit is one entity matching a search query.
type SearchHit struct {
	Kind  string `json:"kind"` // ledger | transaction | pool | account | contract | asset | tag
	ID    string `json:"id"`
	Label string `json:"label"`
}

var (
	reHash   = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	reStrkey = regexp.MustCompile(`^[GCM][A-Z2-7]{55}$|^M[A-Z2-7]{68}$`)
	reSeq    = regexp.MustCompile(`^[0-9]{1,10}$`)
	reTagish = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
)

// Search classifies free text into entities that exist in the index.
func (r *Repo) Search(ctx context.Context, q string) ([]SearchHit, error) {
	out := []SearchHit{}
	exists := func(sql string, arg any) (bool, error) {
		var ok bool
		err := r.db.QueryRow(ctx, sql, arg).Scan(&ok)
		return ok, err
	}
	switch {
	case reSeq.MatchString(q):
		n, _ := strconv.ParseInt(q, 10, 64)
		ok, err := exists(`SELECT EXISTS (SELECT 1 FROM ledgers WHERE sequence = $1)`, n)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, SearchHit{"ledger", q, "Ledger " + q})
		}
	case reHash.MatchString(q):
		h := strings.ToLower(q)
		ok, err := exists(`SELECT EXISTS (SELECT 1 FROM transactions WHERE hash = $1 OR inner_hash = $1)`, h)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, SearchHit{"transaction", h, "Transaction"})
		}
		if ok, err = exists(`SELECT EXISTS (SELECT 1 FROM liquidity_pools WHERE pool_id = $1)`, h); err != nil {
			return nil, err
		} else if ok {
			out = append(out, SearchHit{"pool", h, "Liquidity pool"})
		}
	case reStrkey.MatchString(q):
		if q[0] != 'C' {
			out = append(out, SearchHit{"account", q, "Account"})
			break
		}
		label := "Contract"
		ok, err := exists(`SELECT EXISTS (SELECT 1 FROM assets WHERE contract_id = $1)`, q)
		if err != nil {
			return nil, err
		}
		if ok {
			label = "Token contract"
		}
		out = append(out, SearchHit{"contract", q, label})
	}
	if len(out) == 0 && len(q) <= 69 {
		rows, err := r.rows(ctx, `SELECT asset_key, code FROM assets
			WHERE asset_key = $1 OR upper(code) LIKE upper($1) || '%' OR contract_id = $1
			ORDER BY (upper(code) = upper($1)) DESC, (SELECT count(*) FROM candles_1m c WHERE c.base = asset_key) DESC
			LIMIT 10`, q)
		if err != nil {
			return nil, err
		}
		for _, a := range rows {
			code, _ := a["code"].(string)
			key := fmt.Sprint(a["asset_key"])
			if code == "" {
				code = key
			}
			out = append(out, SearchHit{"asset", key, code})
		}
	}
	if t := strings.ToLower(strings.TrimPrefix(q, "#")); reTagish.MatchString(t) {
		rows, err := r.rows(ctx, `SELECT tag, count(*) AS n FROM labels WHERE tag LIKE $1 || '%' GROUP BY tag ORDER BY 2 DESC LIMIT 5`, t)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out = append(out, SearchHit{"tag", row["tag"].(string), fmt.Sprintf("#%s (%v addresses)", row["tag"], row["n"])})
		}
	}
	return out, nil
}
