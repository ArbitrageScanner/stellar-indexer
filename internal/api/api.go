// Package api serves analytics queries over the indexed data as JSON.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Status exposes live ingestion progress (optional; nil when the API runs alone).
type Status interface {
	Progress() (current, tip uint32)
}

type Server struct {
	db      *pgxpool.Pool
	log     *slog.Logger
	status  Status
	network string
	cursor  string

	// UI, when set, is served under /ui/ and browsers hitting / are redirected there.
	UI fs.FS
	// USDAsset prices XLM in USD (e.g. USDC:G…); empty disables USD values.
	USDAsset string
	// LabelsToken, when set, is required (Bearer) for label writes.
	LabelsToken string
}

func New(db *pgxpool.Pool, log *slog.Logger, status Status, network, cursor string) *Server {
	return &Server{db: db, log: log, status: status, network: network, cursor: cursor}
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", s.index)
	m.HandleFunc("GET /health", s.health)
	m.HandleFunc("GET /status", s.statusH)
	m.HandleFunc("GET /ledgers", s.ledgers)
	m.HandleFunc("GET /ledgers/{seq}", s.ledger)
	m.HandleFunc("GET /transactions/{hash}", s.transaction)
	m.HandleFunc("GET /accounts/top", s.topAccounts)
	m.HandleFunc("GET /accounts/{id}", s.account)
	m.HandleFunc("GET /accounts/{id}/operations", s.accountOps)
	m.HandleFunc("GET /accounts/{id}/transfers", s.accountTransfers)
	m.HandleFunc("GET /accounts/{id}/trades", s.accountTrades)
	m.HandleFunc("GET /assets", s.assets)
	m.HandleFunc("GET /assets/{key}", s.asset)
	m.HandleFunc("GET /pairs", s.pairs)
	m.HandleFunc("GET /trades", s.trades)
	m.HandleFunc("GET /price", s.price)
	m.HandleFunc("GET /candles", s.candles)
	m.HandleFunc("GET /pools", s.pools)
	m.HandleFunc("GET /protocols", s.protocols)
	m.HandleFunc("GET /protocols/{name}/events", s.protocolEvents)
	m.HandleFunc("GET /contracts/{id}/events", s.contractEvents)
	m.HandleFunc("GET /stats/network", s.networkStats)
	m.HandleFunc("GET /stats/operations", s.opStats)
	m.HandleFunc("GET /stats/tables", s.dbStats)
	m.HandleFunc("GET /transactions", s.listTransactions)
	m.HandleFunc("GET /operations", s.listOperations)
	m.HandleFunc("GET /transfers", s.listTransfers)
	m.HandleFunc("GET /events", s.listEvents)
	m.HandleFunc("GET /contracts", s.listContracts)
	m.HandleFunc("GET /contracts/{id}", s.contractSummary)
	m.HandleFunc("GET /protocols/{name}/summary", s.protocolSummary)
	m.HandleFunc("GET /accounts/{id}/events", s.accountEvents)
	m.HandleFunc("GET /assets/{key}/holders", s.assetHolders)
	m.HandleFunc("GET /search", s.search)
	m.HandleFunc("GET /accounts/{id}/portfolio", s.portfolioH)
	m.HandleFunc("GET /accounts/{id}/stats", s.accountStats)
	m.HandleFunc("GET /accounts/{id}/tags", s.accountTags)
	m.HandleFunc("GET /labels", s.listLabels)
	m.HandleFunc("GET /labels/tags", s.labelTags)
	m.HandleFunc("POST /labels", s.putLabel)
	m.HandleFunc("DELETE /labels/{address}/{tag}", s.deleteLabel)
	m.HandleFunc("GET /leaderboards", s.leaderboards)
	m.HandleFunc("GET /alerts", s.alerts)
	if s.UI != nil {
		m.Handle("GET /ui/", uiHandler(s.UI))
		m.Handle("GET /ui", http.RedirectHandler("/ui/", http.StatusMovedPermanently))
	}
	return s.recover(m)
}

