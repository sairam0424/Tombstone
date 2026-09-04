package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tombstone/flag-api/internal/db/sqlcgen"
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
	//
	// The filter passes projectID through as a NULLABLE parameter rather than
	// comparing "$1 = '' OR project_id::text = $1": casting project_id to
	// text and comparing it against the raw request string is sensitive to
	// UUID formatting (case, e.g.) that the native uuid `=` operator
	// normalizes away — a caller whose resolved project_id differs only in
	// case from the stored value would silently see zero rows instead of
	// their own (DATA-1b PR 2/4 found and fixed exactly this regression in
	// breakglass.sql's ConsumeBreakGlassToken). The explicit ::uuid cast is
	// required, not cosmetic: without it, lib/pq cannot determine $1's type
	// from a bare NULL parameter and Postgres rejects the query outright
	// ("could not determine data type of parameter $1") — found by CI, not
	// reproducible with a non-NULL value, which is exactly why an explicit
	// type is safer than relying on inference from context.
	checkpoints, err := w.loadCheckpoints(ctx, projectID)
	if err != nil {
		return report, fmt.Errorf("read retention checkpoints: %w", err)
	}

	rows, err := sqlcgen.New(w.db).ListAuditLogForVerification(ctx, sql.NullString{String: projectID, Valid: projectID != ""})
	if err != nil {
		return report, fmt.Errorf("read audit log: %w", err)
	}

	var currentChain string
	var expectedPrev string
	first := true

	for _, row := range rows {
		id, flagKey, env, actor, eventType := row.ID, row.FlagKey, row.Environment, row.Actor, row.EventType
		prevState, newState, ip, prevHash, entryHash, rowProjectID := row.PrevStateText, row.NewStateText, row.IpAddress, row.PrevHash, row.EntryHash, row.ProjectIDText
		createdAt := row.CreatedAt
		report.TotalEntries++

		// Chain identity is (project_id, flag_key) — see chainKey's doc
		// comment in retention.go for why this concatenation cannot collide
		// two distinct pairs.
		thisChain := chainKey(rowProjectID, flagKey)
		if first || thisChain != currentChain {
			currentChain = thisChain
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
		//
		// expectedPrev == "" means this is a chain start in THIS scan (or
		// follows a legacy row) — but a genuine genesis or post-legacy row
		// always has its OWN prev_hash empty too (Append reads the chain tip
		// from the live table either way, and finds nothing to point at). A
		// non-empty prev_hash here means real predecessor rows existed and
		// are simply no longer in this result set: either archived (a
		// retention checkpoint explains exactly this hash) or deleted
		// (nothing explains it — flag it rather than silently accept it as
		// a "fresh genesis", which is what this code did before checkpoints
		// existed).
		switch {
		case expectedPrev == "" && prevHash != "":
			if !w.checkpointExplains(checkpoints, thisChain, prevHash) {
				report.addFailure(id, flagKey,
					"prev_hash refers to an entry that is no longer present, and no valid retention checkpoint explains the gap — possible tampering")
			}
		case expectedPrev != "" && !w.key.Equal(prevHash, expectedPrev):
			report.addFailure(id, flagKey,
				"prev_hash does not match the preceding entry — an entry was removed, reordered, or inserted")
		}

		report.VerifiedEntries++
		expectedPrev = entryHash
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

// checkpointRecord is one sealed retention checkpoint, as needed to
// recompute and check its signature.
type checkpointRecord struct {
	projectID, flagKey, prunedThroughHash string
	prunedThroughCreatedAt                time.Time
	signature                             string
}

// loadCheckpoints reads every retention checkpoint in scope, keyed by
// (chain, the hash it explains) so resolving a gap in the row-scan loop
// above costs one map lookup rather than a query per gap. Scoped by
// projectID the same way the main row query is, for the same reason
// (TEN-1a-2): a project must never learn about, or be affected by, another
// project's checkpoints.
func (w *Writer) loadCheckpoints(ctx context.Context, projectID string) (map[string]checkpointRecord, error) {
	rows, err := sqlcgen.New(w.db).ListAuditRetentionCheckpoints(ctx, sql.NullString{String: projectID, Valid: projectID != ""})
	if err != nil {
		return nil, err
	}

	out := map[string]checkpointRecord{}
	for _, row := range rows {
		c := checkpointRecord{
			projectID:              row.ProjectIDText,
			flagKey:                row.FlagKey,
			prunedThroughHash:      row.PrunedThroughHash,
			prunedThroughCreatedAt: row.PrunedThroughCreatedAt,
			signature:              row.Signature,
		}
		out[checkpointMapKey(chainKey(c.projectID, c.flagKey), c.prunedThroughHash)] = c
	}
	return out, nil
}

// checkpointMapKey combines a chain identity with the specific hash a
// checkpoint explains — a chain can accumulate many checkpoints over
// successive retention runs, so the hash must be part of the key, not just
// the chain.
func checkpointMapKey(chain, prunedThroughHash string) string {
	return chain + "\x00" + prunedThroughHash
}

// checkpointExplains reports whether a valid, signed checkpoint accounts for
// a chain's row referring to gapHash as its prev_hash with no predecessor in
// this scan. Recomputing the signature (rather than trusting the stored row
// at face value) matters exactly as it does for entry_hash: RULEs on
// audit_retention_checkpoints block UPDATE/DELETE, not INSERT, so a party
// able to write bogus rows there must still be unable to make one verify
// without holding AUDIT_HMAC_KEY.
func (w *Writer) checkpointExplains(checkpoints map[string]checkpointRecord, chain, gapHash string) bool {
	c, ok := checkpoints[checkpointMapKey(chain, gapHash)]
	if !ok {
		return false
	}
	want := w.key.Sum(checkpointCanonical(c.projectID, c.flagKey, c.prunedThroughHash, c.prunedThroughCreatedAt))
	return w.key.Equal(want, c.signature)
}

// CountEntries returns the total number of audit rows. Used by the compliance
// evidence endpoint so it no longer needs its own query.
func (w *Writer) CountEntries(ctx context.Context) (int, error) {
	n, err := sqlcgen.New(w.db).CountAuditEntries(ctx)
	return int(n), err
}
