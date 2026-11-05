package api

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Explorer endpoints: filtered feeds with keyset pagination, search and
// per-protocol summaries. Feed rows carry their position columns so a client
// can page with ?before=<ledger>.<tx_index>[.<op_index>].<index>.

// filters accumulates WHERE clauses with positional arguments.
type filters struct {
	where []string
	args  []any
}

func (f *filters) add(clause string, v any) {
	f.args = append(f.args, v)
	f.where = append(f.where, strings.ReplaceAll(clause, "?", fmt.Sprintf("$%d", len(f.args))))
}

func (f *filters) sql() string {
	if len(f.where) == 0 {
		return "true"
	}
	return strings.Join(f.where, " AND ")
}

// position adds a row-value keyset condition: (cols) < (before parts).
func (f *filters) position(r *http.Request, cols ...string) error {
	v := r.URL.Query().Get("before")
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	if len(parts) != len(cols) {
		return badRequest{fmt.Errorf("before must be %d dot-separated integers (%s)", len(cols), strings.Join(cols, "."))}
	}
	ph := make([]string, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return badRequest{fmt.Errorf("before: %v", err)}
		}
		f.args = append(f.args, n)
		ph[i] = fmt.Sprintf("$%d", len(f.args))
	}
	f.where = append(f.where, fmt.Sprintf("(%s) < (%s)", strings.Join(cols, ", "), strings.Join(ph, ", ")))
	return nil
}

func boolArg(v string) (bool, bool) {
	b, err := strconv.ParseBool(v)
	return b, err == nil
}

func (s *Server) listTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := &filters{}
	f.add("id < ?", cursorArg(r))
	if v := q.Get("source"); v != "" {
		f.add("source = ?", v)
	}
	if v := q.Get("contract"); v != "" {
		f.add("contract_id = ?", v)
	}
	if v := q.Get("function"); v != "" {
		f.add("function = ?", v)
	}
	if b, ok := boolArg(q.Get("successful")); ok {
		f.add("successful = ?", b)
	}
	if b, ok := boolArg(q.Get("soroban")); ok {
		f.add("is_soroban = ?", b)
	}
	if v, err := strconv.ParseInt(q.Get("ledger"), 10, 64); err == nil {
		f.add("ledger = ?", v)
	}
	f.args = append(f.args, limit(r, 50, 500))
	rows, err := s.query(r.Context(), `SELECT id::text, hash, ledger, tx_index, closed_at, source, successful, result_code,
		op_count, fee_charged, is_soroban, contract_id, function, memo_type, memo
		FROM transactions WHERE `+f.sql()+fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(f.args)), f.args...)
	s.reply(w, rows, err)
}

func (s *Server) listOperations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := &filters{}
	f.add("id < ?", cursorArg(r))
	if v := q.Get("type"); v != "" {
		f.add("type = ?", v)
	}
	if v := q.Get("source"); v != "" {
		f.add("source = ?", v)
	}
	if b, ok := boolArg(q.Get("successful")); ok {
		f.add("successful = ?", b)
	}
	f.args = append(f.args, limit(r, 50, 500))
	rows, err := s.query(r.Context(), `SELECT id::text, tx_hash, ledger, op_index, closed_at, type, source, successful, result_code, details
		FROM operations WHERE `+f.sql()+fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(f.args)), f.args...)
	s.reply(w, rows, err)
}

