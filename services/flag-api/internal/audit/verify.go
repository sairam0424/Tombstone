package audit

import (
	"context"
	"fmt"
	"time"
)

// VerifyReport is the computed integrity result for the audit log.
//
// It deliberately separates VERIFIED rows from LEGACY rows. Entries written
// before AUD-1 have no entry_hash and were produced by two mutually
// incompatible formulas, so their chain links cannot be recomputed by anyone.
// Reporting them as "unverifiable" is the honest answer; the previous behaviour
// was to hardcode merkle_chain_integrity=true regardless of any of this.
type VerifyReport struct {
	Intact bool `json:"intact"`

	TotalEntries    int `json:"total_entries"`
	VerifiedEntries int `json:"verified_entries"`
	LegacyEntries   int `json:"legacy_entries_unverifiable"`

	// Chains examined (one per distinct flag_key).
	ChainsChecked int `json:"chains_checked"`

	// Failures found, capped so a badly broken log cannot produce an unbounded
	// response. FailureCount is the true total.
	FailureCount int             `json:"failure_count"`
	Failures     []VerifyFailure `json:"failures"`

	CheckedAt string `json:"checked_at"`
	Note      string `json:"note,omitempty"`
}

// VerifyFailure identifies one broken link.
type VerifyFailure struct {
	EntryID string `json:"entry_id"`
	FlagKey string `json:"flag_key"`
	Reason  string `json:"reason"`
}

const maxReportedFailures = 50

// Verify recomputes every keyed hash in the audit log and checks that each entry
// links to its predecessor. It is read-only.
//
// projectID scopes both which rows are examined AND how chains are grouped:
// chains are per (project_id, flag_key), not flag_key alone (TEN-1a-2) —
// flags.key is unique only per (project_id, key), so two projects with a
// same-keyed flag would otherwise be treated as one chain. Pass "" for the
// unscoped, whole-log view (compliance evidence's system-wide integrity
// figure); the VIEWER-accessible /audit/verify HTTP route always passes the
// caller's own resolved project_id, so a project cannot learn about (or
// receive a false-tampering report contaminated by) another project's chains.
//
// Intact is true only when there are zero failures AND at least one entry was
// actually verified — an empty or entirely-legacy log must not be reported as
// cryptographically intact.
func (w *Writer) Verify(ctx context.Context, projectID string) (VerifyReport, error) {
	report := VerifyReport{CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if w.key == nil {
		return report, fmt.Errorf("audit writer has no signing key")
	}

	// Ordered by (project_id, flag_key), then position within the chain, so a
	// single pass can walk each chain in sequence.
	rows, err := w.db.QueryContext(ctx, `
		SELECT id, COALESCE(flag_key,''), COALESCE(environment,''), actor, event_type,
		       COALESCE(prev_state::text,''), COALESCE(new_state::text,''),
		       COALESCE(ip_address,''), COALESCE(prev_hash,''), COALESCE(entry_hash,''),
		       created_at, COALESCE(project_id::text,'')
		FROM audit_log
		WHERE $1 = '' OR project_id::text = $1
		ORDER BY COALESCE(project_id::text,''), COALESCE(flag_key,''), created_at ASC, id ASC
	`, projectID)
	if err != nil {
		return report, fmt.Errorf("read audit log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var currentChain string
	var expectedPrev string
	first := true

	for rows.Next() {
		var id, flagKey, env, actor, eventType, prevState, newState, ip, prevHash, entryHash, rowProjectID string
		var createdAt time.Time
		if err := rows.Scan(&id, &flagKey, &env, &actor, &eventType,
			&prevState, &newState, &ip, &prevHash, &entryHash, &createdAt, &rowProjectID); err != nil {
			return report, err
		}
		report.TotalEntries++

		// Chain identity is (project_id, flag_key) — "\x00" as a separator
		// cannot appear in a UUID string or a flag key, so this concatenation
		// cannot collide two distinct pairs onto the same chain key.
		chainKey := rowProjectID + "\x00" + flagKey
		if first || chainKey != currentChain {
			currentChain = chainKey
			expectedPrev = ""
			report.ChainsChecked++
			first = false
		}

		// Pre-AUD-1 row: no keyed hash exists, so nothing can be recomputed.
		// It also breaks the link for the row after it, so the chain restarts.
		if entryHash == "" {
			report.LegacyEntries++
			expectedPrev = ""
			continue
		}

		// 1. Does the row's own hash match its contents?
		want := w.key.Sum(canonical(id, Entry{
			FlagKey:     flagKey,
			Environment: env,
			Actor:       actor,
			EventType:   eventType,
			PrevState:   []byte(prevState),
			NewState:    []byte(newState),
			IPAddress:   ip,
		}, createdAt, prevHash))

		if !w.key.Equal(want, entryHash) {
			report.addFailure(id, flagKey, "entry_hash does not match the row contents — the row was altered or forged")
			expectedPrev = entryHash
			continue
		}

		// 2. Does it link to the previous verified entry in this chain?
		// expectedPrev == "" means this is a chain start (or follows a legacy
		// row), so any prev_hash is accepted as the genesis of the verified span.
		if expectedPrev != "" && !w.key.Equal(prevHash, expectedPrev) {
			report.addFailure(id, flagKey,
				"prev_hash does not match the preceding entry — an entry was removed, reordered, or inserted")
		}

		report.VerifiedEntries++
		expectedPrev = entryHash
	}
	if err := rows.Err(); err != nil {
		return report, err
	}

	report.Intact = report.FailureCount == 0 && report.VerifiedEntries > 0

	switch {
	case report.TotalEntries == 0:
		report.Note = "audit log is empty — nothing to verify, so integrity is not asserted"
	case report.VerifiedEntries == 0:
		report.Note = "every entry predates AUD-1 keyed hashing and cannot be verified; integrity is not asserted"
	case report.LegacyEntries > 0:
		report.Note = fmt.Sprintf(
			"%d entry/entries predate AUD-1 keyed hashing and are excluded from verification; the %d keyed entries verify cleanly",
			report.LegacyEntries, report.VerifiedEntries)
	}

	return report, nil
}

func (r *VerifyReport) addFailure(id, flagKey, reason string) {
	r.FailureCount++
	if len(r.Failures) < maxReportedFailures {
		r.Failures = append(r.Failures, VerifyFailure{EntryID: id, FlagKey: flagKey, Reason: reason})
	}
}

// HasKey reports whether chain verification is possible at all. Without a key
// there is nothing to recompute, which is a configuration state (503) rather
// than a server error (500).
func (w *Writer) HasKey() bool { return w != nil && w.key != nil }

// CountEntries returns the total number of audit rows. Used by the compliance
// evidence endpoint so it no longer needs its own query.
func (w *Writer) CountEntries(ctx context.Context) (int, error) {
	var n int
	err := w.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&n)
	return n, err
}
