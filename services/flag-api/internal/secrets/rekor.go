package secrets

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
)

// RekorSigner holds the ECDSA private key used to sign Sigstore Rekor
// transparency-log submissions (AUD-1b).
//
// Separate from every other key in this package (JWT_SECRET,
// TOKEN_HASH_PEPPER, COMPLIANCE_SIGNING_KEY, AUDIT_HMAC_KEY) for the same
// reason those are separated from each other: a Rekor entry is PUBLIC —
// anyone can query rekor.sigstore.dev for it — so this key's public half is
// deliberately exposed by design, and its blast radius must never overlap
// with a key that also protects request auth, token hashing, compliance
// export authenticity, or audit-chain integrity. It's also the first
// ASYMMETRIC key in this package: every other one here is a symmetric HMAC
// key, but Rekor's hashedrekord verification is done by third parties who
// never share a secret with this server, so it requires a public/private
// keypair, not a shared secret.
type RekorSigner struct {
	key *ecdsa.PrivateKey
}

// RekorSigningKeyEnvVar names the env var holding this signer's key: a
// PEM-encoded PKCS#8 ECDSA private key. Literal "\n" sequences are accepted
// in place of real newlines, since most single-line .env / secret-manager
// formats can't hold a real multi-line PEM block directly.
const RekorSigningKeyEnvVar = "REKOR_SIGNING_KEY"

// ErrNoRekorSigningKey reports that no signing key is configured. This is
// NOT fatal to any caller: Rekor submission has always been optional and
// fail-open (REKOR_ENABLED gates whether it's even attempted) — a
// deployment that never sets this key simply never actually submits to
// Rekor, the same as if REKOR_ENABLED were false.
var ErrNoRekorSigningKey = errors.New(RekorSigningKeyEnvVar + " is required to sign Rekor transparency-log submissions")

// NewRekorSignerFromEnv loads the signing key from REKOR_SIGNING_KEY.
func NewRekorSignerFromEnv() (*RekorSigner, error) {
	return NewRekorSigner(os.Getenv(RekorSigningKeyEnvVar))
}

// NewRekorSigner parses a PEM-encoded PKCS#8 ECDSA private key.
func NewRekorSigner(pemKey string) (*RekorSigner, error) {
	if pemKey == "" {
		return nil, ErrNoRekorSigningKey
	}
	normalized := strings.ReplaceAll(pemKey, `\n`, "\n")
	block, _ := pem.Decode([]byte(normalized))
	if block == nil {
		return nil, fmt.Errorf("%s is not valid PEM", RekorSigningKeyEnvVar)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", RekorSigningKeyEnvVar, err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s must be a PKCS#8-encoded ECDSA private key, got %T", RekorSigningKeyEnvVar, parsed)
	}
	return &RekorSigner{key: key}, nil
}

// Sign produces a DER-encoded ECDSA signature over digest — a raw hash (e.g.
// SHA-256), NOT the original content. Rekor's hashedrekord entry type
// verifies the signature via `Verify(..., options.WithDigest(digest), ...)`,
// so the signature must cover exactly the digest bytes the entry's
// data.hash.value also records, not the pre-hash content.
func (s *RekorSigner) Sign(digest []byte) ([]byte, error) {
	return ecdsa.SignASN1(rand.Reader, s.key, digest)
}

// PublicKeyPEM returns the PEM-encoded public key to embed in each Rekor
// entry. hashedrekord entries are self-verifying: a verifier reading a
// stored entry checks its signature against the public key embedded in
// THAT SAME entry, never against "whatever key this server currently
// holds" — so this key is free to rotate; historical entries stay
// verifiable against whichever key actually signed them.
func (s *RekorSigner) PublicKeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(&s.key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal rekor public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}