func (s *Server) recover(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic", "path", r.URL.Path, "err", v)
				writeErr(w, http.StatusInternalServerError, fmt.Errorf("internal error"))
			}
			s.log.Debug("http", "path", r.URL.RequestURI(), "dur", time.Since(start))
		}()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions { // CORS preflight for label writes
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// --- helpers ---------------------------------------------------------------

type badRequest struct{ error }

func (s *Server) query(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToMap)
	if out == nil {
		out = []map[string]any{}
	}
	return out, err
}

func (s *Server) one(ctx context.Context, sql string, args ...any) (map[string]any, error) {
	rows, err := s.query(ctx, sql, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

func (s *Server) reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		var br badRequest
		if errors.As(err, &br) {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		s.log.Error("query failed", "err", err)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if m, ok := v.(map[string]any); ok && m == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("not found"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func limit(r *http.Request, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return def
	}
	return min(n, max)
}

func hours(r *http.Request, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("hours"))
	if err != nil || n <= 0 {
		return def
	}
	return min(n, 24*90)
}

// cursorArg parses ?before=<id> for keyset pagination (0 = from newest).
func cursorArg(r *http.Request) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if n <= 0 {
		return 1<<63 - 1
	}
	return n
}

// assetArg normalises asset keys: "XLM"/"native" → native.
func assetArg(v string) string {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "xlm") || strings.EqualFold(v, "native") {
		return "native"
	}
	return v
}

// --- handlers --------------------------------------------------------------

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if s.UI != nil && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/ui/", http.StatusFound)
		return
	}
	s.reply(w, map[string]any{
		"service": "stellar-indexer", "network": s.network,
		"ui": s.UI != nil,
		"endpoints": []string{
			"/health", "/status", "/search?q", "/ledgers?limit&before", "/ledgers/{seq}",
			"/transactions?source&contract&function&successful&soroban&ledger&before&limit", "/transactions/{hash}",
			"/operations?type&source&successful&before&limit",
			"/accounts/top?limit", "/accounts/{id}", "/accounts/{id}/operations?limit&before", "/accounts/{id}/transfers",
			"/accounts/{id}/trades", "/accounts/{id}/events",
			"/assets?hours&limit", "/assets/{key}", "/assets/{key}/holders", "/pairs?hours&limit",
			"/trades?base&counter&venue&before&limit", "/transfers?asset&type&before&limit",
			"/price?base&counter", "/candles?base&counter&interval=1m|5m|15m|30m|1h|4h|1d&from&to&limit",
			"/pools?limit", "/protocols?hours", "/protocols/{name}/summary?hours", "/protocols/{name}/events?action&user&contract&before&limit",
			"/contracts?hours&limit", "/contracts/{id}", "/contracts/{id}/events?before&limit", "/events?contract&topic&protocol&before&limit",
			"/stats/network?hours&bucket=hour|minute|day", "/stats/operations?hours", "/stats/tables",
			"/accounts/{id}/portfolio?history", "/accounts/{id}/stats?hours", "/accounts/{id}/tags",
			"/labels?tag&address", "/labels/tags", "POST /labels {address,tag,note}", "DELETE /labels/{address}/{tag}",
			"/leaderboards?hours&limit", "/alerts?hours&min_xlm&min_profit&min_move&kinds",
		},
		"usd_asset": s.USDAsset, "labels_auth": s.LabelsToken != "",
	}, nil)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.db.Ping(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	s.reply(w, map[string]any{"status": "ok"}, nil)
}

func (s *Server) statusH(w http.ResponseWriter, r *http.Request) {
	row, err := s.one(r.Context(), `SELECT c.ledger AS cursor, c.updated_at,
		(SELECT min(sequence) FROM ledgers) AS first_ledger,
		(SELECT max(closed_at) FROM ledgers) AS last_closed_at,
		(SELECT count(*) FROM ledgers) AS ledgers_indexed
		FROM cursors c WHERE c.name=$1`, s.cursor)
	if err == nil && row == nil {
		row = map[string]any{"cursor": nil}
	}
	if row != nil {
		row["network"] = s.network
		if s.status != nil {
			cur, tip := s.status.Progress()
			row["processing"], row["rpc_tip"] = cur, tip
			if tip >= cur {
				row["lag_ledgers"] = tip - cur
			}
		}
		if t, ok := row["last_closed_at"].(time.Time); ok {
			row["lag_seconds"] = int(time.Since(t).Seconds())
		}
	}
	s.reply(w, row, err)
}

