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

	t.Run("DefaultPartitionRowCount surfaces rows stranded outside any monthly partition", func(t *testing.T) {
		projectID := createAuditTestProject(ctx, t, database, "retention-stranded-tenant")
		const flagKey = "retention-stranded-chain"

		// 2005 is deliberately outside every partition any other subtest in
		// this file creates, so this row lands in audit_log_default by
		// construction — simulating EnsurePartitions having fallen behind.
		strandedAt := time.Date(2005, 3, 1, 0, 0, 0, 0, time.UTC)
		appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "alice", EventType: "flag_created", ProjectID: projectID}, strandedAt)

		count, oldest, err := r.DefaultPartitionRowCount(ctx)
		if err != nil {
			t.Fatalf("DefaultPartitionRowCount: %v", err)
		}
		if count < 1 {
			t.Fatalf("count = %d, want at least 1 (the row just inserted with no matching monthly partition)", count)
		}
		if !oldest.Before(time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("oldest = %s, want it to reflect the stranded 2005 row, not a later one", oldest)
		}

		// Archive's report must surface this too — it's the field the loop
		// script actually reads to raise a signal. The cutoff is
		// deliberately earlier than every other subtest's months in this
		// file, so this call archives nothing real and can't collide with
		// partitions another subtest still expects to exist/rename.
		report, err := r.Archive(ctx, time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("archive: %v", err)
		}
		if len(report.PartitionsArchived) != 0 {
			t.Fatalf("expected no partitions archived at this cutoff, got %v", report.PartitionsArchived)
		}
		if report.StrandedInDefaultPartition < 1 {
			t.Fatalf("ArchiveReport.StrandedInDefaultPartition = %d, want at least 1", report.StrandedInDefaultPartition)
		}
		if report.StrandedSince == nil {
			t.Fatal("ArchiveReport.StrandedSince must be set when rows are stranded")
		}
	})

	// Direct reproduction of the tiebreaker gap: two rows in one chain that
	// share an identical created_at (a legitimate tie under coarse
	// wall-clock resolution or load, not just a contrived edge case — see
	// audit.go's own comment on Truncate(time.Microsecond) precision
	// quirks). archiveOne's tip-selection query must pick the SAME row
	// Append's own chain-tip lookup would have used as the real tip, or the
	// checkpoint it seals vouches for the wrong hash.
	t.Run("checkpoint tip-selection breaks a created_at tie the same way Append does", func(t *testing.T) {
		projectID := createAuditTestProject(ctx, t, database, "retention-tie-tenant")
		const flagKey = "retention-tie-chain"

		// A year earlier than every other subtest's months in this file, not
		// merely a distinct one: discoverArchivablePartitions treats its
		// cutoff as "everything chronologically at or before this," so a
		// LATER year's cutoff sweeps up every earlier, still-unarchived
		// partition regardless of which subtest created it — reproduced
		// concretely while developing this test (an earlier attempt used
		// 2021, whose cutoff swept up 2019's leftover partitions and caused
		// a real rename collision two subtests later). An EARLIER year's
		// cutoff can never reach forward into anything, by construction.
		tieAt := time.Date(2010, 1, 10, 9, 0, 0, 0, time.UTC).UTC().Truncate(time.Microsecond)
		recentMonth := time.Date(2010, 3, 10, 9, 0, 0, 0, time.UTC)
		cutoff := time.Date(2010, 2, 1, 0, 0, 0, 0, time.UTC)

		if err := r.EnsurePartitions(ctx, tieAt, 3); err != nil { // Jan..Apr 2010
			t.Fatalf("EnsurePartitions: %v", err)
		}

		// Two rows, identical created_at, distinct ids — inserted directly
		// (not via appendAtForTest) so which one Append's real tip lookup
		// (ORDER BY created_at DESC, id DESC) would pick is controlled and
		// known: idHigh.
		const idLow = "00000000-0000-0000-0000-000000000001"
		const idHigh = "ffffffff-ffff-ffff-ffff-ffffffffffff"
		eLow := Entry{FlagKey: flagKey, Environment: "production", Actor: "alice", EventType: "flag_created", ProjectID: projectID}
		eHigh := Entry{FlagKey: flagKey, Environment: "production", Actor: "alice", EventType: "flag_environment_updated", ProjectID: projectID}
		hashLow := key.Sum(canonical(idLow, eLow, tieAt, ""))
		hashHigh := key.Sum(canonical(idHigh, eHigh, tieAt, hashLow))

		for _, row := range []struct{ id, actor, eventType, prevHash, entryHash string }{
			{idLow, eLow.Actor, eLow.EventType, "", hashLow},
			{idHigh, eHigh.Actor, eHigh.EventType, hashLow, hashHigh},
		} {
			if _, err := database.ExecContext(ctx, `
				INSERT INTO audit_log (id, flag_key, environment, actor, event_type, prev_hash, entry_hash, created_at, project_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			`, row.id, flagKey, "production", row.actor, row.eventType, nullIfEmpty(row.prevHash), row.entryHash, tieAt, projectID); err != nil {
				t.Fatalf("insert tied row %s: %v", row.id, err)
			}
		}

		// A real successor row: appendAtForTest's chain-tip lookup uses the
		// same ORDER BY as Append itself, so its prev_hash correctly ends up
		// as hashHigh — this is the ground truth a checkpoint must match.
		appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "bob", EventType: "flag_archived", ProjectID: projectID}, recentMonth)

		if _, err := r.Archive(ctx, cutoff); err != nil {
			t.Fatalf("archive: %v", err)
		}

		var storedHash string
		if err := database.QueryRowContext(ctx, `
			SELECT pruned_through_hash FROM audit_retention_checkpoints
			WHERE project_id = $1 AND flag_key = $2
		`, projectID, flagKey).Scan(&storedHash); err != nil {
			t.Fatalf("read checkpoint: %v", err)
		}
		if storedHash != hashHigh {
			t.Fatalf("checkpointed hash = %q, want %q (the id-DESC tie winner, matching Append's own tip-lookup order) — "+
				"a mismatched checkpoint would make Verify falsely report tampering against the row appended after this tie",
				storedHash, hashHigh)
		}

		report, err := w.Verify(ctx, projectID)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.FailureCount != 0 || !report.Intact {
			t.Fatalf("a created_at tie within an archived partition must not produce a false tampering report: %+v", report)
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

	// A forged checkpoint row is the OTHER half of the threat model
	// checkpointExplains's own doc comment states: audit_retention_
	// checkpoints' RULEs block UPDATE/DELETE but not INSERT, so a party able
	// to write bogus rows there must still be unable to make a real gap
	// verify without holding AUDIT_HMAC_KEY.
	t.Run("a checkpoint with a bogus signature does not explain a gap", func(t *testing.T) {
		projectID := createAuditTestProject(ctx, t, database, "retention-forged-checkpoint-tenant")
		const flagKey = "retention-forged-checkpoint-chain"

		month1 := time.Date(2019, 6, 10, 9, 0, 0, 0, time.UTC)
		month2 := time.Date(2019, 8, 10, 9, 0, 0, 0, time.UTC)

		if err := r.EnsurePartitions(ctx, month1, 3); err != nil { // Jun..Sep 2019
			t.Fatalf("EnsurePartitions: %v", err)
		}

		rHash := appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "alice", EventType: "flag_created", ProjectID: projectID}, month1)
		appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "bob", EventType: "flag_archived", ProjectID: projectID}, month2)

		// A row that WOULD explain the gap by (chain, hash) lookup, but whose
		// signature was never actually produced by AUDIT_HMAC_KEY — exactly
		// what an attacker limited to INSERT (not UPDATE/DELETE) could write.
		if _, err := database.ExecContext(ctx, `
			INSERT INTO audit_retention_checkpoints
			    (project_id, flag_key, pruned_through_hash, pruned_through_created_at, signature)
			VALUES ($1, $2, $3, $4, 'not-a-real-hmac-signature')
		`, projectID, flagKey, rHash, month1.UTC().Truncate(time.Microsecond)); err != nil {
			t.Fatalf("insert forged checkpoint: %v", err)
		}

		name := partitionName(time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC))
		if _, err := database.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE audit_log DETACH PARTITION %s`, pgIdent(name))); err != nil {
			t.Fatalf("detach: %v", err)
		}

		report, err := w.Verify(ctx, projectID)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.FailureCount == 0 {
			t.Fatal("a checkpoint row present but with a signature that doesn't verify must NOT explain the gap — real tampering could otherwise be waved through by writing any bogus checkpoint row")
		}
		if report.Intact {
			t.Fatal("Intact must be false when the only checkpoint for a gap has a bad signature")
		}
	})

	// Direct reproduction of the race the checkpoint-decision advisory lock
	// in archiveOne exists to close: a real Append landing between
	// survivingChains' snapshot and the transaction's DETACH must not be
	// able to make a legitimately-continuing chain look tampered with.
	t.Run("a concurrent append racing an archive does not produce a false tampering report", func(t *testing.T) {
		projectID := createAuditTestProject(ctx, t, database, "retention-race-tenant")
		const flagKey = "retention-race-chain"

		oldMonth := time.Date(2019, 9, 10, 9, 0, 0, 0, time.UTC)
		cutoff := time.Date(2019, 10, 1, 0, 0, 0, 0, time.UTC)

		if err := r.EnsurePartitions(ctx, oldMonth, 3); err != nil { // Sep..Dec 2019
			t.Fatalf("EnsurePartitions: %v", err)
		}

		rHash := appendAtForTest(ctx, t, w, Entry{FlagKey: flagKey, Environment: "production", Actor: "alice", EventType: "flag_created", ProjectID: projectID}, oldMonth)

		lockAcquired := make(chan struct{})
		proceed := make(chan struct{})
		appendDone := make(chan error, 1)

		// Plays the role of a real, concurrent Writer.Append: takes the
		// SAME advisory lock Append itself takes, holds it while blocked on
		// `proceed` (simulating "in the middle of its transaction"), then
		// inserts the successor row and commits — releasing the lock.
		go func() {
			raceTx, err := database.BeginTx(ctx, nil)
			if err != nil {
				appendDone <- err
				return
			}
			defer func() { _ = raceTx.Rollback() }()

			if _, err := raceTx.ExecContext(ctx,
				`SELECT pg_advisory_xact_lock($1, hashtext($2))`, advisoryNamespace, flagKey); err != nil {
				appendDone <- err
				return
			}
			close(lockAcquired)
			<-proceed

			id := uuid.New().String()
			entry := Entry{FlagKey: flagKey, Environment: "production", Actor: "bob", EventType: "flag_archived", ProjectID: projectID}
			createdAt := time.Date(2019, 12, 10, 9, 0, 0, 0, time.UTC).UTC().Truncate(time.Microsecond)
			entryHash := key.Sum(canonical(id, entry, createdAt, rHash))

			if _, err := raceTx.ExecContext(ctx, `
				INSERT INTO audit_log (id, flag_key, environment, actor, event_type, prev_hash, entry_hash, created_at, project_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			`, id, flagKey, entry.Environment, entry.Actor, entry.EventType, rHash, entryHash, createdAt, projectID); err != nil {
				appendDone <- err
				return
			}
			appendDone <- raceTx.Commit()
		}()

		<-lockAcquired

		archiveDone := make(chan struct{})
		var archiveReport ArchiveReport
		var archiveErr error
		go func() {
			archiveReport, archiveErr = r.Archive(ctx, cutoff)
			close(archiveDone)
		}()

		// Best-effort: give Archive's goroutine real wall-clock time to reach
		// and block on the same advisory lock before it's released. The
		// test's correctness does not depend on this actually happening —
		// Postgres's lock serializes the commit before the DETACH either
		// way — but it makes the interesting contended path likely, not just
		// possible.
		time.Sleep(200 * time.Millisecond)
		close(proceed)

		if err := <-appendDone; err != nil {
			t.Fatalf("simulated concurrent append: %v", err)
		}
		<-archiveDone
		if archiveErr != nil {
			t.Fatalf("archive: %v", archiveErr)
		}
		if archiveReport.CheckpointsWritten < 1 {
			t.Fatalf("expected the race-winning append's chain to still get a checkpoint, got %d checkpoints written (partitions archived: %v)",
				archiveReport.CheckpointsWritten, archiveReport.PartitionsArchived)
		}

		report, err := w.Verify(ctx, projectID)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.FailureCount != 0 || !report.Intact {
			t.Fatalf("a legitimate concurrent append racing an archive must not produce a false tampering report: %+v", report)
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
