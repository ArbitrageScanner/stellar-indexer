// Package query is the read-side repository: every SQL statement the API
// runs lives here. Methods take typed parameters and return either typed
// domain values (anything analytics consumes, e.g. fills, prices, portfolio
// inputs, tag signals, tickers, candles) or a documented row set.
//
// Row sets (Rows) are a deliberate choice for plain list endpoints whose
// response is exactly the SQL projection: the column list in each query is
// the contract, documented in internal/api/openapi.yaml, and mapping each of
// them to a struct would only duplicate that list. Anything with logic on
// top of the data uses typed structs.
//
// Analytics windows are anchored to the newest indexed ledger (index_now(),
// see Anchor) rather than the wall clock, so a paused or lagging indexer
// still answers "the last 24 hours" over the data it has.
package query

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Row is one result row (column → value); Rows a documented row set.
type (
	Row  = map[string]any
	Rows = []Row
)

// InvalidArgError marks caller mistakes (bad cursor, unknown interval).
type InvalidArgError struct{ Msg string }

func (e *InvalidArgError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &InvalidArgError{Msg: fmt.Sprintf(format, args...)}
}

// IsInvalid reports whether err is a caller error.
func IsInvalid(err error) bool {
	var e *InvalidArgError
	return errors.As(err, &e)
}

// Repo runs queries against the index.
type Repo struct {
	db      *pgxpool.Pool
	timeout time.Duration
}

// New returns a repository with a 20 s statement budget per query.
func New(db *pgxpool.Pool) *Repo { return &Repo{db: db, timeout: 20 * time.Second} }

func (r *Repo) rows(ctx context.Context, sql string, args ...any) (Rows, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	res, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(res, pgx.RowToMap)
	if out == nil {
		out = Rows{}
	}
	return out, err
}

// row returns the first row, or nil when there is none.
func (r *Repo) row(ctx context.Context, sql string, args ...any) (Row, error) {
	rs, err := r.rows(ctx, sql, args...)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return rs[0], nil
}

// where accumulates AND-ed conditions with positional arguments.
type where struct {
	conds []string
	args  []any
}

// arg appends an argument and returns its placeholder.
func (w *where) arg(v any) string {
	w.args = append(w.args, v)
	return "$" + strconv.Itoa(len(w.args))
}

// add appends a condition; each '?' becomes a new placeholder for v.
func (w *where) add(cond string, v any) {
	w.conds = append(w.conds, strings.ReplaceAll(cond, "?", w.arg(v)))
}

func (w *where) raw(cond string) { w.conds = append(w.conds, cond) }

func (w *where) sql() string {
	if len(w.conds) == 0 {
		return "true"
	}
	return strings.Join(w.conds, " AND ")
}

// position adds a keyset condition (cols) < (before parts) from a
// dot-separated cursor such as "65321558.12.0.3".
func (w *where) position(before string, cols ...string) error {
	if before == "" {
		return nil
	}
	parts := strings.Split(before, ".")
	if len(parts) != len(cols) {
		return invalid("before must be %d dot-separated integers (%s)", len(cols), strings.Join(cols, "."))
	}
	ph := make([]string, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return invalid("before: %v", err)
		}
		ph[i] = w.arg(n)
	}
	w.raw(fmt.Sprintf("(%s) < (%s)", strings.Join(cols, ", "), strings.Join(ph, ", ")))
	return nil
}

// ledgerSince is the first ledger closed after a time placeholder; used to
// window tables partitioned by ledger (enables partition pruning).
func ledgerSince(ph string) string {
	return fmt.Sprintf("(SELECT coalesce(min(sequence), 0) FROM ledgers WHERE closed_at > %s)", ph)
}

// Anchor is the close time of the newest indexed ledger (now() when empty).
func (r *Repo) Anchor(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := r.db.QueryRow(ctx, `SELECT index_now()`).Scan(&t)
	return t.UTC(), err
}

// Ping checks database connectivity.
func (r *Repo) Ping(ctx context.Context) error { return r.db.Ping(ctx) }