func (s *Server) ledgers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `SELECT * FROM ledgers WHERE sequence < $1 ORDER BY sequence DESC LIMIT $2`,
		cursorArg(r), limit(r, 20, 200))
	s.reply(w, rows, err)
}

func (s *Server) ledger(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil {
		s.reply(w, nil, badRequest{fmt.Errorf("bad ledger sequence")})
		return
	}
	row, err := s.one(r.Context(), `SELECT * FROM ledgers WHERE sequence=$1`, seq)
	if row != nil {
		row["transactions"], err = s.query(r.Context(),
			`SELECT hash, tx_index, source, successful, op_count, fee_charged, is_soroban, contract_id, function
			 FROM transactions WHERE ledger=$1 ORDER BY tx_index`, seq)
	}
	s.reply(w, row, err)
}

func (s *Server) transaction(w http.ResponseWriter, r *http.Request) {
	ctx, h := r.Context(), strings.ToLower(r.PathValue("hash"))
	row, err := s.one(ctx, `SELECT * FROM transactions WHERE hash=$1`, h)
	if row == nil || err != nil {
		s.reply(w, row, err)
		return
	}
	for k, q := range map[string]string{
		"operations":      `SELECT id::text, op_index, type, source, successful, result_code, details FROM operations WHERE tx_hash=$1 ORDER BY op_index`,
		"transfers":       `SELECT transfer_index, op_index, type, from_addr, to_addr, asset, amount::text FROM transfers WHERE tx_hash=$1 ORDER BY transfer_index`,
		"trades":          `SELECT op_index, trade_index, venue, pool, maker, taker, sold_asset, sold_amount::text, bought_asset, bought_amount::text, base, counter, price FROM trades WHERE tx_hash=$1 ORDER BY op_index, trade_index`,
		"contract_events": `SELECT op_index, event_index, contract_id, protocol, topics, data FROM contract_events WHERE tx_hash=$1 ORDER BY op_index, event_index`,
		"protocol_events": `SELECT op_index, event_index, protocol, contract_id, action, user_addr, asset, amount0::text, amount1::text FROM protocol_events WHERE tx_hash=$1 ORDER BY op_index, event_index`,
	} {
		if row[k], err = s.query(ctx, q, h); err != nil {
			break
		}
	}
	s.reply(w, row, err)
}

func (s *Server) topAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `SELECT account_id, balance / 1e7::float8 AS xlm, num_subentries, home_domain, last_modified
		FROM accounts WHERE NOT deleted ORDER BY balance DESC LIMIT $1`, limit(r, 50, 500))
	s.reply(w, rows, err)
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	row, err := s.one(ctx, `SELECT a.*, a.balance / 1e7::float8 AS xlm FROM accounts a WHERE account_id=$1`, id)
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	if row == nil {
		row = map[string]any{"account_id": id, "state": "not touched since indexing began"}
	}
	if row["trustlines"], err = s.query(ctx, `SELECT asset, balance / 1e7::float8 AS balance, trust_limit / 1e7::float8 AS trust_limit, flags, last_modified
		FROM trustlines WHERE account_id=$1 ORDER BY balance DESC`, id); err != nil {
		s.reply(w, nil, err)
		return
	}
	if row["offers"], err = s.query(ctx, `SELECT offer_id::text, selling, buying, amount / 1e7::float8 AS amount, price, passive, last_modified
		FROM offers WHERE seller=$1 ORDER BY offer_id DESC LIMIT 200`, id); err != nil {
		s.reply(w, nil, err)
		return
	}
	row["activity_24h"], err = s.one(ctx, `SELECT
		(SELECT count(*) FROM transactions WHERE source=$1 AND closed_at > now() - interval '24 hours') AS txs,
		(SELECT count(*) FROM transfers WHERE (from_addr=$1 OR to_addr=$1) AND closed_at > now() - interval '24 hours') AS transfers,
		(SELECT count(*) FROM trades WHERE taker=$1 AND closed_at > now() - interval '24 hours') AS trades,
		(SELECT count(*) FROM protocol_events WHERE user_addr=$1 AND closed_at > now() - interval '24 hours') AS protocol_events`, id)
	s.reply(w, row, err)
}

