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

// Entry is one audit record. Every field participates in the hash.
type Entry struct {
	FlagKey     string // "" for events not scoped to a flag (e.g. break-glass)
	Environment string
	Actor       string
	EventType   string
	PrevState   []byte // JSON, nil allowed
	NewState    []byte // JSON, nil allowed
	IPAddress   string
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

	// The tip of this chain. entry_hash is NULL for pre-AUD-1 rows; such a chain
	// is treated as restarting here, and Verify reports those legacy rows as
	// unverifiable rather than pretending they check out.
	var prevHash sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT entry_hash FROM audit_log
		WHERE flag_key IS NOT DISTINCT FROM $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, nullIfEmpty(e.FlagKey)).Scan(&prevHash)
	if err != nil && err != sql.ErrNoRows {
		return "", "", fmt.Errorf("read chain tip: %w", err)
	}

	id := uuid.New().String()
	createdAt := time.Now().UTC()

	// With no key configured the record is still written, but UNKEYED
	// (entry_hash NULL). Losing an audit record is a worse outcome for an
	// audited system than holding one that cannot be cryptographically verified:
	// the event still needs to be on the record. Verify() reports such rows as
	// "legacy_entries_unverifiable" rather than counting them as intact, so this
	// degradation is visible instead of silent.
	entryHash := ""
	if w.key != nil {
		entryHash = w.key.Sum(canonical(id, e, createdAt, prevHash.String))
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log
		    (id, flag_key, environment, actor, event_type, prev_state, new_state,
		     ip_address, prev_hash, entry_hash, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`, id, nullIfEmpty(e.FlagKey), e.Environment, e.Actor, e.EventType,
		jsonOrNull(e.PrevState), jsonOrNull(e.NewState), e.IPAddress,
		nullIfEmpty(prevHash.String), nullIfEmpty(entryHash), createdAt); err != nil {
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
		// RFC3339Nano in UTC — a stable, unambiguous instant. The old formula
		// used EXTRACT(EPOCH) as a float string, whose text form varies with
		// Postgres settings and silently loses sub-microsecond precision.
		createdAt.UTC().Format(time.RFC3339Nano),
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
