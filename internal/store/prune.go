package store

import (
	"context"
	"fmt"

	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
)

const pruneLock = 727002

// Retention limits how far back each table group is kept, in ledgers behind
// the cursor. Zero keeps everything. Partitioned tables are trimmed by whole
// partitions, so up to one partition (PARTITION_LEDGERS) more than the window
// may be kept.
type Retention struct {
	Raw     uint32 // transactions, operations, transfers, contract_events
	Markets uint32 // trades, protocol_events
	Candles uint32 // candles_1m, ledgers
}

// Prune applies retention: drops expired partitions and deletes old rows from
// the small unpartitioned tables in chunks. It reports what it removed.
func (s *Store) Prune(ctx context.Context, cursorName string, r Retention) (map[string]int64, error) {
	// One pruner at a time (several writers may run as hot standbys).
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, pruneLock).Scan(&locked); err != nil || !locked {
		return nil, err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, pruneLock) //nolint:errcheck

	cur, err := s.Cursor(ctx, cursorName)
	if err != nil || cur == 0 {
		return nil, err
	}
	out := map[string]int64{}
	groups := []struct {
		keep   uint32
		tables []string
	}{
		{r.Raw, []string{"transactions", "operations", "transfers", "contract_events"}},
		{r.Markets, []string{"trades", "protocol_events"}},
	}
	for _, g := range groups {
		if g.keep == 0 || cur <= g.keep {
			continue
		}
		cutoff := int64(cur - g.keep)
		for _, t := range g.tables {
			n, err := s.dropPartitionsBefore(ctx, t, cutoff)
			if err != nil {
				return out, err
			}
			if n > 0 {
				out[t+"_partitions"] += int64(n)
			}
		}
	}
	if r.Candles > 0 && cur > r.Candles {
		cutoff := int64(cur - r.Candles)
		for {
			tag, err := s.Pool.Exec(ctx, `DELETE FROM ledgers WHERE sequence IN (SELECT sequence FROM ledgers WHERE sequence < $1 LIMIT 20000)`, cutoff)
			if err != nil {
				return out, fmt.Errorf("prune ledgers: %w", err)
			}
			out["ledgers"] += tag.RowsAffected()
			metrics.PrunedRows.Add(float64(tag.RowsAffected()))
			if tag.RowsAffected() < 20000 || ctx.Err() != nil {
				break
			}
		}
		// Candles are keyed by time: anchor the cutoff to the newest ledger.
		tag, err := s.Pool.Exec(ctx, `DELETE FROM candles_1m WHERE bucket < index_now() - make_interval(secs => $1)`, float64(r.Candles)*5.5)
		if err != nil {
			return out, err
		}
		out["candles_1m"] = tag.RowsAffected()
		metrics.PrunedRows.Add(float64(tag.RowsAffected()))
	}
	return out, ctx.Err()
}