func (s *Server) accountOps(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `SELECT id::text, tx_hash, ledger, closed_at, type, successful, details
		FROM operations WHERE source=$1 AND id < $2 ORDER BY id DESC LIMIT $3`, r.PathValue("id"), cursorArg(r), limit(r, 50, 500))
	s.reply(w, rows, err)
}

func (s *Server) accountTransfers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `SELECT * FROM (
		  (SELECT ledger, closed_at, tx_hash, type, from_addr, to_addr, asset, amount::text FROM transfers WHERE from_addr=$1 ORDER BY ledger DESC LIMIT $2)
		  UNION ALL
		  (SELECT ledger, closed_at, tx_hash, type, from_addr, to_addr, asset, amount::text FROM transfers WHERE to_addr=$1 ORDER BY ledger DESC LIMIT $2)
		) t ORDER BY ledger DESC LIMIT $2`, r.PathValue("id"), limit(r, 50, 500))
	s.reply(w, rows, err)
}

func (s *Server) accountTrades(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `SELECT closed_at, tx_hash, venue, pool, sold_asset, sold_amount::text, bought_asset, bought_amount::text, price, base, counter
		FROM trades WHERE taker=$1 ORDER BY closed_at DESC LIMIT $2`, r.PathValue("id"), limit(r, 50, 500))
	s.reply(w, rows, err)
}

func (s *Server) assets(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `
		WITH legs AS (
		  SELECT base AS asset, base_volume AS vol FROM trades WHERE closed_at > now() - make_interval(hours => $1)
		  UNION ALL
		  SELECT counter, counter_volume FROM trades WHERE closed_at > now() - make_interval(hours => $1)
		), vol AS (SELECT asset, count(*) AS trades, sum(vol) AS volume FROM legs GROUP BY asset),
		tr AS (SELECT asset, count(*) AS transfers FROM transfers WHERE closed_at > now() - make_interval(hours => $1) GROUP BY asset)
		SELECT a.asset_key, a.type, a.code, a.issuer, a.contract_id, coalesce(v.trades,0) AS trades,
		       coalesce(v.volume,0) AS volume, coalesce(tr.transfers,0) AS transfers,
		       (SELECT count(*) FROM trustlines t WHERE t.asset = a.asset_key) AS holders_indexed
		FROM assets a LEFT JOIN vol v ON v.asset=a.asset_key LEFT JOIN tr ON tr.asset=a.asset_key
		WHERE v.trades IS NOT NULL OR tr.transfers IS NOT NULL
		ORDER BY coalesce(v.trades,0) + coalesce(tr.transfers,0) DESC LIMIT $2`, hours(r, 24), limit(r, 50, 500))
	s.reply(w, rows, err)
}

