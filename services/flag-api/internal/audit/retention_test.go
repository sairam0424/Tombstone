package audit

import (
	"testing"
	"time"
)

func TestCheckpointCanonicalIsDeterministic(t *testing.T) {
	a := checkpointCanonical("proj-1", "flag-1", "hash-1", fixedTime)
	b := checkpointCanonical("proj-1", "flag-1", "hash-1", fixedTime)
	if string(a) != string(b) {
		t.Fatal("checkpoint encoding must be deterministic or Verify could never reproduce a checkpoint's signature")
	}
}

// TestCheckpointCanonicalIsInjectiveAcrossFieldBoundaries mirrors
// TestCanonicalIsInjectiveAcrossFieldBoundaries in audit_test.go — the same
// length-prefixed encoding, for the same reason: a delimiter-joined encoding
// would let a flag_key/hash value shift field boundaries and make two
// distinct checkpoints serialize (and therefore sign) identically.
func TestCheckpointCanonicalIsInjectiveAcrossFieldBoundaries(t *testing.T) {
	a := checkpointCanonical("proj", "ab", "hash", fixedTime)
	b := checkpointCanonical("proj", "a", "bhash", fixedTime)
	if string(a) == string(b) {
		t.Fatal("distinct checkpoints must never serialize identically")
	}
}

func TestCheckpointCanonicalCoversEveryField(t *testing.T) {
	base := checkpointCanonical("proj-1", "flag-1", "hash-1", fixedTime)

	if string(checkpointCanonical("proj-2", "flag-1", "hash-1", fixedTime)) == string(base) {
		t.Error("changing project_id did not change the encoding")
	}
	if string(checkpointCanonical("proj-1", "flag-2", "hash-1", fixedTime)) == string(base) {
		t.Error("changing flag_key did not change the encoding")
	}
	if string(checkpointCanonical("proj-1", "flag-1", "hash-2", fixedTime)) == string(base) {
		t.Error("changing pruned_through_hash did not change the encoding — a forged checkpoint could vouch for the wrong gap")
	}
	if string(checkpointCanonical("proj-1", "flag-1", "hash-1", fixedTime.Add(time.Second))) == string(base) {
		t.Error("changing pruned_through_created_at did not change the encoding")
	}
}

func TestPartitionNameFormatsYearMonth(t *testing.T) {
	got := partitionName(time.Date(2025, time.January, 15, 3, 4, 5, 0, time.UTC))
	if want := "audit_log_2025_01"; got != want {
		t.Errorf("partitionName = %q, want %q", got, want)
	}

	got = partitionName(time.Date(2025, time.November, 1, 0, 0, 0, 0, time.UTC))
	if want := "audit_log_2025_11"; got != want {
		t.Errorf("partitionName = %q, want %q", got, want)
	}
}

func TestPartitionNamePatternRoundTrips(t *testing.T) {
	name := partitionName(time.Date(2031, time.March, 1, 0, 0, 0, 0, time.UTC))
	m := partitionNamePattern.FindStringSubmatch(name)
	if m == nil {
		t.Fatalf("partitionNamePattern did not match its own generated name %q", name)
	}
	if m[1] != "2031" || m[2] != "03" {
		t.Errorf("parsed (year, month) = (%s, %s), want (2031, 03)", m[1], m[2])
	}

	// The DEFAULT catch-all partition must never look like a monthly
	// partition — discoverArchivablePartitions relies on this to never
	// select it for archiving.
	if partitionNamePattern.MatchString("audit_log_default") {
		t.Error("audit_log_default must not match the monthly partition pattern")
	}
}

func TestPgIdentEscapesEmbeddedQuotes(t *testing.T) {
	got := pgIdent(`audit_log_2025_01`)
	if want := `"audit_log_2025_01"`; got != want {
		t.Errorf("pgIdent = %q, want %q", got, want)
	}

	// Every real caller only ever passes a regex-validated or internally
	// computed name, but the quoting itself must still be correct in
	// isolation — an identifier containing a double quote must have it
	// doubled, the standard Postgres escape, not stripped or left unescaped.
	got = pgIdent(`weird"name`)
	if want := `"weird""name"`; got != want {
		t.Errorf("pgIdent = %q, want %q", got, want)
	}
}

func TestChainKeyDoesNotCollideAcrossTheSeparator(t *testing.T) {
	// "ab" + "" vs "a" + "b" would collide under plain concatenation; the
	// NUL separator is what prevents it.
	a := chainKey("ab", "")
	b := chainKey("a", "b")
	if a == b {
		t.Fatal("chainKey must not collide across the (project_id, flag_key) boundary")
	}
}
