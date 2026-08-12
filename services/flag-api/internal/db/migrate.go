// Package db owns the flag-api database schema and its migration runner.
//
// Until v2.0 the schema was applied by hand ("psql < schema.sql" then each
// migrations/*.sql in order) with nothing recording what had been applied —
// so fresh installs were manual and drift was undetectable. This runner makes
// migrations tracked, ordered, idempotent, and safe under concurrent replicas.
//
// Model (matches migrations/README.md):
//   - version 1  = schema.sql          (the baseline; every table/index/ext)
//   - versions N = migrations/NNN_*.sql (incremental; N is the filename prefix)
//
// Applied versions are recorded in the schema_migrations ledger. Re-running is a
// no-op. A Postgres advisory lock serializes concurrent runners so two flag-api
// replicas booting together can't double-apply.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// advisoryLockKey is an arbitrary, fixed 64-bit key. Every runner takes the same
// pg_advisory_lock on it, so migration runs are mutually exclusive cluster-wide.
const advisoryLockKey int64 = 8274_2026

//go:embed schema.sql
var baselineSchemaSQL string

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migration is a single ordered unit of schema change.
type migration struct {
	version int64
	name    string
	sql     string
}

// allMigrations returns every migration (baseline + increments) in ascending
// version order. It fails loudly on a malformed migration filename rather than
// silently skipping a schema change.
func allMigrations() ([]migration, error) {
	migs := []migration{{version: 1, name: "schema.sql (baseline)", sql: baselineSchemaSQL}}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q must be named <version>_<desc>.sql", name)
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration %q has non-numeric version prefix %q: %w", name, prefix, err)
		}
		if version <= 1 {
			return nil, fmt.Errorf("migration %q uses reserved version %d (1 = baseline schema.sql)", name, version)
		}
		content, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		migs = append(migs, migration{version: version, name: name, sql: string(content)})
	}

	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	for i := 1; i < len(migs); i++ {
		if migs[i].version == migs[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d (%q and %q)", migs[i].version, migs[i-1].name, migs[i].name)
		}
	}
	return migs, nil
}

// Migrate applies every pending migration in ascending version order, each in
// its own transaction, and records it in schema_migrations. It is safe to run
// repeatedly (already-applied versions are skipped) and safe to run from
// multiple processes at once (serialized by an advisory lock). Returns the
// versions applied by THIS call (empty if the DB was already up to date).
func Migrate(ctx context.Context, database *sql.DB) ([]int64, error) {
	migs, err := allMigrations()
	if err != nil {
		return nil, err
	}

	// A dedicated connection so the advisory lock and the migrations share one
	// session (advisory locks are session-scoped; the pool must not swap conns).
	conn, err := database.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey) }()

	if err := ensureLedger(ctx, conn); err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}

	var ran []int64
	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		if err := applyOne(ctx, conn, m); err != nil {
			return ran, fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}
		ran = append(ran, m.version)
	}
	return ran, nil
}

// Baseline records every known migration version as applied WITHOUT running any
// SQL. Use it exactly once when adopting this runner on a database that was
// already built by hand (schema.sql + migrations applied via psql), so the
// ledger reflects reality and Migrate() won't try to re-apply non-idempotent
// statements. Returns the versions recorded.
func Baseline(ctx context.Context, database *sql.DB) ([]int64, error) {
	migs, err := allMigrations()
	if err != nil {
		return nil, err
	}
	conn, err := database.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey) }()

	if err := ensureLedger(ctx, conn); err != nil {
		return nil, err
	}
	var recorded []int64
	for _, m := range migs {
		res, err := conn.ExecContext(ctx,
			"INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT (version) DO NOTHING", m.version)
		if err != nil {
			return recorded, fmt.Errorf("baseline version %d: %w", m.version, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			recorded = append(recorded, m.version)
		}
	}
	return recorded, nil
}

func ensureLedger(ctx context.Context, conn *sql.Conn) error {
	const ddl = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    bigint PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`
	if _, err := conn.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create schema_migrations ledger: %w", err)
	}
	return nil
}

func appliedVersions(ctx context.Context, conn *sql.Conn) (map[int64]bool, error) {
	rows, err := conn.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]bool)
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// applyOne runs a single migration and records it, atomically. The migration
// SQL and the ledger insert commit together, so a crash mid-run can never leave
// a version marked applied without its schema change (or vice-versa).
func applyOne(ctx context.Context, conn *sql.Conn, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

	// No args -> lib/pq uses the simple query protocol, which permits the
	// multiple semicolon-separated statements each migration file contains.
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.version); err != nil {
		return err
	}
	return tx.Commit()
}
