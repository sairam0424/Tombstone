package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
)

// AuditKey keys the audit-log Merkle chain (AUD-1).
//
// The chain previously used a plain sha256 of concatenated fields. An unkeyed
// hash is verifiable by anyone — including an attacker who can INSERT into
// audit_log — so a forged entry could be given a perfectly valid-looking chain
// hash. Keying the hash means a valid chain can only be produced by a party
// holding the key, so tampering is detectable rather than merely inconvenient.
//
// The key is separate from JWT_SECRET, the compliance export key, and the token
// pepper: possessing the audit key must not confer authentication or export
// abilities, and vice versa.
type AuditKey struct {
	key []byte
}

// AuditKeyEnvVar holds the audit-chain HMAC key.
const AuditKeyEnvVar = "AUDIT_HMAC_KEY"

// ErrNoAuditKey reports that no audit key is configured.
var ErrNoAuditKey = errors.New(AuditKeyEnvVar + " is required to key the audit chain (must differ from JWT_SECRET)")

// NewAuditKeyFromEnv loads the audit chain key from the environment.
func NewAuditKeyFromEnv() (*AuditKey, error) {
	return NewAuditKey(os.Getenv(AuditKeyEnvVar), os.Getenv("JWT_SECRET"))
}

// NewAuditKey builds an audit key, rejecting an empty key or one that collides
// with the JWT signing key.
func NewAuditKey(key, jwtSecret string) (*AuditKey, error) {
	if key == "" {
		return nil, ErrNoAuditKey
	}
	if jwtSecret != "" && hmac.Equal([]byte(key), []byte(jwtSecret)) {
		return nil, errors.New(AuditKeyEnvVar + " must not equal JWT_SECRET — audit-chain keys and auth keys must stay separate")
	}
	return &AuditKey{key: []byte(key)}, nil
}

// Sum returns the hex-encoded HMAC-SHA256 of data.
func (a *AuditKey) Sum(data []byte) string {
	mac := hmac.New(sha256.New, a.key)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// Equal compares two chain hashes in constant time.
func (a *AuditKey) Equal(x, y string) bool {
	return hmac.Equal([]byte(x), []byte(y))
}
