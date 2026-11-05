package query

import (
	"context"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
)

// LabelFilter selects user labels.
type LabelFilter struct {
	Tag, Address string
	Limit        int
}

// Labels lists user labels, newest first, with the address's indexed XLM.
func (r *Repo) Labels(ctx context.Context, f LabelFilter) (Rows, error) {
	w := &where{}
	if f.Tag != "" {
		w.add("l.tag = ?", f.Tag)
	}
	if f.Address != "" {
		w.add("l.address = ?", f.Address)
	}
	lim := w.arg(f.Limit)
	return r.rows(ctx, `SELECT l.address, l.tag, l.note, l.created_at, a.balance / 1e7::float8 AS xlm
		FROM labels l LEFT JOIN accounts a ON a.account_id = l.address
		WHERE `+w.sql()+` ORDER BY l.created_at DESC LIMIT `+lim, w.args...)
}

// LabelTags counts addresses per tag.
func (r *Repo) LabelTags(ctx context.Context) (Rows, error) {
	return r.rows(ctx, `SELECT tag, count(*) AS addresses, max(created_at) AS last_used FROM labels GROUP BY tag ORDER BY 2 DESC, 1`)
}

// PutLabel inserts or updates a (normalized) label.
func (r *Repo) PutLabel(ctx context.Context, l analytics.Label) (Row, error) {
	return r.row(ctx, `INSERT INTO labels (address, tag, note) VALUES ($1, $2, $3)
		ON CONFLICT (address, tag) DO UPDATE SET note = EXCLUDED.note RETURNING address, tag, note, created_at`, l.Address, l.Tag, l.Note)
}

// DeleteLabel removes a label; false when it did not exist.
func (r *Repo) DeleteLabel(ctx context.Context, address, tag string) (bool, error) {
	ct, err := r.db.Exec(ctx, `DELETE FROM labels WHERE address = $1 AND tag = $2`, address, tag)
	return ct.RowsAffected() > 0, err
}

// LabelsFor maps addresses to their tags.
func (r *Repo) LabelsFor(ctx context.Context, addrs []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(addrs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `SELECT address, array_agg(tag ORDER BY tag) FROM labels WHERE address = ANY($1) GROUP BY address`, addrs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		var tags []string
		if err := rows.Scan(&a, &tags); err != nil {
			return nil, err
		}
		out[a] = tags
	}
	return out, rows.Err()
}
