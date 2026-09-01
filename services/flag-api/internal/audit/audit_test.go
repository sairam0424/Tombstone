package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/tombstone/flag-api/internal/secrets"
)

func testKey(t *testing.T) *secrets.AuditKey {
	t.Helper()
	k, err := secrets.NewAuditKey("audit-test-"+strings.Repeat("0", 24), "")
	if err != nil {
		t.Fatalf("audit key: %v", err)
	}
	return k
}

var fixedTime = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

func sampleEntry() Entry {
	return Entry{
		FlagKey:     "checkout-v2",
		Environment: "production",
		Actor:       "alice",
		EventType:   "flag_environment_updated",
		PrevState:   []byte(`{"enabled":false}`),
		NewState:    []byte(`{"enabled":true}`),
		IPAddress:   "10.0.0.1",
	}
}

func TestCanonicalIsDeterministic(t *testing.T) {
	a := canonical("id-1", sampleEntry(), fixedTime, "prev")
	b := canonical("id-1", sampleEntry(), fixedTime, "prev")
	if string(a) != string(b) {
		t.Fatal("canonical encoding must be deterministic or verification can never reproduce a hash")
	}
}

// TestCanonicalIsInjectiveAcrossFieldBoundaries is the real reason for
// length-prefixing. The retired implementation joined fields with "|", so a field
// whose VALUE contained "|" could shift the boundaries and make two genuinely
// different entries serialize identically — a forgery vector.
func TestCanonicalIsInjectiveAcrossFieldBoundaries(t *testing.T) {
	// Under the old "|"-join both of these produce ...|alice|flag_deleted|...
	e1 := sampleEntry()
	e1.Actor = "alice|flag_deleted"
	e1.EventType = "flag_created"

	e2 := sampleEntry()
	e2.Actor = "alice"
	e2.EventType = "flag_deleted|flag_created"

	if string(canonical("id-1", e1, fixedTime, "p")) == string(canonical("id-1", e2, fixedTime, "p")) {
		t.Fatal("distinct entries must never serialize identically — field boundaries must be unambiguous")
	}
}

func TestCanonicalCoversEveryField(t *testing.T) {
	base := sampleEntry()
	baseHash := testKey(t).Sum(canonical("id-1", base, fixedTime, "prev"))

	mutations := map[string]func(*Entry){
		"flag_key":    func(e *Entry) { e.FlagKey = "other" },
		"environment": func(e *Entry) { e.Environment = "staging" },
		"actor":       func(e *Entry) { e.Actor = "mallory" },
		"event_type":  func(e *Entry) { e.EventType = "flag_archived" },
		"prev_state":  func(e *Entry) { e.PrevState = []byte(`{"enabled":true}`) },
		"new_state":   func(e *Entry) { e.NewState = []byte(`{"enabled":false}`) },
		"ip_address":  func(e *Entry) { e.IPAddress = "10.9.9.9" },
	}

	key := testKey(t)
	for field, mutate := range mutations {
		mutated := base
		mutate(&mutated)
		if key.Sum(canonical("id-1", mutated, fixedTime, "prev")) == baseHash {
			t.Errorf("changing %s did not change the hash — that field is not covered and could be tampered with undetected", field)
		}
	}

	// Identity, timestamp and chain position must also be covered.
	if key.Sum(canonical("id-2", base, fixedTime, "prev")) == baseHash {
		t.Error("changing the entry id did not change the hash")
	}
	if key.Sum(canonical("id-1", base, fixedTime.Add(time.Second), "prev")) == baseHash {
		t.Error("changing created_at did not change the hash")
	}
	if key.Sum(canonical("id-1", base, fixedTime, "different-prev")) == baseHash {
		t.Error("changing prev_hash did not change the hash — entries would not commit to their history")
	}
}

