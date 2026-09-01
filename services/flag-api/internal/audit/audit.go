// Package audit owns the append-only, hash-chained audit log.
//
// Before AUD-1 four call sites each built the chain themselves, and they did not
// agree: flags.go and scheduler.go hashed six pipe-joined fields, scheduled.go
// hashed only `id + timestamp` with no separator, and none of them held a lock
// while doing SELECT-last-then-INSERT. The consequences were:
//
//   - The chain could not be verified at all, because a given prev_hash might
//     have been produced by either formula and there was no way to tell which.
//   - Concurrent writes to the same flag both read the same "last" row and both
//     wrote a prev_hash pointing at it, forking the chain silently.
//   - The hash was unkeyed, so anyone able to INSERT could also compute a
//     valid-looking chain hash for a forged row.
//
// This package is the single writer. It uses one canonical, length-prefixed
// encoding (so distinct entries can never serialize identically), a keyed HMAC
// (so only a key holder can extend the chain), and an advisory-locked
// transaction per chain (so the read-then-append is atomic).
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tombstone/flag-api/internal/secrets"
)

// advisoryNamespace separates audit-chain advisory locks from any other
// advisory lock in the system (e.g. the migration runner's).
const advisoryNamespace int32 = 0x41554431 // "AUD1"

// Entry is one audit record.
type Entry struct {
	FlagKey     string // "" for events not scoped to a flag (e.g. break-glass)
	Environment string
	Actor       string
	EventType   string
	PrevState   []byte // JSON, nil allowed
	NewState    []byte // JSON, nil allowed
	IPAddress   string
	// ProjectID scopes this entry's chain (TEN-1a-2) — "" for events that are
	// legitimately project-less (break-glass) or written by a caller that
	// predates this field. It is NOT part of canonical()'s hashed content: it
	// is set exclusively by server-side code from a resolved, validated
	// project_id (never client input), so integrity here rests on the same
	// write-path trust every other server-set field (Actor, EventType) already
	// relies on, not on cryptographic commitment. Keeping it out of the hash
	// means canonical()'s existing formula — and every entry_hash already
	// written under it — is completely unaffected by this field's addition.
	ProjectID string
}

// Writer appends entries to the audit log, maintaining the hash chain.
type Writer struct {
	db  *sql.DB
	key *secrets.AuditKey
}

// NewWriter builds the single audit writer.
func NewWriter(db *sql.DB, key *secrets.AuditKey) *Writer {
	return &Writer{db: db, key: key}
}

