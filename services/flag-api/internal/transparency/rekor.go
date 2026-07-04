package transparency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const rekorURL = "https://rekor.sigstore.dev/api/v1/log/entries"

// RekorClient submits audit entry hashes to the Sigstore Rekor transparency log.
// All operations fail-open: a Rekor failure never blocks flag API operations.
type RekorClient struct {
	enabled    bool
	httpClient *http.Client
}

// NewRekorClient constructs a RekorClient. REKOR_ENABLED=true enables submissions.
func NewRekorClient() *RekorClient {
	return &RekorClient{
		enabled:    os.Getenv("REKOR_ENABLED") == "true",
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// rekorEntry is the JSON payload shape expected by Rekor's rekord type.
type rekorEntry struct {
	Kind       string         `json:"kind"`
	APIVersion string         `json:"apiVersion"`
	Spec       rekorEntrySpec `json:"spec"`
}

type rekorEntrySpec struct {
	Data      rekorDataSpec      `json:"data"`
	Signature rekorSignatureSpec `json:"signature"`
}

type rekorDataSpec struct {
	Hash rekorHashSpec `json:"hash"`
}

type rekorHashSpec struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

type rekorSignatureSpec struct {
	Format string `json:"format"`
}

// rekorResponse is the shape of a successful Rekor POST response.
// The response is a map[logID]entryObject; we only care about the first entry.
type rekorEntryResponse struct {
	LogIndex      int64  `json:"logIndex"`
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

	hash := sha256.Sum256(entryJSON)
	payload := rekorEntry{
		Kind:       "rekord",
		APIVersion: "0.0.1",
		Spec: rekorEntrySpec{
			Data: rekorDataSpec{
				Hash: rekorHashSpec{
					Algorithm: "sha256",
					Value:     fmt.Sprintf("%x", hash),
				},
			},
			Signature: rekorSignatureSpec{
				Format: "x509",
			},
		},
	}

	body, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		log.Printf("[rekor] warn: marshal payload: %v", marshalErr)
		return "", 0, nil
	}

	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, rekorURL, bytes.NewReader(body))
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