func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	ctx, key := r.Context(), assetArg(r.PathValue("key"))
	row, err := s.one(ctx, `SELECT * FROM assets WHERE asset_key=$1 OR contract_id=$1 LIMIT 1`, key)
	if row == nil || err != nil {
		s.reply(w, row, err)
		return
	}
	key = row["asset_key"].(string)
	row["stats_24h"], err = s.one(ctx, `SELECT
		(SELECT count(*) FROM transfers WHERE asset=$1 AND closed_at > now() - interval '24 hours') AS transfers,
		(SELECT coalesce(sum(amount),0)::text FROM transfers WHERE asset=$1 AND type='transfer' AND closed_at > now() - interval '24 hours') AS transfer_amount,
		(SELECT coalesce(sum(amount),0)::text FROM transfers WHERE asset=$1 AND type='mint' AND closed_at > now() - interval '24 hours') AS minted,
		(SELECT coalesce(sum(amount),0)::text FROM transfers WHERE asset=$1 AND type='burn' AND closed_at > now() - interval '24 hours') AS burned,
		(SELECT count(*) FROM trades WHERE (base=$1 OR counter=$1) AND closed_at > now() - interval '24 hours') AS trades,
		(SELECT count(*) FROM trustlines WHERE asset=$1) AS trustlines_indexed`, key)
	if err == nil {
		row["top_pairs"], err = s.query(ctx, `SELECT base, counter, count(*) AS trades, sum(base_volume) AS base_volume
			FROM trades WHERE (base=$1 OR counter=$1) AND closed_at > now() - interval '24 hours'
			GROUP BY 1,2 ORDER BY 3 DESC LIMIT 10`, key)
	}
	s.reply(w, row, err)
}

func (s *Server) pairs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `SELECT base, counter, count(*) AS trades, sum(base_volume) AS base_volume,
		sum(counter_volume) AS counter_volume, sum(counter_volume)/nullif(sum(base_volume),0) AS vwap,
		(array_agg(price ORDER BY ledger DESC, tx_index DESC, op_index DESC, trade_index DESC))[1] AS last_price,
		array_agg(DISTINCT venue) AS venues
		FROM trades WHERE closed_at > now() - make_interval(hours => $1) AND base <> ''
		GROUP BY base, counter ORDER BY trades DESC LIMIT $2`, hours(r, 24), limit(r, 50, 500))
	s.reply(w, rows, err)
}

// pair resolves ?base&counter into canonical order; inverted reports whether
// prices must be flipped for the caller's orientation.
func pair(r *http.Request) (base, counter string, inverted bool, err error) {
	b, c := assetArg(r.URL.Query().Get("base")), assetArg(r.URL.Query().Get("counter"))
	if b == "" || c == "" || b == c {
		return "", "", false, badRequest{fmt.Errorf("base and counter asset keys are required (native, CODE:ISSUER or C... contract)")}
	}
	if b <= c {
		return b, c, false, nil
	}
	return c, b, true, nil
}

func (s *Server) trades(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := &filters{}
	if err := f.position(r, "ledger", "tx_index", "op_index", "trade_index"); err != nil {
		s.reply(w, nil, err)
		return
	}
	if q.Get("base") != "" || q.Get("counter") != "" {
		b, c, _, err := pair(r)
		if err != nil {
			s.reply(w, nil, err)
			return
		}
		f.add("base = ?", b)
		f.add("counter = ?", c)
	}
	if v := q.Get("venue"); v != "" {
		f.add("venue = ?", v)
	}
	if v := q.Get("taker"); v != "" {
		f.add("taker = ?", v)
	}
	if v := q.Get("pool"); v != "" {
		f.add("pool = ?", v)
	}
	f.args = append(f.args, limit(r, 50, 1000))
	rows, err := s.query(r.Context(), `SELECT ledger, tx_index, op_index, trade_index, closed_at, tx_hash, venue, pool, maker, taker,
		sold_asset, sold_amount::text, bought_asset, bought_amount::text, base, counter, price, base_volume, counter_volume
		FROM trades WHERE `+f.sql()+fmt.Sprintf(`
		ORDER BY ledger DESC, tx_index DESC, op_index DESC, trade_index DESC LIMIT $%d`, len(f.args)), f.args...)
	s.reply(w, rows, err)
}

