package store

import (
	"context"
	"fmt"
)

// Retention limits how far back each table group is kept, in ledgers behind
// the cursor. Zero keeps everything.
type Retention struct {
	Raw     uint32 // transactions, operations, transfers, contract_events
	Markets uint32 // trades, protocol_events
	Candles uint32 // candles_1m, ledgers
}

// Prune deletes rows older than the retention window. Deletes run in chunks
// so they never hold long locks against the ingest writer.
func (s *Store) Prune(ctx context.Context, cursorName string, r Retention) (map[string]int64, error) {
	cur, err := s.Cursor(ctx, cursorName)
	if err != nil || cur == 0 {
		return nil, err
	}
	out := map[string]int64{}
	run := func(keep uint32, table, col string, scale int64) error {
		if keep == 0 || cur <= keep {
			return nil
		}
		cutoff := int64(cur-keep) * scale
		for {
			tag, err := s.Pool.Exec(ctx, fmt.Sprintf(
				`DELETE FROM %s WHERE ctid IN (SELECT ctid FROM %s WHERE %s < $1 LIMIT 20000)`, table, table, col), cutoff)
			if err != nil {
				return fmt.Errorf("prune %s: %w", table, err)
			}
			out[table] += tag.RowsAffected()
			if tag.RowsAffected() < 20000 || ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	// operations.id is a TOID: ledger << 32, so it doubles as a ledger index.
	steps := []struct {
		keep       uint32
		table, col string
		scale      int64
	}{
		{r.Raw, "operations", "id", 1 << 32},
		{r.Raw, "transactions", "ledger", 1},
		{r.Raw, "transfers", "ledger", 1},
		{r.Raw, "contract_events", "ledger", 1},
		{r.Markets, "trades", "ledger", 1},
		{r.Markets, "protocol_events", "ledger", 1},
		{r.Candles, "ledgers", "sequence", 1},
	}
	for _, st := range steps {
		if err := run(st.keep, st.table, st.col, st.scale); err != nil {
			return out, err
		}
	}
	if r.Candles > 0 && cur > r.Candles {
		// ~5s per ledger; candles are keyed by time, not ledger.
		tag, err := s.Pool.Exec(ctx, `DELETE FROM candles_1m WHERE bucket < now() - make_interval(secs => $1)`, float64(r.Candles)*5.5)
		if err != nil {
			return out, err
		}
		out["candles_1m"] = tag.RowsAffected()
	}
	return out, nil
}
