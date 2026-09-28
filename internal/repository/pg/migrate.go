package pg

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"
)

// legacyBaseline is the last migration that was applied by hand before the
// server learned to migrate itself. On a database that already has these
// tables we record them as applied instead of running them again.
const legacyBaseline = "0010"

// migrateLockID guards against two instances migrating at the same time
// (for example during a Railway redeploy overlap).
const migrateLockID = 7234001

// Migrate applies every *.sql file from files that has not been applied yet,
// in file-name order, each in its own transaction.
func Migrate(ctx context.Context, db *DB, files fs.FS) error {
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockID); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockID) //nolint:errcheck

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	// First run on a database that was migrated by hand: record the old
	// migrations as done instead of re-running them.
	if len(applied) == 0 {
		var legacy bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass('public.users') IS NOT NULL`).Scan(&legacy); err != nil {
			return fmt.Errorf("detect legacy schema: %w", err)
		}
		if legacy {
			for _, n := range names {
				v := version(n)
				if v <= legacyBaseline {
					if _, err := conn.Exec(ctx,
						`INSERT INTO schema_migrations(version) VALUES ($1) ON CONFLICT DO NOTHING`, v); err != nil {
						return err
					}
					applied[v] = true
					log.Printf("migrate: %s recorded as already applied (legacy baseline)", n)
				}
			}
		}
	}

	for _, n := range names {
		v := version(n)
		if applied[v] {
			continue
		}
		body, err := fs.ReadFile(files, n)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", n, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, v); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		log.Printf("migrate: applied %s", n)
	}
	return nil
}

// version turns "0011_platform_store.sql" into "0011".
func version(name string) string {
	if i := strings.IndexByte(name, '_'); i > 0 {
		return name[:i]
	}
	return strings.TrimSuffix(name, ".sql")
}
