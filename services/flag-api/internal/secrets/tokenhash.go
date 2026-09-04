// Package secrets holds the key material and one-way transforms flag-api uses
// for credentials at rest (SEC-4).
//
// Before SEC-4, service tokens and break-glass tokens were stored and compared
// as PLAINTEXT (`WHERE token=$1`), so any read of the database — a backup, a
// replica, a leaked dump, an over-broad SELECT — yielded working production
// credentials. Tokens are now stored only as a keyed hash.
package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
)

// TokenHasher turns a bearer token into the value stored in the database.
//
// Construction: HMAC-SHA256(pepper, token).
//
// Why a keyed hash and not bcrypt/argon2 with a per-row salt: tokens are looked
// up by the token alone (there is no username to locate the row with), so the
// stored value MUST be derivable from the presented token to allow an indexed
// O(1) lookup. A per-row random salt would force a full table scan plus one KDF
// per row on every authenticated request. A slow KDF adds nothing here anyway —
// these tokens are 256 bits of crypto/rand, so offline brute force is already
// infeasible; the threat being closed is "attacker reads the database", and the
// pepper (which never lives in the database) is what closes it.
type TokenHasher struct {
	pepper []byte
}

// ErrNoPepper reports that no pepper was configured. Callers must treat this as
// fatal rather than degrading to plaintext comparison.
var ErrNoPepper = errors.New("TOKEN_HASH_PEPPER is required (32+ bytes recommended) — refusing to store credentials unhashed")

// PepperEnvVar is the environment variable holding the token-hashing pepper.
const PepperEnvVar = "TOKEN_HASH_PEPPER"

// NewTokenHasherFromEnv loads the pepper from the environment. It fails closed:
// without a pepper there is no safe way to store or verify a token, so the
// caller must abort startup rather than silently fall back to plaintext.
func NewTokenHasherFromEnv() (*TokenHasher, error) {
	return NewTokenHasher(os.Getenv(PepperEnvVar))
}

// NewTokenHasher builds a hasher from an explicit pepper.
func NewTokenHasher(pepper string) (*TokenHasher, error) {
	if pepper == "" {
		return nil, ErrNoPepper
	}
	return &TokenHasher{pepper: []byte(pepper)}, nil
}

// Hash returns the hex-encoded HMAC-SHA256 of the token. The result is stable
// for a given pepper, which is what allows an indexed lookup by hash.
func (h *TokenHasher) Hash(token string) string {
	mac := hmac.New(sha256.New, h.pepper)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// Equal compares a presented token against a stored hash in constant time,
// so a timing side channel cannot be used to discover a valid hash byte by byte.
func (h *TokenHasher) Equal(token, storedHash string) bool {
	return hmac.Equal([]byte(h.Hash(token)), []byte(storedHash))
}
