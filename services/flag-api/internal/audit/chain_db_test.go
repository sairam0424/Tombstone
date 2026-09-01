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
		// Real UUID literals: audit_log.project_id is a UUID column, and
		// passing a non-UUID string here fails outright against real
		// Postgres ("invalid input syntax for type uuid") — found by an
		// adversarial review actually running this test.
		const projectA = "11111111-1111-1111-1111-111111111111"
		const projectB = "22222222-2222-2222-2222-222222222222"
		const sharedKey = "aud1-shared-key"

		// Interleaved on purpose — A1, B1, A2 — so B1 sits BETWEEN two of
		// project A's entries in created_at order. A regression that groups
		// chains by flag_key alone (instead of (project_id, flag_key)) would
		// make Verify expect A2's prev_hash to match B1's hash, not A1's —
		// this ordering is what makes that regression detectable below.
		eA1 := sampleEntry()
		eA1.FlagKey = sharedKey
		eA1.ProjectID = projectA
		_, hashA1, err := w.Append(ctx, eA1)
		if err != nil {
			t.Fatalf("append A1: %v", err)
		}

		eB1 := sampleEntry()
		eB1.FlagKey = sharedKey
		eB1.ProjectID = projectB
		if _, _, err := w.Append(ctx, eB1); err != nil {
			t.Fatalf("append B1: %v", err)
		}

		eA2 := sampleEntry()
		eA2.FlagKey = sharedKey
		eA2.ProjectID = projectA
		if _, _, err := w.Append(ctx, eA2); err != nil {
			t.Fatalf("append A2: %v", err)
		}

		// Direct proof of Append's chain-tip-lookup fix: B1 must start its OWN
		// chain (prev_hash NULL), and A2 must link to A1 — NOT to B1, which
		// sits between them in wall-clock order.
		var prevHashB1, prevHashA2 sql.NullString
		if err := database.QueryRowContext(ctx, `
			SELECT prev_hash FROM audit_log WHERE flag_key=$1 AND project_id=$2 ORDER BY created_at ASC LIMIT 1
		`, sharedKey, projectB).Scan(&prevHashB1); err != nil {
			t.Fatalf("read B1: %v", err)
		}
		if prevHashB1.Valid && prevHashB1.String != "" {
			t.Fatalf("B1 (first entry for a key project A also has) must start its OWN chain "+
				"(prev_hash NULL), got prev_hash=%q — the chains forked", prevHashB1.String)
		}
		if err := database.QueryRowContext(ctx, `
			SELECT prev_hash FROM audit_log WHERE flag_key=$1 AND project_id=$2 ORDER BY created_at DESC LIMIT 1
		`, sharedKey, projectA).Scan(&prevHashA2); err != nil {
			t.Fatalf("read A2: %v", err)
		}
		if !prevHashA2.Valid || prevHashA2.String != hashA1 {
			t.Fatalf("A2's prev_hash = %q, want project A1's hash %q (must link within its own project, "+
				"not to project B's interleaved entry)", prevHashA2.String, hashA1)
		}

		// Direct proof of Verify's Go-side grouping-key fix: scan the WHOLE
		// log (no SQL project filter) with A1/B1/A2 interleaved in it, and
		// confirm the grouping still keeps each project's chain separate. A
		// regression here would report a link failure at A2 (expecting B1's
		// hash) even though nothing was actually tampered with.
		global, err := w.Verify(ctx, "")
		if err != nil {
			t.Fatalf("verify (global): %v", err)
		}
		if global.FailureCount != 0 {
			t.Fatalf("global verify reports tampering it does not have — the (project_id, flag_key) "+
				"grouping key is not being applied: %+v", global.Failures)
		}

		reportA, err := w.Verify(ctx, projectA)
		if err != nil {
			t.Fatalf("verify project A: %v", err)
		}
		if reportA.FailureCount != 0 {
			t.Fatalf("project A reports tampering it does not have: %+v", reportA.Failures)
		}
		if reportA.TotalEntries != 2 {
			t.Errorf("project A TotalEntries = %d, want exactly 2 (project B's entry must not be counted)",
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
			t.Errorf("project B TotalEntries = %d, want exactly 1 (project A's entries must not be counted)",
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
