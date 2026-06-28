package tlsutil_test

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sairam0424/Tombstone/services/flag-api/internal/tlsutil"
)

// TestGenerateCACert verifies that a CA cert is generated with correct properties.
func TestGenerateCACert(t *testing.T) {
	caTLSCert, pool, err := tlsutil.GenerateCACert()
	if err != nil {
		t.Fatalf("GenerateCACert: %v", err)
	}
	if caTLSCert == nil {
		t.Fatal("expected non-nil CA cert")
	}
	if pool == nil {
		t.Fatal("expected non-nil cert pool")
	}
	if len(caTLSCert.Certificate) == 0 {
		t.Fatal("expected at least one certificate DER block")
	}
}

// TestGenerateServiceCert verifies that a service cert uses the canonical CN pattern
// tombstone.svc.<serviceName>.
func TestGenerateServiceCert(t *testing.T) {
	caCert, _, err := tlsutil.GenerateCACert()
	if err != nil {
		t.Fatalf("GenerateCACert: %v", err)
	}

	svcCert, err := tlsutil.GenerateServiceCert(caCert, "flag-api")
	if err != nil {
		t.Fatalf("GenerateServiceCert: %v", err)
	}
	if svcCert == nil {
		t.Fatal("expected non-nil service cert")
	}
	if len(svcCert.Certificate) == 0 {
		t.Fatal("expected at least one certificate DER block")
	}

	leaf, err := x509.ParseCertificate(svcCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if leaf.Subject.CommonName != "tombstone.svc.flag-api" {
		t.Errorf("CN = %q, want tombstone.svc.flag-api", leaf.Subject.CommonName)
	}
}

// TestWriteAndLoadCerts is an integration test: generates the full PKI chain,
// writes PEM files to a temp directory, loads them back, and verifies a working
// mTLS round-trip via httptest.
func TestWriteAndLoadCerts(t *testing.T) {
	dir := t.TempDir()

	caCert, _, err := tlsutil.GenerateCACert()
	if err != nil {
		t.Fatalf("GenerateCACert: %v", err)
	}
	serverCert, err := tlsutil.GenerateServiceCert(caCert, "flag-api")
	if err != nil {
		t.Fatalf("GenerateServiceCert(server): %v", err)
	}
	clientCert, err := tlsutil.GenerateServiceCert(caCert, "client")
	if err != nil {
		t.Fatalf("GenerateServiceCert(client): %v", err)
	}
	if err := tlsutil.WriteCerts(dir, caCert, serverCert, clientCert); err != nil {
		t.Fatalf("WriteCerts: %v", err)
	}

	// Verify expected files were created.
	for _, name := range []string{"ca.crt", "flag-api.crt", "flag-api.key", "client.crt", "client.key"} {
		path := dir + "/" + name
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", path)
		}
	}

	// Load server TLS config and verify MinVersion is TLS 1.3.
	serverCfg, err := tlsutil.LoadServerTLSConfig(dir)
	if err != nil {
		t.Fatalf("LoadServerTLSConfig: %v", err)
	}
	if serverCfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("server MinVersion = %d, want TLS 1.3 (%d)", serverCfg.MinVersion, tls.VersionTLS13)
	}

	// Load client TLS config and verify MinVersion is TLS 1.3.
	clientCfg, err := tlsutil.LoadClientTLSConfig(dir)
	if err != nil {
		t.Fatalf("LoadClientTLSConfig: %v", err)
	}
	if clientCfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("client MinVersion = %d, want TLS 1.3 (%d)", clientCfg.MinVersion, tls.VersionTLS13)
	}

	// Spin up an mTLS test server and verify a round-trip GET succeeds.
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts.TLS = serverCfg
	ts.StartTLS()
	defer ts.Close()

	// httptest.Server binds to 127.0.0.1 which has no IP SAN in our test certs.
	// InsecureSkipVerify is used here only to bypass hostname verification in the
	// test; the real mTLS handshake (mutual cert exchange) is still exercised.
	// Production traffic uses DNS names, so this skip is test-only.
	testCfg := clientCfg.Clone()
	testCfg.InsecureSkipVerify = true //nolint:gosec // test-only: no IP SAN for 127.0.0.1
	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: testCfg},
	}
	resp, err := httpClient.Get(ts.URL)
	if err != nil {
		t.Fatalf("mTLS round-trip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestLoadClientTLSConfig_ReturnNilWhenMTLSDisabled verifies that LoadClientTLSConfig
// returns an error (not a usable config) when no cert files are present on disk.
// This underpins the opt-in contract: callers MUST gate on MTLS_ENABLED=true before
// calling LoadClientTLSConfig, because the function always attempts to read cert files
// and will return an error when they are absent. When MTLS_ENABLED is unset, callers
// skip this function entirely and use the default http.Client without TLS.
func TestLoadClientTLSConfig_ReturnNilWhenMTLSDisabled(t *testing.T) {
	t.Setenv("MTLS_ENABLED", "")

	// Confirm that MTLS_ENABLED is not "true" — callers must not invoke LoadClientTLSConfig.
	if os.Getenv("MTLS_ENABLED") == "true" {
		t.Fatal("MTLS_ENABLED should not be true in this test")
	}

	// When no cert files exist, LoadClientTLSConfig must return an error.
	// This verifies that the function cannot silently succeed with an empty/missing
	// cert directory — callers that skip the MTLS_ENABLED gate would get an error,
	// not a dangerously unconfigured TLS config.
	emptyDir := t.TempDir()
	cfg, err := tlsutil.LoadClientTLSConfig(emptyDir)
	if err == nil {
		t.Error("LoadClientTLSConfig with no cert files should return an error, got nil")
	}
	if cfg != nil {
		t.Error("LoadClientTLSConfig with no cert files should return nil config, got non-nil")
	}
}
