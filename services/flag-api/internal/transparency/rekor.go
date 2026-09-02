package transparency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/tombstone/flag-api/internal/secrets"
)

const rekorURL = "https://rekor.sigstore.dev/api/v1/log/entries"

// RekorClient submits audit entry hashes to the Sigstore Rekor transparency log.
// All operations fail-open: a Rekor failure never blocks flag API operations.
type RekorClient struct {
	enabled      bool
	httpClient   *http.Client
	signer       *secrets.RekorSigner
	publicKeyPEM []byte
	// url defaults to rekorURL; overridable only within this package, so
	// tests can point SubmitAuditEntry at a local httptest.Server instead of
	// the real Rekor endpoint.
	url string
}

// NewRekorClient constructs a RekorClient. REKOR_ENABLED=true enables
// submissions, but only when signer is also non-nil — a hashedrekord entry
// cannot be submitted without a real signature and public key (AUD-1b: the
// PREVIOUS version of this client attempted submission with neither, which
// Rekor's server-side validation would have rejected outright — every
// REKOR_ENABLED=true deployment had been silently submitting nothing that
// ever actually landed in the log).
func NewRekorClient(signer *secrets.RekorSigner) *RekorClient {
	enabled := os.Getenv("REKOR_ENABLED") == "true"

	var pubKeyPEM []byte
	if enabled {
		if signer == nil {
			log.Printf("[rekor] warn: REKOR_ENABLED=true but %s is not configured — Rekor submissions disabled", secrets.RekorSigningKeyEnvVar)
			enabled = false
		} else {
			var err error
			pubKeyPEM, err = signer.PublicKeyPEM()
			if err != nil {
				log.Printf("[rekor] warn: marshal rekor public key: %v — Rekor submissions disabled", err)
				enabled = false
			}
		}
	}

	return &RekorClient{
		enabled:      enabled,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		signer:       signer,
		publicKeyPEM: pubKeyPEM,
		url:          rekorURL,
	}
}

// hashedRekordEntry is the JSON payload shape for Rekor's hashedrekord type
// (schema verified live against github.com/sigstore/rekor's
// pkg/types/hashedrekord/v0.0.1). Chosen over the "rekord" type this client
// used previously because hashedrekord submits only a hash of the audit
// entry (never the entry's own prev_state/new_state content), matching this
// integration's actual purpose: prove a hash existed at a point in time,
// not publish the underlying data to a public log.
type hashedRekordEntry struct {
	Kind       string                `json:"kind"`
	APIVersion string                `json:"apiVersion"`
	Spec       hashedRekordEntrySpec `json:"spec"`
}

type hashedRekordEntrySpec struct {
	Data      rekorDataSpec         `json:"data"`
	Signature hashedRekordSignature `json:"signature"`
}

type rekorDataSpec struct {
	Hash rekorHashSpec `json:"hash"`
}

type rekorHashSpec struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

// hashedRekordSignature carries the DER-encoded ECDSA signature (base64)
// over the digest below, plus the PEM-encoded public key (base64) needed to
// verify it — hashedrekord entries are self-verifying: a verifier checks
// the signature against the public key embedded in THIS entry, never
// against whatever key the submitting server currently holds.
type hashedRekordSignature struct {
	Content   string                   `json:"content"`
	PublicKey hashedRekordSignaturePub `json:"publicKey"`
}

type hashedRekordSignaturePub struct {
	Content string `json:"content"`
}

// rekorResponse is the shape of a successful Rekor POST response.
// The response is a map[logID]entryObject; we only care about the first entry.
type rekorEntryResponse struct {
	LogIndex       int64 `json:"logIndex"`
	IntegratedTime int64 `json:"integratedTime"`
}

// SubmitAuditEntry submits the SHA-256 hash of entryJSON to the Rekor transparency log.
//
// Returns:
//   - (logID, logIndex, nil) on success
//   - ("", 0, nil) when REKOR_ENABLED=false — fail-open, caller is unaffected
//
// Any network or parsing error is logged as a warning and swallowed — never
// returned — so callers are never blocked by Rekor unavailability.
func (r *RekorClient) SubmitAuditEntry(ctx context.Context, entryJSON []byte) (logID string, logIndex int64, err error) {
	if !r.enabled {
		return "", 0, nil
	}

	digest := sha256.Sum256(entryJSON)

	// The signature must cover exactly the digest bytes — Rekor's
	// hashedrekord verifier calls Verify(..., options.WithDigest(digest),
	// ...), not Verify over entryJSON itself.
	sig, signErr := r.signer.Sign(digest[:])
	if signErr != nil {
		log.Printf("[rekor] warn: sign entry: %v", signErr)
		return "", 0, nil
	}

	payload := hashedRekordEntry{
		Kind:       "hashedrekord",
		APIVersion: "0.0.1",
		Spec: hashedRekordEntrySpec{
			Data: rekorDataSpec{
				Hash: rekorHashSpec{
					Algorithm: "sha256",
					Value:     hex.EncodeToString(digest[:]),
				},
			},
			Signature: hashedRekordSignature{
				Content: base64.StdEncoding.EncodeToString(sig),
				PublicKey: hashedRekordSignaturePub{
					Content: base64.StdEncoding.EncodeToString(r.publicKeyPEM),
				},
			},
		},
	}

	body, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		log.Printf("[rekor] warn: marshal payload: %v", marshalErr)
		return "", 0, nil
	}

	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if reqErr != nil {
		log.Printf("[rekor] warn: build request: %v", reqErr)
		return "", 0, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, doErr := r.httpClient.Do(req)
	if doErr != nil {
		log.Printf("[rekor] warn: POST failed: %v", doErr)
		return "", 0, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[rekor] warn: unexpected status %d", resp.StatusCode)
		return "", 0, nil
	}

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		log.Printf("[rekor] warn: read response body: %v", readErr)
		return "", 0, nil
	}

	// Response is map[string]rekorEntryResponse where the key is the log UUID.
	var entries map[string]rekorEntryResponse
	if parseErr := json.Unmarshal(respBody, &entries); parseErr != nil {
		log.Printf("[rekor] warn: parse response: %v", parseErr)
		return "", 0, nil
	}

	for id, entry := range entries {
		return id, entry.LogIndex, nil
	}

	log.Printf("[rekor] warn: response contained no entries")
	return "", 0, nil
}