func (s *Server) listTransfers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := &filters{}
	if err := f.position(r, "ledger", "tx_index", "transfer_index"); err != nil {
		s.reply(w, nil, err)
		return
	}
	if v := q.Get("asset"); v != "" {
		f.add("asset = ?", assetArg(v))
	}
	if v := q.Get("type"); v != "" {
		f.add("type = ?", v)
	}
	f.args = append(f.args, limit(r, 50, 500))
	rows, err := s.query(r.Context(), `SELECT ledger, tx_index, transfer_index, op_index, closed_at, tx_hash, type, from_addr, to_addr, asset, amount::text
		FROM transfers WHERE `+f.sql()+fmt.Sprintf(` ORDER BY ledger DESC, tx_index DESC, transfer_index DESC LIMIT $%d`, len(f.args)), f.args...)
	s.reply(w, rows, err)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := &filters{}
	if err := f.position(r, "ledger", "tx_index", "op_index", "event_index"); err != nil {
		s.reply(w, nil, err)
		return
	}
	if v := q.Get("contract"); v != "" {
		f.add("contract_id = ?", v)
	}
	if v := q.Get("topic"); v != "" {
		f.add("topic0 = ?", v)
	}
	if v := q.Get("protocol"); v != "" {
		if v == "none" {
			f.where = append(f.where, "protocol IS NULL")
		} else {
			f.add("protocol = ?", v)
		}
	}
	f.args = append(f.args, limit(r, 50, 500))
	rows, err := s.query(r.Context(), `SELECT ledger, tx_index, op_index, event_index, closed_at, tx_hash, contract_id, topic0, topics, data, protocol, successful
		FROM contract_events WHERE `+f.sql()+fmt.Sprintf(` ORDER BY ledger DESC, tx_index DESC, op_index DESC, event_index DESC LIMIT $%d`, len(f.args)), f.args...)
	s.reply(w, rows, err)
}

func (s *Server) listContracts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `
		WITH ev AS (
		  SELECT contract_id, max(protocol) AS protocol, count(*) AS events, count(DISTINCT tx_hash) AS txs,
		         max(closed_at) AS last_seen, (array_agg(DISTINCT topic0))[1:6] AS topics
		  FROM contract_events WHERE closed_at > now() - make_interval(hours => $1) GROUP BY contract_id
		), calls AS (
		  SELECT contract_id, count(*) AS calls, count(*) FILTER (WHERE NOT successful) AS failed_calls,
		         count(DISTINCT source) AS callers, (array_agg(DISTINCT function))[1:6] AS functions
		  FROM transactions WHERE contract_id IS NOT NULL AND closed_at > now() - make_interval(hours => $1) GROUP BY contract_id
		)
		SELECT coalesce(ev.contract_id, calls.contract_id) AS contract_id, ev.protocol,
		       coalesce(ev.events, 0) AS events, coalesce(ev.txs, 0) AS event_txs, ev.last_seen, ev.topics,
		       coalesce(calls.calls, 0) AS calls, coalesce(calls.failed_calls, 0) AS failed_calls,
		       coalesce(calls.callers, 0) AS callers, calls.functions,
		       a.asset_key AS token
		FROM ev FULL JOIN calls ON calls.contract_id = ev.contract_id
		LEFT JOIN assets a ON a.contract_id = coalesce(ev.contract_id, calls.contract_id)
		ORDER BY coalesce(ev.events, 0) + coalesce(calls.calls, 0) DESC LIMIT $2`, hours(r, 24), limit(r, 50, 500))
	s.reply(w, rows, err)
}

func (s *Server) contractSummary(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	row, err := s.one(ctx, `SELECT $1::text AS contract_id,
		(SELECT max(protocol) FROM contract_events WHERE contract_id=$1) AS protocol,
		(SELECT count(*) FROM contract_events WHERE contract_id=$1) AS events,
		(SELECT count(*) FROM transactions WHERE contract_id=$1) AS calls,
		(SELECT count(DISTINCT source) FROM transactions WHERE contract_id=$1) AS callers,
		(SELECT min(closed_at) FROM contract_events WHERE contract_id=$1) AS first_seen,
		(SELECT max(closed_at) FROM contract_events WHERE contract_id=$1) AS last_seen,
		(SELECT asset_key FROM assets WHERE contract_id=$1 LIMIT 1) AS token`, id)
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	for k, q := range map[string]string{
		"topics": `SELECT topic0, count(*) AS events FROM contract_events WHERE contract_id=$1 GROUP BY 1 ORDER BY 2 DESC LIMIT 20`,
		"functions": `SELECT function, count(*) AS calls, count(*) FILTER (WHERE NOT successful) AS failed
			FROM transactions WHERE contract_id=$1 GROUP BY 1 ORDER BY 2 DESC LIMIT 20`,
		"protocol_actions": `SELECT action, count(*) AS events FROM protocol_events WHERE contract_id=$1 GROUP BY 1 ORDER BY 2 DESC`,
	} {
		if row[k], err = s.query(ctx, q, id); err != nil {
			break
		}
	}
	s.reply(w, row, err)
}

