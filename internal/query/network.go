package query

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Component is a pipeline heartbeat (fetcher, writer).
type Component struct {
	Name      string         `json:"name"`
	Role      string         `json:"role"`
	Ledger    *int64         `json:"ledger"`
	Tip       *int64         `json:"tip"`
	Info      map[string]any `json:"info"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Gap is a ledger range the writer never received.
type Gap struct {
	From       int64     `json:"from_ledger"`
	To         int64     `json:"to_ledger"`
	Reason     string    `json:"reason"`
	DetectedAt time.Time `json:"detected_at"`
}

// Status is the index position and pipeline health.
type Status struct {
	Cursor         *int64      `json:"cursor"`
	UpdatedAt      *time.Time  `json:"updated_at"`
	FirstLedger    *int64      `json:"first_ledger"`
	LastClosedAt   *time.Time  `json:"last_closed_at"`
	LedgersIndexed int64       `json:"ledgers_indexed"`
	Components     []Component `json:"components"`
	Gaps           int64       `json:"gaps"`
	LastGap        *Gap        `json:"last_gap"`
}

// Status reads the cursor, ledger range, component heartbeats and gaps.
func (r *Repo) Status(ctx context.Context, cursor string) (Status, error) {
	var s Status
	err := r.db.QueryRow(ctx, `SELECT (SELECT ledger FROM cursors WHERE name = $1), (SELECT updated_at FROM cursors WHERE name = $1),
		(SELECT min(sequence) FROM ledgers), (SELECT max(closed_at) FROM ledgers), (SELECT count(*) FROM ledgers),
		(SELECT count(*) FROM ledger_gaps)`, cursor).
		Scan(&s.Cursor, &s.UpdatedAt, &s.FirstLedger, &s.LastClosedAt, &s.LedgersIndexed, &s.Gaps)
	if err != nil {
		return s, err
	}
	rows, err := r.db.Query(ctx, `SELECT name, role, ledger, tip, info, updated_at FROM components ORDER BY name`)
	if err != nil {
		return s, err
	}
	s.Components, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Component, error) {
		var c Component
		err := row.Scan(&c.Name, &c.Role, &c.Ledger, &c.Tip, &c.Info, &c.UpdatedAt)
		return c, err
	})
	if err != nil {
		return s, err
	}
	if s.Components == nil {
		s.Components = []Component{}
	}
	var g Gap
	err = r.db.QueryRow(ctx, `SELECT from_ledger, to_ledger, reason, detected_at FROM ledger_gaps ORDER BY from_ledger DESC LIMIT 1`).
		Scan(&g.From, &g.To, &g.Reason, &g.DetectedAt)
	if err == nil {
		s.LastGap = &g
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return s, err
	}
	return s, nil
}

// Ledgers lists ledgers below before (0 = newest), newest first.
func (r *Repo) Ledgers(ctx context.Context, before int64, limit int) (Rows, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	return r.rows(ctx, `SELECT * FROM ledgers WHERE sequence < $1 ORDER BY sequence DESC LIMIT $2`, before, limit)
}

// Ledger returns one ledger with its transaction list, or nil.
func (r *Repo) Ledger(ctx context.Context, seq int64) (Row, error) {
	row, err := r.row(ctx, `SELECT * FROM ledgers WHERE sequence = $1`, seq)
	if row == nil || err != nil {
		return row, err
	}
	row["transactions"], err = r.rows(ctx, `SELECT hash, tx_index, source, successful, op_count, fee_charged, is_soroban, contract_id, function
		FROM transactions WHERE ledger = $1 ORDER BY tx_index`, seq)
	return row, err
}

// NetworkStats aggregates ledgers per minute|hour|day since a time.
func (r *Repo) NetworkStats(ctx context.Context, since time.Time, bucket string) (Rows, error) {
	return r.rows(ctx, `SELECT date_trunc($2, closed_at) AS t, count(*) AS ledgers, sum(tx_count) AS txs,
		sum(tx_success) AS txs_ok, sum(tx_failed) AS txs_failed, sum(op_count) AS ops, sum(soroban_tx_count) AS soroban_txs,
		sum(fees_charged) / 1e7::float8 AS fees_xlm, round(avg(tx_count), 2) AS avg_txs_per_ledger, max(protocol) AS protocol
		FROM ledgers WHERE closed_at > $1 GROUP BY 1 ORDER BY 1 DESC`, since, bucket)
}

// OperationStats is the operation mix since a time.
func (r *Repo) OperationStats(ctx context.Context, since time.Time) (Rows, error) {
	return r.rows(ctx, `SELECT type, count(*) AS ops, count(*) FILTER (WHERE successful) AS successful,
		count(DISTINCT source) AS sources FROM operations WHERE ledger >= `+ledgerSince("$1")+`
		GROUP BY type ORDER BY ops DESC`, since)
}

// TableStats lists tables with planner row estimates and sizes; partitioned
// tables are summed over their partitions.
func (r *Repo) TableStats(ctx context.Context) (Rows, error) {
	return r.rows(ctx, `SELECT c.relname AS table,
		CASE WHEN c.relkind = 'p' THEN (SELECT coalesce(sum(greatest(p.reltuples, 0)), 0) FROM pg_partition_tree(c.oid) t JOIN pg_class p ON p.oid = t.relid WHERE t.isleaf)
		     ELSE greatest(c.reltuples, 0) END::bigint AS rows_estimate,
		CASE WHEN c.relkind = 'p' THEN (SELECT coalesce(sum(pg_total_relation_size(t.relid)), 0) FROM pg_partition_tree(c.oid) t WHERE t.isleaf)
		     ELSE pg_total_relation_size(c.oid) END::bigint AS bytes,
		CASE WHEN c.relkind = 'p' THEN (SELECT count(*) FROM pg_partition_tree(c.oid) t WHERE t.isleaf) END AS partitions
		FROM pg_class c WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition AND c.relnamespace = to_regnamespace(current_schema())
		ORDER BY bytes DESC`)
}
