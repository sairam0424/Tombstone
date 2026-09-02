package transparency

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tombstone/flag-api/internal/secrets"
)

// genTestSigner builds a *secrets.RekorSigner from a freshly generated key —
// never a literal fixture.
func genTestSigner(t *testing.T) *secrets.RekorSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	signer, err := secrets.NewRekorSigner(string(pemKey))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return signer
}

// TestNewRekorClient_DisabledByDefault is the fail-open baseline: no
// REKOR_ENABLED at all must never attempt a submission, regardless of
// whether a signer is configured.
func TestNewRekorClient_DisabledByDefault(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer server.Close()

	client := NewRekorClient(genTestSigner(t))
	client.url = server.URL

	logID, logIndex, err := client.SubmitAuditEntry(context.Background(), []byte(`{"a":1}`))
	if err != nil || logID != "" || logIndex != 0 {
		t.Fatalf("got (%q, %d, %v), want (\"\", 0, nil)", logID, logIndex, err)
	}
	if hit {
		t.Error("SubmitAuditEntry made an HTTP call while disabled")
	}
}

// TestNewRekorClient_EnabledWithoutSignerDisables is the direct regression
// proof for AUD-1b: REKOR_ENABLED=true with no signing key configured must
// NOT fall back to submitting a signature-less entry (the previous bug) —
// it must disable submission entirely instead.
func TestNewRekorClient_EnabledWithoutSignerDisables(t *testing.T) {
	t.Setenv("REKOR_ENABLED", "true")

	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer server.Close()

	client := NewRekorClient(nil)
	client.url = server.URL

	logID, logIndex, err := client.SubmitAuditEntry(context.Background(), []byte(`{"a":1}`))
	if err != nil || logID != "" || logIndex != 0 {
		t.Fatalf("got (%q, %d, %v), want (\"\", 0, nil)", logID, logIndex, err)
	}
	if hit {
		t.Error("SubmitAuditEntry made an HTTP call with REKOR_ENABLED=true but no signer configured")
	}
}

// TestSubmitAuditEntry_SubmitsAVerifiableHashedRekordEntry is the core
// AUD-1b regression proof: the entry actually sent over the wire is
// shaped exactly as Rekor's hashedrekord v0.0.1 type expects, and its
// signature genuinely verifies against its own embedded public key and the
// digest of entryJSON — not a placeholder, not empty, not wrong-shaped.
func TestSubmitAuditEntry_SubmitsAVerifiableHashedRekordEntry(t *testing.T) {
	t.Setenv("REKOR_ENABLED", "true")

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("read request body: %v", readErr)
		}
		gotBody = body

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"24296fb24b8ad77a": {"logIndex": 42, "integratedTime": 1700000000}}`)
	}))
	defer server.Close()

	client := NewRekorClient(genTestSigner(t))
	client.url = server.URL

	entryJSON := []byte(`{"id":"abc","event_type":"flag_updated"}`)
	logID, logIndex, err := client.SubmitAuditEntry(context.Background(), entryJSON)
	if err != nil {
		t.Fatalf("SubmitAuditEntry: %v", err)
	}
	if logID != "24296fb24b8ad77a" || logIndex != 42 {
		t.Errorf("got (%q, %d), want (\"24296fb24b8ad77a\", 42)", logID, logIndex)
	}

	var sent hashedRekordEntry
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if sent.Kind != "hashedrekord" {
		t.Errorf("kind = %q, want hashedrekord", sent.Kind)
	}
	if sent.APIVersion != "0.0.1" {
		t.Errorf("apiVersion = %q, want 0.0.1", sent.APIVersion)
	}
	if sent.Spec.Data.Hash.Algorithm != "sha256" {
		t.Errorf("hash algorithm = %q, want sha256", sent.Spec.Data.Hash.Algorithm)
	}
	wantDigest := sha256.Sum256(entryJSON)
	if sent.Spec.Data.Hash.Value != hex.EncodeToString(wantDigest[:]) {
		t.Errorf("hash value = %q, want %q (sha256 of entryJSON)", sent.Spec.Data.Hash.Value, hex.EncodeToString(wantDigest[:]))
	}

	// The core AUD-1b proof: decode exactly what a real Rekor server would
	// decode, and verify it with the SAME primitives Rekor's hashedrekord
	// v0.0.1 entry.go uses (ecdsa.VerifyASN1-equivalent verification against
	// the raw digest bytes).
	sigBytes, err := base64.StdEncoding.DecodeString(sent.Spec.Signature.Content)
	if err != nil {
		t.Fatalf("decode signature content: %v", err)
	}
	pubPEM, err := base64.StdEncoding.DecodeString(sent.Spec.Signature.PublicKey.Content)
	if err != nil {
		t.Fatalf("decode public key content: %v", err)
	}
	block, _ := pem.Decode(pubPEM)
	if block == nil {
		t.Fatal("embedded public key is not valid PEM")
	}
	parsedPub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse embedded public key: %v", err)
	}
	pub, ok := parsedPub.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("embedded public key type = %T, want *ecdsa.PublicKey", parsedPub)
	}
	if !ecdsa.VerifyASN1(pub, wantDigest[:], sigBytes) {
		t.Fatal("the submitted signature does not verify against the submitted digest and public key — this is exactly the bug AUD-1b fixes")
	}
}

func TestSubmitAuditEntry_NonOKStatusFailsOpen(t *testing.T) {
	t.Setenv("REKOR_ENABLED", "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewRekorClient(genTestSigner(t))
	client.url = server.URL

	logID, logIndex, err := client.SubmitAuditEntry(context.Background(), []byte(`{"a":1}`))
	if err != nil || logID != "" || logIndex != 0 {
		t.Fatalf("got (%q, %d, %v), want (\"\", 0, nil) — a non-2xx status must fail open, not error", logID, logIndex, err)
	}
}

func TestSubmitAuditEntry_MalformedResponseFailsOpen(t *testing.T) {
	t.Setenv("REKOR_ENABLED", "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `not valid json`)
	}))
	defer server.Close()

	client := NewRekorClient(genTestSigner(t))
	client.url = server.URL

	logID, logIndex, err := client.SubmitAuditEntry(context.Background(), []byte(`{"a":1}`))
	if err != nil || logID != "" || logIndex != 0 {
		t.Fatalf("got (%q, %d, %v), want (\"\", 0, nil) — a malformed response must fail open, not error", logID, logIndex, err)
	}
}

func TestSubmitAuditEntry_NetworkFailureFailsOpen(t *testing.T) {
	t.Setenv("REKOR_ENABLED", "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	unreachableURL := server.URL
	server.Close() // closed before use — guarantees a connection failure

	client := NewRekorClient(genTestSigner(t))
	client.url = unreachableURL

	logID, logIndex, err := client.SubmitAuditEntry(context.Background(), []byte(`{"a":1}`))
	if err != nil || logID != "" || logIndex != 0 {
		t.Fatalf("got (%q, %d, %v), want (\"\", 0, nil) — a network failure must fail open, not error", logID, logIndex, err)
	}
}