func (s *Server) protocolSummary(w http.ResponseWriter, r *http.Request) {
	ctx, name, h := r.Context(), r.PathValue("name"), hours(r, 24)
	out := map[string]any{"protocol": name, "hours": h}
	var err error
	for k, q := range map[string]string{
		"actions": `SELECT action, count(*) AS events, count(DISTINCT user_addr) AS users, count(DISTINCT tx_hash) AS txs
			FROM protocol_events WHERE protocol=$1 AND closed_at > now() - make_interval(hours => $2)
			GROUP BY 1 ORDER BY 2 DESC`,
		"by_asset": `SELECT action, asset, count(*) AS events, count(DISTINCT user_addr) AS users,
			sum(amount0) / 1e7::float8 AS amount0, sum(amount1) / 1e7::float8 AS amount1
			FROM protocol_events WHERE protocol=$1 AND asset IS NOT NULL AND closed_at > now() - make_interval(hours => $2)
			GROUP BY 1, 2 ORDER BY 3 DESC LIMIT 200`,
		"contracts": `SELECT contract_id, count(*) AS events, count(DISTINCT user_addr) AS users, array_agg(DISTINCT action) AS actions,
			max(closed_at) AS last_seen
			FROM protocol_events WHERE protocol=$1 AND closed_at > now() - make_interval(hours => $2)
			GROUP BY 1 ORDER BY 2 DESC LIMIT 50`,
		"users": `SELECT user_addr, count(*) AS events, count(DISTINCT tx_hash) AS txs, array_agg(DISTINCT action) AS actions
			FROM protocol_events WHERE protocol=$1 AND user_addr IS NOT NULL AND closed_at > now() - make_interval(hours => $2)
			GROUP BY 1 ORDER BY 2 DESC LIMIT 20`,
		"pools": `SELECT pool, base, counter, count(*) AS trades, sum(base_volume) AS base_volume, sum(counter_volume) AS counter_volume,
			(array_agg(price ORDER BY ledger DESC, tx_index DESC, op_index DESC, trade_index DESC))[1] AS last_price,
			count(DISTINCT taker) AS takers
			FROM trades WHERE venue=$1 AND closed_at > now() - make_interval(hours => $2)
			GROUP BY 1, 2, 3 ORDER BY 4 DESC LIMIT 50`,
		"timeline": `SELECT date_trunc('hour', closed_at) AS t, count(*) AS events, count(DISTINCT user_addr) AS users
			FROM protocol_events WHERE protocol=$1 AND closed_at > now() - make_interval(hours => $2)
			GROUP BY 1 ORDER BY 1`,
	} {
		if out[k], err = s.query(ctx, q, name, h); err != nil {
			break
		}
	}
	s.reply(w, out, err)
}

func (s *Server) accountEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `SELECT ledger, closed_at, tx_hash, protocol, contract_id, action, asset, amount0::text, amount1::text
		FROM protocol_events WHERE user_addr=$1 ORDER BY closed_at DESC LIMIT $2`, r.PathValue("id"), limit(r, 50, 500))
	s.reply(w, rows, err)
}

func (s *Server) assetHolders(w http.ResponseWriter, r *http.Request) {
	key, n := assetArg(r.PathValue("key")), limit(r, 50, 500)
	if key == "native" {
		rows, err := s.query(r.Context(), `SELECT account_id, balance / 1e7::float8 AS balance, last_modified
			FROM accounts WHERE NOT deleted ORDER BY balance DESC LIMIT $1`, n)
		s.reply(w, rows, err)
		return
	}
	rows, err := s.query(r.Context(), `SELECT account_id, balance / 1e7::float8 AS balance, trust_limit / 1e7::float8 AS trust_limit, flags, last_modified
		FROM trustlines WHERE asset = (SELECT asset_key FROM assets WHERE asset_key=$1 OR contract_id=$1 LIMIT 1)
		ORDER BY balance DESC LIMIT $2`, key, n)
	s.reply(w, rows, err)
}

var (
	reHash   = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	reStrkey = regexp.MustCompile(`^[GCM][A-Z2-7]{55}$|^M[A-Z2-7]{68}$`)
	reSeq    = regexp.MustCompile(`^[0-9]{1,10}$`)
)

