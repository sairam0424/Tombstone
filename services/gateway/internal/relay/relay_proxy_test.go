package relay

import (
	"testing"

	"go.uber.org/zap"
)

// newTestLogger returns a no-op zap logger suitable for unit tests.
func newTestLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	if err != nil {
		t.Fatalf("create test logger: %v", err)
	}
	return logger
}

// TestRelayConfigDefaults verifies that when Port is left empty the relay
// applies the hardcoded default port "8090".
func TestRelayConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := RelayConfig{
		GatewayURL:  "http://gateway:8080",
		Environment: "production",
		// Port intentionally omitted
	}

	if got := cfg.effectivePort(); got != defaultPort {
		t.Errorf("effectivePort() = %q, want %q", got, defaultPort)
	}

	// Also verify that an explicit port is preserved as-is.
	cfg.Port = "9999"
	if got := cfg.effectivePort(); got != "9999" {
		t.Errorf("effectivePort() with explicit port = %q, want %q", got, "9999")
	}
}

// TestCacheReadWrite verifies that data written to the relay cache for a given
// environment can be read back and matches the original bytes exactly.
func TestCacheReadWrite(t *testing.T) {
	t.Parallel()

	logger := newTestLogger(t)
	rp := NewRelayProxy(RelayConfig{
		GatewayURL:  "http://gateway:8080",
		Environment: "production",
	}, logger)

	env := "staging"
	payload := []byte(`{"flags":[{"key":"dark_mode","enabled":true}]}`)

	// Nothing written yet — cache should return nil.
	if got := rp.getCache(env); got != nil {
		t.Errorf("empty cache returned non-nil for %q: %s", env, got)
	}

	rp.setCache(env, payload)

	got := rp.getCache(env)
	if got == nil {
		t.Fatalf("getCache(%q) returned nil after setCache", env)
	}
	if string(got) != string(payload) {
		t.Errorf("cache mismatch:\n  got  %s\n  want %s", got, payload)
	}
}

// TestCacheReadWriteMutation verifies that the returned cache bytes are a copy;
// mutating the returned slice does not corrupt the stored value.
func TestCacheReadWriteMutation(t *testing.T) {
	t.Parallel()

	logger := newTestLogger(t)
	rp := NewRelayProxy(RelayConfig{Environment: "production"}, logger)

	env := "canary"
	original := []byte(`{"flags":[]}`)
	rp.setCache(env, original)

	first := rp.getCache(env)
	// Corrupt the returned slice.
	for i := range first {
		first[i] = 'X'
	}

	// A second read must return the uncorrupted original.
	second := rp.getCache(env)
	if string(second) != string(original) {
		t.Errorf("cache was mutated through returned slice:\n  got  %s\n  want %s", second, original)
	}
}

// TestCacheIsolation verifies that writing a snapshot for the "dev" environment
// does not affect the "production" environment's cache, and vice-versa.
func TestCacheIsolation(t *testing.T) {
	t.Parallel()

	logger := newTestLogger(t)
	rp := NewRelayProxy(RelayConfig{Environment: "production"}, logger)

	devPayload  := []byte(`{"env":"dev","flags":[{"key":"new_ui","enabled":false}]}`)
	prodPayload := []byte(`{"env":"production","flags":[{"key":"new_ui","enabled":true}]}`)

	rp.setCache("dev", devPayload)
	rp.setCache("production", prodPayload)

	// dev read must not bleed into production.
	gotProd := rp.getCache("production")
	if string(gotProd) != string(prodPayload) {
		t.Errorf("production cache corrupted by dev write:\n  got  %s\n  want %s", gotProd, prodPayload)
	}

	// production write must not bleed into dev.
	gotDev := rp.getCache("dev")
	if string(gotDev) != string(devPayload) {
		t.Errorf("dev cache corrupted by production write:\n  got  %s\n  want %s", gotDev, devPayload)
	}

	// An unrelated environment must still return nil.
	if got := rp.getCache("staging"); got != nil {
		t.Errorf("staging cache should be nil, got %s", got)
	}
}

// TestPersistSnapshotSkipsWhenNoDirConfigured verifies that PersistSnapshot is
// a no-op (returns nil) when SnapshotDir is empty.
func TestPersistSnapshotSkipsWhenNoDirConfigured(t *testing.T) {
	t.Parallel()

	logger := newTestLogger(t)
	rp := NewRelayProxy(RelayConfig{
		Environment: "production",
		// SnapshotDir intentionally empty
	}, logger)

	if err := rp.PersistSnapshot("production", []byte(`{}`)); err != nil {
		t.Errorf("PersistSnapshot without SnapshotDir should be a no-op, got error: %v", err)
	}
}

// TestPersistAndLoadSnapshot verifies the round-trip: persist a snapshot to a
// temp directory, then load it back from disk.
func TestPersistAndLoadSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := newTestLogger(t)
	rp := NewRelayProxy(RelayConfig{
		Environment: "production",
		SnapshotDir: dir,
	}, logger)

	payload := []byte(`{"flags":[{"key":"checkout_v2","enabled":true}]}`)

	if err := rp.PersistSnapshot("production", payload); err != nil {
		t.Fatalf("PersistSnapshot: %v", err)
	}

	loaded, err := rp.loadSnapshotFromDisk("production")
	if err != nil {
		t.Fatalf("loadSnapshotFromDisk: %v", err)
	}
	if string(loaded) != string(payload) {
		t.Errorf("disk round-trip mismatch:\n  got  %s\n  want %s", loaded, payload)
	}
}

// TestConnectedStateTransitions verifies that the connected flag transitions
// correctly through setConnected / read via the health method path.
func TestConnectedStateTransitions(t *testing.T) {
	t.Parallel()

	logger := newTestLogger(t)
	rp := NewRelayProxy(RelayConfig{Environment: "production"}, logger)

	// Initially false.
	rp.connMu.RLock()
	initial := rp.connected
	rp.connMu.RUnlock()
	if initial {
		t.Error("relay should start disconnected")
	}

	rp.setConnected(true)
	rp.connMu.RLock()
	after := rp.connected
	rp.connMu.RUnlock()
	if !after {
		t.Error("relay should be connected after setConnected(true)")
	}

	rp.setConnected(false)
	rp.connMu.RLock()
	final := rp.connected
	rp.connMu.RUnlock()
	if final {
		t.Error("relay should be disconnected after setConnected(false)")
	}
}
