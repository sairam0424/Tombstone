package audit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/tombstone/flag-api/internal/db"
)

// TestRetentionAgainstPostgres is the executable gate for DATA-2. It runs
// against a real Postgres in the flag-api-migrations CI job and skips
// locally, matching TestChainAgainstPostgres (chain_db_test.go).
//
// Every date here is deliberately in the past (2019) and namespaced with a
// "retention-" prefix — real audit_log rows written by any OTHER test in
// this package or a future run land in whichever month partition their real
// created_at falls into, never 2019, so this file's own archiving can never
// accidentally sweep up unrelated data.
func TestRetentionAgainstPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed retention test")
	}

	database, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := db.Migrate(ctx, database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	key := testKey(t)
	w := NewWriter(database, key)
	r := NewRetention(database, key)

	t.Run("EnsurePartitions creates the requested monthly partitions, idempotently", func(t *testing.T) {
		from := time.Date(2019, 6, 10, 0, 0, 0, 0, time.UTC)
		if err := r.EnsurePartitions(ctx, from, 2); err != nil {
			t.Fatalf("EnsurePartitions: %v", err)
		}
		for _, name := range []string{"audit_log_2019_06", "audit_log_2019_07", "audit_log_2019_08"} {
			if !regclassExists(ctx, t, database, name) {
				t.Fatalf("expected partition %q to exist", name)
			}
		}
		if err := r.EnsurePartitions(ctx, from, 2); err != nil {
			t.Fatalf("EnsurePartitions (2nd call, must be idempotent): %v", err)
		}
	})

	t.Run("archiving a chain's oldest rows preserves verifiability via a checkpoint", func(t *testing.T) {
		projectID := createAuditTestProject(ctx, t, database, "retention-continuing-tenant")
		const flagKey = "retention-continuing-chain"

		oldMonth := time.Date(2019, 1, 10, 9, 0, 0, 0, time.UTC)
		recentMonth := time.Date(2019, 4, 10, 9, 0, 0, 0, time.UTC)
		cutoff := time.Date(2019, 3, 1, 0, 0, 0, 0, time.UTC) // archives Jan (+ empty Feb), not Apr

		if err := r.EnsurePartitions(ctx, oldMonth, 4); err != nil { // Jan..May 2019
			t.Fatalf("EnsurePartitions: %v", err)
		}

		appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "alice", EventType: "flag_created", ProjectID: projectID}, oldMonth)
		appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "alice", EventType: "flag_environment_updated", ProjectID: projectID}, oldMonth.Add(24*time.Hour))
		appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "alice", EventType: "flag_environment_updated", ProjectID: projectID}, oldMonth.Add(48*time.Hour))
		appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "bob", EventType: "flag_archived", ProjectID: projectID}, recentMonth)

		before, err := w.Verify(ctx, projectID)
		if err != nil {
			t.Fatalf("verify before archive: %v", err)
		}
		if before.FailureCount != 0 || !before.Intact {
			t.Fatalf("chain must verify intact BEFORE archiving: %+v", before)
		}
		if before.TotalEntries != 4 {
			t.Fatalf("TotalEntries = %d, want 4", before.TotalEntries)
		}

		report, err := r.Archive(ctx, cutoff)
		if err != nil {
			t.Fatalf("archive: %v", err)
		}
		if report.CheckpointsWritten < 1 {
			t.Fatalf("expected at least one checkpoint written for the continuing chain, got %d", report.CheckpointsWritten)
		}
		foundJan := false
		for _, p := range report.PartitionsArchived {
			if p == "audit_log_2019_01" {
				foundJan = true
			}
		}
		if !foundJan {
			t.Fatalf("expected audit_log_2019_01 to be archived, got %v", report.PartitionsArchived)
		}

		after, err := w.Verify(ctx, projectID)
		if err != nil {
			t.Fatalf("verify after archive: %v", err)
		}
		if after.TotalEntries != 1 {
			t.Fatalf("TotalEntries after archive = %d, want 1 (three archived, one remaining)", after.TotalEntries)
		}
		if after.FailureCount != 0 {
			t.Fatalf("a legitimately archived chain must NOT be reported as tampered: %+v", after.Failures)
		}
		if !after.Intact {
			t.Fatalf("Intact = false after a legitimate archive, note=%q failures=%+v", after.Note, after.Failures)
		}

		var archivedCount int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log_2019_01_archived`).Scan(&archivedCount); err != nil {
			t.Fatalf("query archived partition: %v", err)
		}
		if archivedCount != 3 {
			t.Fatalf("audit_log_2019_01_archived has %d rows, want 3 (the data must survive archiving, not be destroyed)", archivedCount)
		}

		// no_archive_delete (DO INSTEAD NOTHING) makes a DELETE succeed with
		// zero rows affected, not error — the same convention as
		// no_audit_delete on the live table. The row count, not the error
		// return, is the real proof the guard was reapplied.
		if _, err := database.ExecContext(ctx, `DELETE FROM audit_log_2019_01_archived`); err != nil {
			t.Fatalf("DELETE against an archived partition should be a no-op, not an error: %v", err)
		}
		var afterDelete int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log_2019_01_archived`).Scan(&afterDelete); err != nil {
			t.Fatalf("query archived partition after DELETE attempt: %v", err)
		}
		if afterDelete != 3 {
			t.Fatalf("archived partition has %d rows after a DELETE attempt, want 3 unchanged — the archive-only RULE was not reapplied", afterDelete)
		}
	})

	t.Run("a gap with no checkpoint is reported as tampering, not silently accepted", func(t *testing.T) {
		projectID := createAuditTestProject(ctx, t, database, "retention-tamper-tenant")
		const flagKey = "retention-tampered-chain"

		month1 := time.Date(2019, 2, 10, 9, 0, 0, 0, time.UTC)
		month2 := time.Date(2019, 4, 10, 9, 0, 0, 0, time.UTC)

		if err := r.EnsurePartitions(ctx, month1, 3); err != nil { // Feb..May 2019
			t.Fatalf("EnsurePartitions: %v", err)
		}

		appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "alice", EventType: "flag_created", ProjectID: projectID}, month1)
		appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "bob", EventType: "flag_archived", ProjectID: projectID}, month2)

		// Detach+rename the OLD partition directly, bypassing
		// Retention.Archive entirely — simulating an attacker (or a bug)
		// with enough privilege to remove rows without ever sealing a
		// checkpoint. This is exactly the scenario a checkpoint exists to
		// catch: without it, Verify's pre-DATA-2 behavior silently accepted
		// ANY missing predecessor as a fresh genesis.
		name := partitionName(time.Date(2019, 2, 1, 0, 0, 0, 0, time.UTC))
		if _, err := database.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE audit_log DETACH PARTITION %s`, pgIdent(name))); err != nil {
			t.Fatalf("detach without checkpoint: %v", err)
		}

		report, err := w.Verify(ctx, projectID)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.FailureCount == 0 {
			t.Fatal("an unexplained gap (detach with no checkpoint) must be reported as a failure, not silently accepted as a fresh genesis")
		}
		if report.Intact {
			t.Fatal("Intact must be false when a gap has no checkpoint")
		}
	})
}

// appendAtForTest inserts a real, correctly-keyed audit entry at an explicit
// createdAt. Writer.Append always uses time.Now(), so retention tests build
// multi-month chains this way instead of waiting real months. Duplicates
// Append's chain-tip-then-insert rather than changing Append's signature —
// no production caller needs an injectable timestamp, only this test — and
// deliberately skips Append's jsonb round-trip handling for prev_state/
// new_state, since every entry here leaves both nil.
func appendAtForTest(ctx context.Context, t *testing.T, w *Writer, e Entry, createdAt time.Time) string {
	t.Helper()
	createdAt = createdAt.UTC().Truncate(time.Microsecond)

	var prevHash sql.NullString
	err := w.db.QueryRowContext(ctx, `
		SELECT entry_hash FROM audit_log
		WHERE flag_key IS NOT DISTINCT FROM $1 AND project_id IS NOT DISTINCT FROM $2
		ORDER BY created_at DESC, id DESC LIMIT 1
	`, nullIfEmpty(e.FlagKey), nullIfEmpty(e.ProjectID)).Scan(&prevHash)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("read chain tip: %v", err)
	}

	id := uuid.New().String()
	entryHash := w.key.Sum(canonical(id, e, createdAt, prevHash.String))

	if _, err := w.db.ExecContext(ctx, `
		INSERT INTO audit_log
		    (id, flag_key, environment, actor, event_type, prev_state, new_state,
		     ip_address, prev_hash, entry_hash, created_at, project_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, id, nullIfEmpty(e.FlagKey), e.Environment, e.Actor, e.EventType,
		jsonOrNull(e.PrevState), jsonOrNull(e.NewState), e.IPAddress,
		nullIfEmpty(prevHash.String), nullIfEmpty(entryHash), createdAt, nullIfEmpty(e.ProjectID),
	); err != nil {
		t.Fatalf("insert backdated entry: %v", err)
	}
	return entryHash
}

func regclassExists(ctx context.Context, t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	if err := database.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		t.Fatalf("to_regclass check: %v", err)
	}
	return exists
}