// search classifies a free-text query into explorer entities that exist in
// the index (ledger, transaction, pool, account, contract, asset).
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	ctx, q := r.Context(), strings.TrimSpace(r.URL.Query().Get("q"))
	out := []map[string]any{}
	add := func(kind, id, label string) {
		out = append(out, map[string]any{"kind": kind, "id": id, "label": label})
	}
	exists := func(sql string, arg any) bool {
		var ok bool
		_ = s.db.QueryRow(ctx, sql, arg).Scan(&ok)
		return ok
	}
	if q == "" {
		s.reply(w, out, badRequest{fmt.Errorf("q is required")})
		return
	}
	switch {
	case reSeq.MatchString(q):
		n, _ := strconv.ParseInt(q, 10, 64)
		if exists(`SELECT EXISTS (SELECT 1 FROM ledgers WHERE sequence=$1)`, n) {
			add("ledger", q, "Ledger "+q)
		}
	case reHash.MatchString(q):
		h := strings.ToLower(q)
		if exists(`SELECT EXISTS (SELECT 1 FROM transactions WHERE hash=$1 OR inner_hash=$1)`, h) {
			add("transaction", h, "Transaction")
		}
		if exists(`SELECT EXISTS (SELECT 1 FROM liquidity_pools WHERE pool_id=$1)`, h) {
			add("pool", h, "Liquidity pool")
		}
	case reStrkey.MatchString(q):
		if q[0] == 'C' {
			label := "Contract"
			if exists(`SELECT EXISTS (SELECT 1 FROM assets WHERE contract_id=$1)`, q) {
				label = "Token contract"
			}
			add("contract", q, label)
		} else {
			add("account", q, "Account")
		}
	}
	if len(out) == 0 && len(q) <= 69 {
		rows, err := s.query(ctx, `SELECT asset_key, code, issuer, type FROM assets
			WHERE asset_key = $1 OR upper(code) LIKE upper($1) || '%' OR contract_id = $1
			ORDER BY (upper(code) = upper($1)) DESC, (SELECT count(*) FROM trades t WHERE t.base = asset_key OR t.counter = asset_key) DESC
			LIMIT 10`, q)
		if err != nil {
			s.reply(w, nil, err)
			return
		}
		for _, a := range rows {
			code, _ := a["code"].(string)
			if code == "" {
				code = fmt.Sprint(a["asset_key"])
			}
			add("asset", fmt.Sprint(a["asset_key"]), code)
		}
	}
	if t := strings.ToLower(strings.TrimPrefix(q, "#")); reTag.MatchString(t) {
		tags, err := s.query(ctx, `SELECT tag, count(*) AS n FROM labels WHERE tag LIKE $1 || '%' GROUP BY tag ORDER BY 2 DESC LIMIT 5`, t)
		if err != nil {
			s.reply(w, nil, err)
			return
		}
		for _, r := range tags {
			add("tag", r["tag"].(string), fmt.Sprintf("#%s (%v addresses)", r["tag"], r["n"]))
		}
	}
	s.reply(w, out, nil)
}

// uiHandler serves the static frontend. index.html is never cached so a new
// binary's UI is picked up on reload; other files are cached briefly.
func uiHandler(ui fs.FS) http.Handler {
	files := http.StripPrefix("/ui/", http.FileServerFS(ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/ui/")
		if p == "" || p == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		files.ServeHTTP(w, r)
	})
}

// statsCounts is a quick table-size summary for the overview page; it uses
// planner estimates so it stays cheap on large tables.
func (s *Server) tableStats(ctx context.Context) ([]map[string]any, error) {
	return s.query(ctx, `SELECT relname AS table, greatest(reltuples, 0)::bigint AS rows_estimate,
		pg_total_relation_size(oid) AS bytes
		FROM pg_class WHERE relkind = 'r' AND relnamespace = to_regnamespace(current_schema())
		ORDER BY bytes DESC`)
}

func (s *Server) dbStats(w http.ResponseWriter, r *http.Request) {
	rows, err := s.tableStats(r.Context())
	s.reply(w, rows, err)
}
