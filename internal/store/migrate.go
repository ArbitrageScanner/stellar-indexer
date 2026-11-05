package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLock serialises migrations across processes (api, fetch, write).
const migrationLock = 727001

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

func loadMigrations() ([]migration, error) {
	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, f := range files {
		base := strings.TrimSuffix(strings.TrimPrefix(f, "migrations/"), ".sql")
		v, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("migration %s: name must start with a number", f)
		}
		b, _ := migrationFS.ReadFile(f)
		sum := sha256.Sum256(b)
		out = append(out, migration{version: v, name: base, sql: string(b), checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d", out[i].version)
		}
	}
	return out, nil
}

// ErrLegacySchema means the database was created by the pre-migration
// prototype; it has to be recreated (history is not retained by design).
var ErrLegacySchema = errors.New("database has tables but no schema_migrations: created by an older build; recreate it")

// Migrate applies pending migrations in order, each in its own transaction,
// and verifies the checksums of applied ones. Safe to call concurrently.
func (s *Store) Migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLock) //nolint:errcheck

	var hasTable, legacy bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL, to_regclass('ledgers') IS NOT NULL`).Scan(&hasTable, &legacy); err != nil {
		return err
	}
	if !hasTable && legacy {
		return ErrLegacySchema
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version int PRIMARY KEY, name text NOT NULL, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	applied := map[int]string{}
	rows, err := conn.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		var c string
		if err := rows.Scan(&v, &c); err != nil {
			rows.Close()
			return err
		}
		applied[v] = c
	}
	rows.Close()
	for _, m := range ms {
		if c, ok := applied[m.version]; ok {
			if c != m.checksum {
				return fmt.Errorf("migration %s was modified after being applied (checksum mismatch)", m.name)
			}
			continue
		}
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`, m.version, m.name, m.checksum)
			return err
		}); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	for v := range applied {
		if v > ms[len(ms)-1].version {
			return fmt.Errorf("database is at migration %d, newer than this binary (%d)", v, ms[len(ms)-1].version)
		}
	}
	return nil
}