// TestCanonicalDoesNotHashProjectID documents a deliberate choice (TEN-1a-2),
// not an oversight: ProjectID scopes reads/chain grouping but is set
// exclusively by server-side code from validated context, never client
// input, so it does not need cryptographic commitment the way attacker-
// reachable fields do. Keeping it out of canonical() also means every
// entry_hash already written before this field existed remains valid and
// reproducible under the unchanged formula — changing this would be a
// backward-incompatible hash-format change, exactly the kind of risk this
// test exists to catch if someone "fixes" it later without re-deriving why.
func TestCanonicalDoesNotHashProjectID(t *testing.T) {
	base := sampleEntry()
	baseHash := testKey(t).Sum(canonical("id-1", base, fixedTime, "prev"))

	mutated := base
	mutated.ProjectID = "some-other-project"
	if got := testKey(t).Sum(canonical("id-1", mutated, fixedTime, "prev")); got != baseHash {
		t.Error("canonical() must not hash ProjectID — see the comment on Entry.ProjectID for why")
	}
}

// TestCanonicalIsMicrosecondQuantized pins a real bug that shipped past all the
// in-memory tests above and was caught only by TestChainAgainstPostgres:
// created_at is a timestamptz, which stores MICROSECONDS, but the encoding used
// RFC3339Nano and committed to nanoseconds Postgres rounds away. The value read
// back therefore hashed differently from the value written, and every single row
// reported as forged. The encoding must not depend on precision the database
// cannot hold.
func TestCanonicalIsMicrosecondQuantized(t *testing.T) {
	withNanos := fixedTime.Add(1500 * time.Nanosecond)
	asStored := withNanos.Truncate(time.Microsecond)

	if string(canonical("id-1", sampleEntry(), withNanos, "p")) !=
		string(canonical("id-1", sampleEntry(), asStored, "p")) {
		t.Fatal("sub-microsecond precision must not affect the hash — Postgres discards it, so verification could never reproduce it")
	}

	// A whole microsecond IS stored, so it must still be covered.
	if string(canonical("id-1", sampleEntry(), asStored, "p")) ==
		string(canonical("id-1", sampleEntry(), asStored.Add(time.Microsecond), "p")) {
		t.Fatal("a one-microsecond difference must change the hash — that is the resolution created_at is stored at")
	}
}

// TestHashIsKeyed proves the chain is not forgeable by someone who merely knows
// the algorithm: without the key they cannot produce a matching hash.
func TestHashIsKeyed(t *testing.T) {
	k1, err := secrets.NewAuditKey("key-one-"+strings.Repeat("0", 24), "")
	if err != nil {
		t.Fatalf("key1: %v", err)
	}
	k2, err := secrets.NewAuditKey("key-two-"+strings.Repeat("0", 24), "")
	if err != nil {
		t.Fatalf("key2: %v", err)
	}
	enc := canonical("id-1", sampleEntry(), fixedTime, "prev")
	if k1.Sum(enc) == k2.Sum(enc) {
		t.Fatal("hash must depend on the key, otherwise anyone who can INSERT can forge a valid chain")
	}
}

func TestAuditKeyRejectsEmptyAndJWTReuse(t *testing.T) {
	if _, err := secrets.NewAuditKey("", ""); err == nil {
		t.Error("empty audit key must be rejected")
	}
	shared := "shared-" + strings.Repeat("0", 24)
	if _, err := secrets.NewAuditKey(shared, shared); err == nil {
		t.Error("audit key equal to the JWT key must be rejected — key separation is the point")
	}
}

// TestUnkeyedWriterStillRecords documents the degraded-mode decision: with no
// key the audit record is still written (unkeyed), because losing an audit record
// is worse for an audited system than holding one that cannot be verified.
// Verify() surfaces such rows as unverifiable instead of counting them as intact.
func TestUnkeyedWriterIsUsableButCannotVerify(t *testing.T) {
	unkeyed := NewWriter(nil, nil)
	if unkeyed.HasKey() {
		t.Fatal("a writer with no key must report HasKey()==false so callers can return 503")
	}

	keyed := NewWriter(nil, testKey(t))
	if !keyed.HasKey() {
		t.Fatal("a writer with a key must report HasKey()==true")
	}
}
