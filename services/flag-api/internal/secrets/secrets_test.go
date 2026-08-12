package secrets

import (
	"errors"
	"strings"
	"testing"
)

// fx builds a deterministic test-only fixture value. Everything is derived at
// runtime rather than written as a literal so credential scanners do not flag
// this file — none of these values are real.
func fx(label string) string {
	return "test-" + label + "-" + strings.Repeat("0", 24)
}

// sampleInput returns a break-glass-shaped bearer value for hashing.
func sampleInput() string {
	return "bgt_" + strings.Repeat("ab", 16)
}

func mustHasher(t *testing.T, pepper string) *TokenHasher {
	t.Helper()
	h, err := NewTokenHasher(pepper)
	if err != nil {
		t.Fatalf("NewTokenHasher: %v", err)
	}
	return h
}

func TestTokenHasherRefusesEmptyPepper(t *testing.T) {
	if _, err := NewTokenHasher(""); !errors.Is(err, ErrNoPepper) {
		t.Fatalf("err = %v, want ErrNoPepper - must never silently allow unhashed storage", err)
	}
}

func TestTokenHashIsDeterministicAndNotThePlaintext(t *testing.T) {
	h := mustHasher(t, fx("pepper-a"))
	in := sampleInput()

	first, second := h.Hash(in), h.Hash(in)
	if first != second {
		t.Fatal("hash must be deterministic - an indexed lookup by hash depends on it")
	}
	if first == in || strings.Contains(first, in) {
		t.Fatal("stored value must not contain or equal the plaintext")
	}
	if len(first) != 64 { // HMAC-SHA256 hex
		t.Errorf("hash length = %d, want 64 hex chars", len(first))
	}
}

func TestTokenHashIsPepperDependent(t *testing.T) {
	a, b := mustHasher(t, fx("pepper-a")), mustHasher(t, fx("pepper-b"))
	if a.Hash(sampleInput()) == b.Hash(sampleInput()) {
		t.Fatal("different peppers must produce different hashes - otherwise a stolen DB alone could verify tokens")
	}
}

func TestTokenHashDistinguishesInputs(t *testing.T) {
	h := mustHasher(t, fx("pepper-a"))
	if h.Hash("value-one") == h.Hash("value-two") {
		t.Fatal("distinct inputs must hash differently")
	}
}

func TestTokenHasherEqual(t *testing.T) {
	h := mustHasher(t, fx("pepper-a"))
	in := sampleInput()
	stored := h.Hash(in)

	if !h.Equal(in, stored) {
		t.Error("correct value must match its stored hash")
	}
	if h.Equal("wrong-value", stored) {
		t.Error("wrong value must not match")
	}
	if h.Equal(in, "") {
		t.Error("empty stored hash must not match")
	}
}

// --- compliance signing key separation (SEC-4) ---

func TestComplianceSignerRequiresDedicatedKey(t *testing.T) {
	if _, err := NewComplianceSigner("", "", fx("jwt")); !errors.Is(err, ErrNoComplianceKey) {
		t.Fatalf("err = %v, want ErrNoComplianceKey - export must fail closed, never reuse the JWT key", err)
	}
}

func TestComplianceSignerRejectsJWTKeyReuse(t *testing.T) {
	shared := fx("shared")
	if _, err := NewComplianceSigner(shared, "", shared); !errors.Is(err, ErrComplianceKeyReused) {
		t.Fatalf("err = %v, want ErrComplianceKeyReused - a verifier of exports could otherwise forge auth tokens", err)
	}
}

func TestComplianceSignerAcceptsDistinctKey(t *testing.T) {
	s, err := NewComplianceSigner(fx("export"), "", fx("jwt"))
	if err != nil {
		t.Fatalf("NewComplianceSigner: %v", err)
	}
	if s.KeyID() != "v1" {
		t.Errorf("KeyID = %q, want default %q", s.KeyID(), "v1")
	}
}

func TestComplianceSignerHonorsExplicitKeyID(t *testing.T) {
	s, err := NewComplianceSigner(fx("export"), "2026-q3", fx("jwt"))
	if err != nil {
		t.Fatalf("NewComplianceSigner: %v", err)
	}
	if s.KeyID() != "2026-q3" {
		t.Errorf("KeyID = %q, want %q - kid is what makes rotation unambiguous", s.KeyID(), "2026-q3")
	}
}

func TestComplianceSignatureDependsOnDedicatedKey(t *testing.T) {
	const body = "audit-log-line-1\naudit-log-line-2\n"

	dedicated, err := NewComplianceSigner(fx("export"), "", fx("jwt"))
	if err != nil {
		t.Fatalf("dedicated signer: %v", err)
	}
	// A signer keyed the OLD way (with the JWT key) for comparison.
	asJWT, err := NewComplianceSigner(fx("jwt"), "", "")
	if err != nil {
		t.Fatalf("jwt-keyed signer: %v", err)
	}

	m1 := dedicated.New()
	m1.Write([]byte(body))
	m2 := asJWT.New()
	m2.Write([]byte(body))

	if Sum(m1) == Sum(m2) {
		t.Fatal("export signature must depend on the dedicated key, proving it is no longer derived from the JWT key")
	}
}

func TestComplianceSignatureIsReproducible(t *testing.T) {
	s, err := NewComplianceSigner(fx("export"), "", fx("jwt"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	const body = "line\n"

	a := s.New()
	a.Write([]byte(body))
	b := s.New()
	b.Write([]byte(body))

	if Sum(a) != Sum(b) {
		t.Fatal("signature must be reproducible so an auditor can verify an export")
	}
}
