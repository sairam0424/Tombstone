package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
)

// ComplianceSigner signs compliance/audit exports.
//
// SEC-4: the export was previously signed with JWT_SECRET — the same key that
// validates authentication JWTs. That made the feature unusable as designed:
// giving an auditor the key needed to VERIFY an export also gave them the key
// needed to MINT authentication tokens for any user. Rotating one key also
// silently invalidated the other's guarantees.
//
// The signing key is now separate, and every signature carries a key id (kid)
// so a key can be rotated without making previously issued exports ambiguous
// about which key signed them.
type ComplianceSigner struct {
	key []byte
	kid string
}

const (
	// ComplianceKeyEnvVar holds the compliance-export signing key. It MUST NOT
	// be the same value as JWT_SECRET.
	ComplianceKeyEnvVar = "COMPLIANCE_SIGNING_KEY"
	// ComplianceKeyIDEnvVar names the active key, emitted as "kid" alongside
	// each signature so verifiers know which key to use after a rotation.
	ComplianceKeyIDEnvVar = "COMPLIANCE_SIGNING_KEY_ID"

	defaultComplianceKeyID = "v1"
)

var (
	// ErrNoComplianceKey reports that no dedicated signing key is configured.
	// Export must fail rather than fall back to JWT_SECRET, which would
	// silently reintroduce the vulnerability.
	ErrNoComplianceKey = errors.New(ComplianceKeyEnvVar + " is required to sign compliance exports (must differ from JWT_SECRET)")

	// ErrComplianceKeyReused reports that the signing key is the JWT signing
	// key, which defeats the separation entirely.
	ErrComplianceKeyReused = errors.New(ComplianceKeyEnvVar + " must not equal JWT_SECRET — a verifier of exports would be able to forge auth tokens")
)

// NewComplianceSignerFromEnv loads the dedicated export signing key. It returns
// an error (rather than degrading to JWT_SECRET) when unset or when it collides
// with the JWT secret.
func NewComplianceSignerFromEnv() (*ComplianceSigner, error) {
	return NewComplianceSigner(
		os.Getenv(ComplianceKeyEnvVar),
		os.Getenv(ComplianceKeyIDEnvVar),
		os.Getenv("JWT_SECRET"),
	)
}

// NewComplianceSigner builds a signer, rejecting a key that is empty or equal to
// the JWT signing key.
func NewComplianceSigner(key, kid, jwtSecret string) (*ComplianceSigner, error) {
	if key == "" {
		return nil, ErrNoComplianceKey
	}
	if jwtSecret != "" && hmac.Equal([]byte(key), []byte(jwtSecret)) {
		return nil, ErrComplianceKeyReused
	}
	if kid == "" {
		kid = defaultComplianceKeyID
	}
	return &ComplianceSigner{key: []byte(key), kid: kid}, nil
}

// KeyID returns the active key id, emitted with each signature.
func (s *ComplianceSigner) KeyID() string { return s.kid }

// New returns a fresh HMAC accumulator for streaming an export body.
func (s *ComplianceSigner) New() hashWriter {
	return hmac.New(sha256.New, s.key)
}

// Sum finalizes an accumulator into a hex signature.
func Sum(h hashWriter) string { return hex.EncodeToString(h.Sum(nil)) }

// hashWriter is the subset of hash.Hash the export path needs. Declared locally
// so callers don't need to import "hash" just to hold the accumulator.
type hashWriter interface {
	Write(p []byte) (int, error)
	Sum(b []byte) []byte
}
