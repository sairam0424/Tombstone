package db

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// expectedVersions is the full set the runner must apply: 1 = schema.sql
// baseline, then each migrations/NNN_*.sql prefix. Update this alongside any
// new migration so the runner test stays a real regression gate.
var expectedVersions = []int64{1, 2, 3, 4, 5, 10, 11, 12, 13, 14, 15, 16, 17}

// TestMigrationRunner exercises the runner against a REAL Postgres. It is the
// executable gate for DATA-1 and runs in CI (the flag-api-migrations job sets
// TEST_DATABASE_URL to a fresh pgvector-enabled Postgres). It skips locally when
// TEST_DATABASE_URL is unset so `go test ./...` needs no database.
//
// This test requires a PRISTINE database: the first subtest asserts that every
// version gets applied, which only holds if nothing migrated it first. Any other
// DB-backed package sharing TEST_DATABASE_URL must therefore run AFTER this one,
// never concurrently — see the sequential `go test` invocations in ci.yml.
func TestMigrationRunner(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed migration test")
	}

	database, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := database.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	t.Run("fresh apply runs every version in order", func(t *testing.T) {
		applied, err := Migrate(ctx, database)
		if err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		if !equalVersions(applied, expectedVersions) {
			t.Fatalf("applied = %v, want %v", applied, expectedVersions)
		}
		if n := countLedger(ctx, t, database); n != len(expectedVersions) {
			t.Fatalf("schema_migrations has %d rows, want %d", n, len(expectedVersions))
		}
		// Spot-check that the baseline actually created core tables.
		for _, table := range []string{"flags", "flag_environments", "audit_log", "schema_migrations"} {
			if !tableExists(ctx, t, database, table) {
				t.Fatalf("expected table %q to exist after migrate", table)
			}
		}
	})

	t.Run("re-run is a no-op (idempotent)", func(t *testing.T) {
		applied, err := Migrate(ctx, database)
		if err != nil {
			t.Fatalf("Migrate (2nd): %v", err)
		}
		if len(applied) != 0 {
			t.Fatalf("second Migrate applied %v, want none", applied)
		}
	})

	t.Run("baseline adopts an already-built DB without re-running SQL", func(t *testing.T) {
		// Simulate a hand-built DB: tables exist but the ledger is gone.
		if _, err := database.ExecContext(ctx, "DROP TABLE schema_migrations"); err != nil {
			t.Fatalf("drop ledger: %v", err)
		}
		recorded, err := Baseline(ctx, database)
		if err != nil {
			t.Fatalf("Baseline: %v", err)
		}
		if !equalVersions(recorded, expectedVersions) {
			t.Fatalf("baseline recorded = %v, want %v", recorded, expectedVersions)
		}
		// After baselining, Migrate must run nothing (no non-idempotent re-apply).
		applied, err := Migrate(ctx, database)
		if err != nil {
			t.Fatalf("Migrate after baseline: %v", err)
		}
		if len(applied) != 0 {
			t.Fatalf("Migrate after baseline applied %v, want none", applied)
		}
	})
}

func equalVersions(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func countLedger(ctx context.Context, t *testing.T, database *sql.DB) int {
	t.Helper()
	var n int
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	return n
}

func tableExists(ctx context.Context, t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	err := database.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists)
	if err != nil {
		t.Fatalf("table check %q: %v", name, err)
	}
	return exists
}