func (s *Server) price(w http.ResponseWriter, r *http.Request) {
	b, c, inv, err := pair(r)
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	row, err := s.one(r.Context(), `SELECT
		(SELECT price FROM trades WHERE base=$1 AND counter=$2 ORDER BY ledger DESC, tx_index DESC, op_index DESC, trade_index DESC LIMIT 1) AS last,
		(SELECT max(closed_at) FROM trades WHERE base=$1 AND counter=$2) AS last_trade_at,
		(SELECT open FROM candles_1m WHERE base=$1 AND counter=$2 AND bucket >= now() - interval '24 hours' ORDER BY bucket LIMIT 1) AS open_24h,
		(SELECT max(high) FROM candles_1m WHERE base=$1 AND counter=$2 AND bucket >= now() - interval '24 hours') AS high_24h,
		(SELECT min(low) FROM candles_1m WHERE base=$1 AND counter=$2 AND bucket >= now() - interval '24 hours') AS low_24h,
		(SELECT sum(base_volume) FROM candles_1m WHERE base=$1 AND counter=$2 AND bucket >= now() - interval '24 hours') AS base_volume_24h,
		(SELECT sum(counter_volume) FROM candles_1m WHERE base=$1 AND counter=$2 AND bucket >= now() - interval '24 hours') AS counter_volume_24h,
		(SELECT sum(trades) FROM candles_1m WHERE base=$1 AND counter=$2 AND bucket >= now() - interval '24 hours') AS trades_24h`, b, c)
	if err == nil && row != nil {
		if inv {
			for _, k := range []string{"last", "open_24h"} {
				row[k] = invert(row[k])
			}
			row["high_24h"], row["low_24h"] = invert(row["low_24h"]), invert(row["high_24h"])
			row["base_volume_24h"], row["counter_volume_24h"] = row["counter_volume_24h"], row["base_volume_24h"]
			b, c = c, b
		}
		row["base"], row["counter"] = b, c
		if l, ok := row["last"].(float64); ok {
			if o, ok := row["open_24h"].(float64); ok && o != 0 {
				row["change_24h_pct"] = (l/o - 1) * 100
			}
		}
	}
	s.reply(w, row, err)
}

func invert(v any) any {
	if f, ok := v.(float64); ok && f != 0 {
		return 1 / f
	}
	return v
}

var intervals = map[string]string{
	"1m": "1 minute", "5m": "5 minutes", "15m": "15 minutes", "30m": "30 minutes",
	"1h": "1 hour", "4h": "4 hours", "1d": "1 day",
}

func (s *Server) candles(w http.ResponseWriter, r *http.Request) {
	b, c, inv, err := pair(r)
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	q := r.URL.Query()
	iv := q.Get("interval")
	if iv == "" {
		iv = "1h"
	}
	step, ok := intervals[iv]
	if !ok {
		s.reply(w, nil, badRequest{fmt.Errorf("interval must be one of 1m,5m,15m,30m,1h,4h,1d")})
		return
	}
	to := time.Now().UTC()
	if t, err := time.Parse(time.RFC3339, q.Get("to")); err == nil {
		to = t
	}
	from := to.Add(-7 * 24 * time.Hour)
	if t, err := time.Parse(time.RFC3339, q.Get("from")); err == nil {
		from = t
	}
	rows, err := s.query(r.Context(), `SELECT date_bin($3::interval, bucket, '2000-01-01') AS t,
		(array_agg(open ORDER BY bucket))[1] AS open, max(high) AS high, min(low) AS low,
		(array_agg(close ORDER BY bucket DESC))[1] AS close,
		sum(base_volume) AS base_volume, sum(counter_volume) AS counter_volume, sum(trades) AS trades
		FROM candles_1m WHERE base=$1 AND counter=$2 AND bucket >= $4 AND bucket < $5
		GROUP BY 1 ORDER BY 1 DESC LIMIT $6`, b, c, step, from, to, limit(r, 500, 5000))
	if err == nil && inv {
		for _, row := range rows {
			row["open"], row["close"] = invert(row["open"]), invert(row["close"])
			row["high"], row["low"] = invert(row["low"]), invert(row["high"])
			row["base_volume"], row["counter_volume"] = row["counter_volume"], row["base_volume"]
		}
		b, c = c, b
	}
	s.reply(w, map[string]any{"base": b, "counter": c, "interval": iv, "candles": rows}, err)
}

