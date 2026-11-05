package store

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
)

// Fact tables are RANGE-partitioned by ledger. Partitions are created ahead
// of the writer (current and next range) and retention drops whole
// partitions, which avoids the bloat and vacuum load of row deletes.
var partitionedTables = []string{"transactions", "operations", "trades", "transfers", "contract_events", "protocol_events"}

// DefaultPartitionLedgers is ~30 hours of ledgers per partition.
const DefaultPartitionLedgers = 20000

type partitions struct {
	mu   sync.Mutex
	size int64
	have map[int64]bool // range starts known to exist for every table
}

// partitionSize returns the range size fixed at first use (stored in meta):
// changing PARTITION_LEDGERS later must not produce overlapping ranges.
func (s *Store) partitionSize(ctx context.Context) (int64, error) {
	s.parts.mu.Lock()
	defer s.parts.mu.Unlock()
	if s.parts.size > 0 {
		return s.parts.size, nil
	}
	want := s.PartitionLedgers
	if want <= 0 {
		want = DefaultPartitionLedgers
	}
	var v string
	err := s.Pool.QueryRow(ctx, `INSERT INTO meta (key, value) VALUES ('partition_ledgers', $1)
		ON CONFLICT (key) DO UPDATE SET value = meta.value RETURNING value`, strconv.FormatInt(want, 10)).Scan(&v)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("meta.partition_ledgers=%q is invalid", v)
	}
	s.parts.size = n
	return n, nil
}

// EnsurePartitions makes sure the ranges holding seq and the next one exist.
func (s *Store) EnsurePartitions(ctx context.Context, seq uint32) error {
	size, err := s.partitionSize(ctx)
	if err != nil {
		return err
	}
	start := int64(seq) / size * size
	for _, from := range []int64{start, start + size} {
		s.parts.mu.Lock()
		done := s.parts.have[from]
		s.parts.mu.Unlock()
		if done {
			continue
		}
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			// Short lock wait: partition DDL briefly locks the parent table.
			if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '10s'`); err != nil {
				return err
			}
			for _, t := range partitionedTables {
				var exists bool
				if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, partName(t, from)).Scan(&exists); err != nil {
					return err
				}
				if exists {
					continue
				}
				if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%d) TO (%d)`,
					pgx.Identifier{partName(t, from)}.Sanitize(), pgx.Identifier{t}.Sanitize(), from, from+size)); err != nil {
					return fmt.Errorf("create partition %s: %w", partName(t, from), err)
				}
				metrics.PartitionsCreated.Inc()
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.parts.mu.Lock()
		s.parts.have[from] = true
		s.parts.mu.Unlock()
	}
	return nil
}

func partName(table string, from int64) string { return fmt.Sprintf("%s_p%d", table, from) }

var reBound = regexp.MustCompile(`FROM \('?(\d+)'?\) TO \('?(\d+)'?\)`)

type partition struct {
	name     string
	from, to int64
}

// listPartitions returns the attached partitions of table with their bounds.
func (s *Store) listPartitions(ctx context.Context, table string) ([]partition, error) {
	rows, err := s.Pool.Query(ctx, `SELECT c.relname, pg_get_expr(c.relpartbound, c.oid)
		FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = to_regclass($1) ORDER BY c.relname`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []partition
	for rows.Next() {
		var name, bound string
		if err := rows.Scan(&name, &bound); err != nil {
			return nil, err
		}
		m := reBound.FindStringSubmatch(bound)
		if m == nil {
			continue
		}
		from, _ := strconv.ParseInt(m[1], 10, 64)
		to, _ := strconv.ParseInt(m[2], 10, 64)
		out = append(out, partition{name: name, from: from, to: to})
	}
	return out, rows.Err()
}

// dropPartitionsBefore detaches (concurrently, so readers and the writer are
// not blocked) and drops every partition of table entirely below cutoff.
func (s *Store) dropPartitionsBefore(ctx context.Context, table string, cutoff int64) (int, error) {
	parts, err := s.listPartitions(ctx, table)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range parts {
		if p.to > cutoff {
			continue
		}
		parent, child := pgx.Identifier{table}.Sanitize(), pgx.Identifier{p.name}.Sanitize()
		if _, err := s.Pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s DETACH PARTITION %s CONCURRENTLY`, parent, child)); err != nil {
			// A previous detach interrupted mid-way leaves the partition pending.
			if _, ferr := s.Pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s DETACH PARTITION %s FINALIZE`, parent, child)); ferr != nil {
				return n, fmt.Errorf("detach %s: %w", p.name, err)
			}
		}
		if _, err := s.Pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, child)); err != nil {
			return n, fmt.Errorf("drop %s: %w", p.name, err)
		}
		s.parts.mu.Lock()
		delete(s.parts.have, p.from)
		s.parts.mu.Unlock()
		metrics.PartitionsDropped.Inc()
		n++
	}
	return n, nil
}
