package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	v1alpha1 "github.com/tombstone-io/tombstone/services/tombstone-operator/api/v1alpha1"
)

// TestGetFlag_RetriesTransientFailure verifies that a flag-api handler which
// fails a couple of times before returning 404 still resolves correctly —
// proving the reconciler's resilient client retried transient 500s rather
// than surfacing the first failure immediately.
func TestGetFlag_RetriesTransientFailure(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := newFeatureFlagReconcilerWithClient(nil, nil, zap.NewNop(), srv.URL, "test-token", nil)

	result, err := r.getFlag(context.Background(), "my-flag")
	if err != nil {
		t.Fatalf("getFlag() returned unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for 404, got %+v", result)
	}
	if got := atomic.LoadInt32(&attempts); got < 3 {
		t.Fatalf("expected at least 3 attempts (retried through transient 500s), got %d", got)
	}
}

// TestCreateFlag_CircuitOpensAfterConsecutiveFailures verifies that once
// flag-api fails enough times consecutively across calls, the reconciler's
// circuit breaker opens and a subsequent call fails fast without adding any
// new attempt against the (down) upstream.
//
// httpclient.DefaultConfig() sets MaxRetries=3 (4 attempts/call) and
// FailureThreshold=5. The retry policy wraps the circuit breaker (failsafe.With
// composes policies in the order given, outermost first), so every retry
// attempt is individually gated by the breaker:
//   - call 1: 4 attempts, all hit the server (failure count 1..4)
//   - call 2: attempt 1 hits the server (failure count reaches 5, breaker
//     opens); attempts 2-4 fail fast against the now-open breaker
//   - call 3: fails fast on attempt 1, zero new server hits
func TestCreateFlag_CircuitOpensAfterConsecutiveFailures(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := newFeatureFlagReconcilerWithClient(nil, nil, zap.NewNop(), srv.URL, "test-token", nil)

	flag := &v1alpha1.FeatureFlag{
		Spec: v1alpha1.FeatureFlagSpec{Key: "my-flag", Type: "BOOLEAN"},
	}

	// Warm-up calls: accumulate enough consecutive failures to open the breaker.
	if _, err := r.createFlag(context.Background(), flag); err == nil {
		t.Fatal("expected call 1 to fail against an always-500 upstream")
	}
	if _, err := r.createFlag(context.Background(), flag); err == nil {
		t.Fatal("expected call 2 to fail against an always-500 upstream")
	}

	afterWarmup := atomic.LoadInt32(&attempts)
	if afterWarmup < 5 {
		t.Fatalf("expected at least 5 real server hits to trip the breaker, got %d", afterWarmup)
	}

	// This call should fail fast: the circuit is open, so no new attempt
	// should reach the server at all.
	if _, err := r.createFlag(context.Background(), flag); err == nil {
		t.Fatal("expected call 3 to fail (circuit open)")
	}
	afterOpen := atomic.LoadInt32(&attempts)
	if afterOpen != afterWarmup {
		t.Fatalf("expected no new server hit while circuit open: before=%d after=%d", afterWarmup, afterOpen)
	}
}
