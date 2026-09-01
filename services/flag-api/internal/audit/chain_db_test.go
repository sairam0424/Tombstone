package audit

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/tombstone/flag-api/internal/db"
)

// TestChainAgainstPostgres is the executable gate for AUD-1. It runs against a
// real Postgres in the flag-api-migrations CI job and skips locally.
//
// Subtest ORDER matters: audit_log blocks DELETE by rule, so a forged row cannot
// be cleaned up and permanently breaks global verification. Every "chain is
// intact" assertion therefore runs BEFORE the tampering subtests.
func TestChainAgainstPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed audit chain test")
	}

	database, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Ensure the schema (incl. migration 015's entry_hash column) exists.
	if _, err := db.Migrate(ctx, database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	w := NewWriter(database, testKey(t))

	t.Run("append builds a chain that verifies", func(t *testing.T) {
		for i, ev := range []string{"flag_created", "flag_environment_updated", "flag_archived"} {
			e := sampleEntry()
			e.FlagKey = "aud1-chain"
			e.EventType = ev
			if _, _, err := w.Append(ctx, e); err != nil {
				t.Fatalf("append %d: %v", i, err)
			}
		}

		report, err := w.Verify(ctx, "")
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.FailureCount != 0 {
			t.Fatalf("FailureCount = %d, want 0; failures: %+v", report.FailureCount, report.Failures)
		}
		if report.VerifiedEntries < 3 {
			t.Fatalf("VerifiedEntries = %d, want >= 3", report.VerifiedEntries)
		}
		if !report.Intact {
			t.Fatalf("Intact = false, want true (note: %q)", report.Note)
		}
	})

	// Regression for the second half of the round-trip bug: prev_state/new_state
	// are jsonb, so Postgres reparses the JSON and re-renders it on read — keys
	// come back in jsonb's own order and separators gain a space. Hashing the
	// caller's bytes instead of that rendering made every row fail its own
	// self-hash. These states are written in a form jsonb will NOT return verbatim.
	t.Run("states reformatted by jsonb still verify", func(t *testing.T) {
		e := sampleEntry()
		e.FlagKey = "aud1-jsonb"
		// Keys deliberately out of jsonb's output order, with extra whitespace.
		e.PrevState = []byte(`{ "zebra":1,   "alpha": {"b":2,"a":1} }`)
		e.NewState = []byte(`{"zebra":2,"alpha":{"a":1,"b":2}}`)

		if _, _, err := w.Append(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}

		report, err := w.Verify(ctx, "")
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.FailureCount != 0 {
			t.Fatalf("jsonb reformatting must not read as tampering: %d failure(s): %+v",
				report.FailureCount, report.Failures)
		}
	})

	// The fork race: two writers previously both read the same chain tip and both
	// wrote a prev_hash pointing at it. If the advisory lock were absent, these
	// concurrent appends would produce entries whose prev_hash does not match
	// their predecessor, and Verify would report link failures.
	t.Run("concurrent appends to one chain do not fork it", func(t *testing.T) {
		const writers = 8
		var wg sync.WaitGroup
		errs := make(chan error, writers)

		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				e := sampleEntry()
				e.FlagKey = "aud1-concurrent"
				e.EventType = "flag_environment_updated"
				if _, _, err := w.Append(ctx, e); err != nil {
					errs <- err
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent append failed: %v", err)
		}

		report, err := w.Verify(ctx, "")
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.FailureCount != 0 {
			t.Fatalf("chain forked under %d concurrent writers: %d failure(s): %+v",
				writers, report.FailureCount, report.Failures)
		}
	})

	// TEN-1a-2: flags.key is unique only per (project_id, key), so two
	// projects can legitimately have a flag with the identical key string.
	// Before this fix, Append's chain-tip lookup matched flag_key alone, so
	// project B's entry below would have linked its prev_hash to project A's
	// tip — merging two tenants' audit trails into one chain.
	t.Run("chains with the same flag_key in different projects do not fork each other", func(t *testing.T) {
		const projectA = "aud1-tenant-a"
		const projectB = "aud1-tenant-b"
		const sharedKey = "aud1-shared-key"

		eA := sampleEntry()
		eA.FlagKey = sharedKey
		eA.ProjectID = projectA
		if _, _, err := w.Append(ctx, eA); err != nil {
			t.Fatalf("append to project A: %v", err)
		}

		eB := sampleEntry()
		eB.FlagKey = sharedKey
		eB.ProjectID = projectB
		if _, _, err := w.Append(ctx, eB); err != nil {
			t.Fatalf("append to project B: %v", err)
		}

		var prevHashB sql.NullString
		if err := database.QueryRowContext(ctx, `
			SELECT prev_hash FROM audit_log
			WHERE flag_key=$1 AND project_id=$2
			ORDER BY created_at ASC LIMIT 1
		`, sharedKey, projectB).Scan(&prevHashB); err != nil {
			t.Fatalf("read project B's first entry: %v", err)
		}
		if prevHashB.Valid && prevHashB.String != "" {
			t.Fatalf("project B's first entry for a key project A also has must start its OWN chain "+
				"(prev_hash NULL), got prev_hash=%q — the chains forked", prevHashB.String)
		}

		reportA, err := w.Verify(ctx, projectA)
		if err != nil {
			t.Fatalf("verify project A: %v", err)
		}
		if reportA.FailureCount != 0 {
			t.Fatalf("project A reports tampering it does not have: %+v", reportA.Failures)
		}
		if reportA.TotalEntries != 1 {
			t.Errorf("project A TotalEntries = %d, want exactly 1 (project B's entry must not be counted)",
				reportA.TotalEntries)
		}

		reportB, err := w.Verify(ctx, projectB)
		if err != nil {
			t.Fatalf("verify project B: %v", err)
		}
		if reportB.FailureCount != 0 {
			t.Fatalf("project B reports tampering it does not have: %+v", reportB.Failures)
		}
		if reportB.TotalEntries != 1 {
			t.Errorf("project B TotalEntries = %d, want exactly 1 (project A's entry must not be counted)",
				reportB.TotalEntries)
		}
	})

	t.Run("legacy rows are reported unverifiable rather than intact", func(t *testing.T) {
		// A pre-AUD-1 row: no entry_hash. Nobody can recompute it.
		if _, err := database.ExecContext(ctx, `
			INSERT INTO audit_log (flag_key, environment, actor, event_type, prev_hash, entry_hash)
			VALUES ('aud1-legacy', 'production', 'legacy-actor', 'flag_created', 'deadbeef', NULL)
		`); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}

		report, err := w.Verify(ctx, "")
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.LegacyEntries < 1 {
			t.Fatal("a row without entry_hash must be counted as legacy/unverifiable")
		}
		// A legacy row is not a FAILURE — it is simply outside the verified span.
		if report.FailureCount != 0 {
			t.Fatalf("legacy rows must not be reported as tampering: %+v", report.Failures)
		}
		if report.Note == "" {
			t.Error("report must explain that some entries are excluded from verification")
		}
	})

	// This is the threat model: audit_log blocks UPDATE and DELETE, but INSERT is
	// permitted, so an attacker fabricates a row. Without the key they cannot
	// compute a valid entry_hash, so verification must catch it.
	t.Run("a forged inserted row is detected", func(t *testing.T) {
		if _, err := database.ExecContext(ctx, `
			INSERT INTO audit_log (flag_key, environment, actor, event_type, prev_hash, entry_hash)
			VALUES ('aud1-forged', 'production', 'mallory', 'flag_environment_updated',
			        'aaaa', 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff')
		`); err != nil {
			t.Fatalf("insert forged row: %v", err)
		}

		report, err := w.Verify(ctx, "")
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if report.FailureCount < 1 {
			t.Fatal("a row with a bogus entry_hash must be reported as a failure")
		}
		if report.Intact {
			t.Fatal("Intact must be false once any entry fails verification")
		}

		found := false
		for _, f := range report.Failures {
			if f.FlagKey == "aud1-forged" {
				found = true
			}
		}
		if !found {
			t.Errorf("the forged row must be named in the failures list; got %+v", report.Failures)
		}
	})
}