// Append writes one entry and links it into its chain, atomically.
//
// The chain is scoped per flag_key (matching the existing
// idx_audit_flag_key_ts index and the previous behaviour) so that audit writes
// for different flags do not serialize against each other — important at the
// 5,000-flag scale this system targets. An advisory transaction lock on the
// chain key makes the read-last-then-insert atomic WITHIN a chain, which is what
// eliminates the fork race.
//
// Returns the new entry's id and its self-hash.
func (w *Writer) Append(ctx context.Context, e Entry) (string, string, error) {

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	// Serialize appends to THIS chain. pg_advisory_xact_lock releases
	// automatically at commit/rollback, so a crashed writer cannot wedge it.
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock($1, hashtext($2))`, advisoryNamespace, e.FlagKey); err != nil {
		return "", "", fmt.Errorf("lock audit chain: %w", err)
	}

	// Two things at once, in one round-trip:
	//
	//  1. The tip of this chain. entry_hash is NULL for pre-AUD-1 rows; such a
	//     chain is treated as restarting here, and Verify reports those legacy
	//     rows as unverifiable rather than pretending they check out. A scalar
	//     subquery yields NULL for an empty chain, so there is no ErrNoRows case.
	//     TEN-1a-2: scoped by project_id as well as flag_key — flags.key is
	//     unique only per (project_id, key), so two projects with a same-keyed
	//     flag would otherwise share one chain and could corrupt each other's
	//     prev_hash linkage. The advisory lock below stays keyed by flag_key
	//     alone (unchanged): that only means two different projects' writes to
	//     a same-keyed flag serialize against each other unnecessarily, which
	//     is safe, not a correctness bug — the chain-tip lookup here is what
	//     actually determines correctness, and it IS project-scoped.
	//
	//  2. The states rendered EXACTLY as Postgres will return them. prev_state
	//     and new_state are jsonb, so Postgres reparses the JSON and re-renders
	//     it on read — keys are reordered and `{"a":1}` comes back as
	//     `{"a": 1}`. Hashing the bytes the caller handed us would therefore
	//     produce a hash Verify could never reproduce, and EVERY row would
	//     report as forged. jsonb's text rendering does not depend on any
	//     session setting, so asking Postgres for it is deterministic.
	var prevStateText, newStateText, prevHash sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT $2::jsonb::text, $3::jsonb::text,
		       (SELECT entry_hash FROM audit_log
		         WHERE flag_key IS NOT DISTINCT FROM $1
		           AND project_id IS NOT DISTINCT FROM $4
		         ORDER BY created_at DESC, id DESC
		         LIMIT 1)
	`, nullIfEmpty(e.FlagKey), jsonOrNull(e.PrevState), jsonOrNull(e.NewState), nullIfEmpty(e.ProjectID)).
		Scan(&prevStateText, &newStateText, &prevHash); err != nil {
		return "", "", fmt.Errorf("read chain tip: %w", err)
	}

	// The entry as it will exist on the record — this, not the caller's copy, is
	// what gets hashed and stored, so the two can never disagree.
	stored := e
	stored.PrevState = []byte(prevStateText.String)
	stored.NewState = []byte(newStateText.String)

	id := uuid.New().String()
	// timestamptz stores MICROSECONDS, and the two languages disagree on how to
	// get there: Postgres ROUNDS finer digits (verified — .4917416 stores as
	// .491742) while Go's UnixMicro() TRUNCATES. An untruncated time.Now() would
	// therefore be hashed as ...741 but stored as ...742, and no verifier could
	// ever reproduce the hash from the stored row. Truncating first removes the
	// disagreement: created_at becomes exactly the instant that was hashed.
	//
	// This only reproduces on Linux. darwin's wall clock is already
	// microsecond-granular (time.Now() never yields sub-microsecond digits), so
	// the bug is invisible on a Mac and shows up only in CI.
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	// With no key configured the record is still written, but UNKEYED
	// (entry_hash NULL). Losing an audit record is a worse outcome for an
	// audited system than holding one that cannot be cryptographically verified:
	// the event still needs to be on the record. Verify() reports such rows as
	// "legacy_entries_unverifiable" rather than counting them as intact, so this
	// degradation is visible instead of silent.
	entryHash := ""
	if w.key != nil {
		entryHash = w.key.Sum(canonical(id, stored, createdAt, prevHash.String))
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log
		    (id, flag_key, environment, actor, event_type, prev_state, new_state,
		     ip_address, prev_hash, entry_hash, created_at, project_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, id, nullIfEmpty(e.FlagKey), e.Environment, e.Actor, e.EventType,
		jsonOrNull(stored.PrevState), jsonOrNull(stored.NewState), e.IPAddress,
		nullIfEmpty(prevHash.String), nullIfEmpty(entryHash), createdAt, nullIfEmpty(e.ProjectID)); err != nil {
		return "", "", fmt.Errorf("insert audit entry: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return id, entryHash, nil
}

// canonical builds the injective byte encoding that is hashed.
//
// Every field is length-prefixed ("<len>:<value>") rather than delimiter-joined.
// The previous "|"-joined form was ambiguous: an actor literally named
// `alice|flag_deleted` could shift field boundaries and collide with a different
// legitimate entry. Length prefixes make distinct entries impossible to
// serialize identically.
//
// prev_hash is included so each entry commits to the entire history behind it.
//
// e.PrevState/e.NewState MUST be the states as Postgres renders them from jsonb,
// and createdAt MUST be microsecond-truncated — both callers (Append and Verify)
// pass values in that form. Anything else hashes content the database does not
// hold, and verification of a stored row can then never reproduce the hash.
func canonical(id string, e Entry, createdAt time.Time, prevHash string) []byte {
	var b strings.Builder
	for _, f := range []string{
		id,
		e.FlagKey,
		e.Environment,
		e.Actor,
		e.EventType,
		string(e.PrevState),
		string(e.NewState),
		e.IPAddress,
		// Microsecond Unix epoch — matching timestamptz's storage resolution
		// exactly, so the encoding cannot commit to precision the database will
		// discard. RFC3339Nano encoded nanoseconds Postgres cannot store, which
		// made every row fail its own self-hash on read-back. Rendering the
		// timestamp via Postgres (as the states are) was rejected: unlike jsonb,
		// timestamptz's text output depends on the session's TimeZone and
		// DateStyle — the same flaw as the retired EXTRACT(EPOCH) formula.
		strconv.FormatInt(createdAt.UTC().UnixMicro(), 10),
		prevHash,
	} {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return []byte(b.String())
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func jsonOrNull(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