func (s *Server) pools(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `SELECT pool_id, asset_a, asset_b, reserve_a / 1e7::float8 AS reserve_a, reserve_b / 1e7::float8 AS reserve_b,
		CASE WHEN reserve_a > 0 THEN reserve_b::float8 / reserve_a END AS price_b_per_a, total_shares, trustline_count, fee_bp, last_modified
		FROM liquidity_pools WHERE NOT deleted ORDER BY trustline_count DESC LIMIT $1`, limit(r, 50, 500))
	s.reply(w, rows, err)
}

func (s *Server) protocols(w http.ResponseWriter, r *http.Request) {
	h := hours(r, 24)
	rows, err := s.query(r.Context(), `SELECT protocol, count(*) AS events, count(DISTINCT user_addr) AS users,
		count(DISTINCT contract_id) AS contracts, count(DISTINCT tx_hash) AS txs,
		jsonb_object_agg(action, n) AS actions
		FROM (SELECT protocol, action, user_addr, contract_id, tx_hash, count(*) OVER (PARTITION BY protocol, action) AS n
		      FROM protocol_events WHERE closed_at > now() - make_interval(hours => $1)) e
		GROUP BY protocol ORDER BY events DESC`, h)
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	vol, err := s.query(r.Context(), `SELECT venue, count(*) AS trades, count(DISTINCT pool) AS pools, count(DISTINCT taker) AS takers
		FROM trades WHERE closed_at > now() - make_interval(hours => $1) GROUP BY venue ORDER BY trades DESC`, h)
	s.reply(w, map[string]any{"hours": h, "protocols": rows, "trade_venues": vol}, err)
}

func (s *Server) protocolEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := &filters{}
	f.add("protocol = ?", r.PathValue("name"))
	if err := f.position(r, "ledger", "tx_index", "op_index", "event_index"); err != nil {
		s.reply(w, nil, err)
		return
	}
	if a := q.Get("action"); a != "" {
		f.add("action = ?", a)
	}
	if u := q.Get("user"); u != "" {
		f.add("user_addr = ?", u)
	}
	if c := q.Get("contract"); c != "" {
		f.add("contract_id = ?", c)
	}
	if v := q.Get("asset"); v != "" {
		f.add("asset = ?", assetArg(v))
	}
	f.args = append(f.args, limit(r, 50, 1000))
	rows, err := s.query(r.Context(), `SELECT ledger, tx_index, op_index, event_index, closed_at, tx_hash, contract_id, action, user_addr,
		asset, amount0::text, amount1::text, data
		FROM protocol_events WHERE `+f.sql()+fmt.Sprintf(` ORDER BY ledger DESC, tx_index DESC, op_index DESC, event_index DESC LIMIT $%d`, len(f.args)), f.args...)
	s.reply(w, rows, err)
}

func (s *Server) contractEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	q.Set("contract", r.PathValue("id"))
	r.URL.RawQuery = q.Encode()
	s.listEvents(w, r)
}

func (s *Server) networkStats(w http.ResponseWriter, r *http.Request) {
	bucket := r.URL.Query().Get("bucket")
	if bucket != "minute" && bucket != "day" {
		bucket = "hour"
	}
	rows, err := s.query(r.Context(), `SELECT date_trunc($2, closed_at) AS t, count(*) AS ledgers, sum(tx_count) AS txs,
		sum(tx_success) AS txs_ok, sum(tx_failed) AS txs_failed, sum(op_count) AS ops, sum(soroban_tx_count) AS soroban_txs,
		sum(fees_charged) / 1e7::float8 AS fees_xlm, round(avg(tx_count), 2) AS avg_txs_per_ledger, max(protocol) AS protocol
		FROM ledgers WHERE closed_at > now() - make_interval(hours => $1) GROUP BY 1 ORDER BY 1 DESC`, hours(r, 24), bucket)
	s.reply(w, rows, err)
}

func (s *Server) opStats(w http.ResponseWriter, r *http.Request) {
	rows, err := s.query(r.Context(), `SELECT type, count(*) AS ops, count(*) FILTER (WHERE successful) AS successful,
		count(DISTINCT source) AS sources FROM operations WHERE closed_at > now() - make_interval(hours => $1)
		GROUP BY type ORDER BY ops DESC`, hours(r, 24))
	s.reply(w, rows, err)
}
