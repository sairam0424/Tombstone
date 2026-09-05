package rollback

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// newNoopRedis returns a go-redis client that will fail every call.
// Execute() treats Redis publish failure as a warning (not an error),
// so this lets us test rollback logic without a real Redis server.
func newNoopRedis() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: "localhost:0", // unreachable — Publish returns an error that is logged and ignored
	})
}

// TestRollbackExecute_CallsKillEndpoint verifies that Execute() calls the
// flag-api kill switch with the correct flag key and environment.
func TestRollbackExecute_CallsKillEndpoint(t *testing.T) {
	var called atomic.Int32
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/flags/my-flag/kill" {
			called.Add(1)
			capturedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "test-token", newNoopRedis(), zap.NewNop())

	req := RollbackRequest{
		FlagKey:     "my-flag",
		Environment: "production",
		Reason:      "circuit breaker tripped",
		ErrorRate:   0.12,
		TriggeredBy: "evaluator",
	}
	err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute() returned unexpected error: %v", err)
	}

	if called.Load() != 1 {
		t.Errorf("kill endpoint called %d times, want 1", called.Load())
	}

	// Verify the request body contained the expected environment and reason fields.
	var body map[string]string
	if jsonErr := json.Unmarshal(capturedBody, &body); jsonErr != nil {
		t.Fatalf("kill body is not valid JSON: %v", jsonErr)
	}
	if body["environment"] != "production" {
		t.Errorf("kill body environment = %q, want %q", body["environment"], "production")
	}
	if body["reason"] != "circuit breaker tripped" {
		t.Errorf("kill body reason = %q, want %q", body["reason"], "circuit breaker tripped")
	}
}

// TestRollbackExecute_ReturnsErrorOnHTTP4xx verifies that a 4xx from flag-api
// causes Execute() to return a non-nil error (so the caller knows rollback failed).
func TestRollbackExecute_ReturnsErrorOnHTTP4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "bad-token", newNoopRedis(), zap.NewNop())

	err := exec.Execute(context.Background(), RollbackRequest{
		FlagKey:     "blocked-flag",
		Environment: "staging",
		Reason:      "test",
		TriggeredBy: "test",
	})
	if err == nil {
		t.Error("Execute() expected an error on HTTP 403, got nil")
	}
}

// TestRollbackExecute_AuthHeaderForwarded verifies that the Bearer token is
// sent in the Authorization header to flag-api.
func TestRollbackExecute_AuthHeaderForwarded(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "my-secret-token", newNoopRedis(), zap.NewNop())

	_ = exec.Execute(context.Background(), RollbackRequest{
		FlagKey:     "auth-flag",
		Environment: "production",
		Reason:      "test",
		TriggeredBy: "test",
	})

	want := "Bearer my-secret-token"
	if gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

// TestSetRolloutPct_CallsRollbackStepEndpoint verifies SetRolloutPct calls
// EVAL-4's graduated rollback-step endpoint (not the binary kill switch)
// with the correct flag key, environment, rollout_pct, and reason.
func TestSetRolloutPct_CallsRollbackStepEndpoint(t *testing.T) {
	var called atomic.Int32
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/flags/my-flag/rollback-step" {
			called.Add(1)
			capturedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "test-token", newNoopRedis(), zap.NewNop())

	err := exec.SetRolloutPct(context.Background(), "my-flag", "production", 50, "circuit_breaker")
	if err != nil {
		t.Fatalf("SetRolloutPct() returned unexpected error: %v", err)
	}
	if called.Load() != 1 {
		t.Errorf("rollback-step endpoint called %d times, want 1", called.Load())
	}

	var body map[string]any
	if jsonErr := json.Unmarshal(capturedBody, &body); jsonErr != nil {
		t.Fatalf("rollback-step body is not valid JSON: %v", jsonErr)
	}
	if body["environment"] != "production" {
		t.Errorf("body environment = %v, want production", body["environment"])
	}
	if body["rollout_pct"] != float64(50) {
		t.Errorf("body rollout_pct = %v, want 50", body["rollout_pct"])
	}
	if body["reason"] != "circuit_breaker" {
		t.Errorf("body reason = %v, want circuit_breaker", body["reason"])
	}
}

// TestSetRolloutPct_409IsTreatedAsSuccess verifies a 409 (flag-api's atomic
// write refused a stale target because a concurrent, more-aggressive step
// already won) returns nil, not an error -- the safety property this call
// exists to establish already holds by the time it returns.
func TestSetRolloutPct_409IsTreatedAsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "test-token", newNoopRedis(), zap.NewNop())

	err := exec.SetRolloutPct(context.Background(), "my-flag", "production", 50, "circuit_breaker")
	if err != nil {
		t.Errorf("SetRolloutPct() with a 409 response returned an error: %v, want nil", err)
	}
}

// TestSetRolloutPct_ReturnsErrorOnOtherHTTP4xx verifies a non-409 4xx (e.g.
// 403 permission denied, 400 validation) still returns an error, unlike 409.
func TestSetRolloutPct_ReturnsErrorOnOtherHTTP4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "test-token", newNoopRedis(), zap.NewNop())

	err := exec.SetRolloutPct(context.Background(), "my-flag", "production", 50, "circuit_breaker")
	if err == nil {
		t.Error("SetRolloutPct() expected an error on HTTP 403, got nil")
	}
}

// TestSetRolloutPct_RejectsOutOfRangePct verifies out-of-range percentages
// are rejected locally, without making a network call at all.
func TestSetRolloutPct_RejectsOutOfRangePct(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "test-token", newNoopRedis(), zap.NewNop())

	for _, pct := range []int{-1, 101} {
		if err := exec.SetRolloutPct(context.Background(), "my-flag", "production", pct, "circuit_breaker"); err == nil {
			t.Errorf("SetRolloutPct(pct=%d) expected an error, got nil", pct)
		}
	}
	if called.Load() != 0 {
		t.Errorf("rollback-step endpoint called %d times for out-of-range input, want 0", called.Load())
	}
}

// TestSetRolloutPct_AuthHeaderForwarded mirrors
// TestRollbackExecute_AuthHeaderForwarded for the new endpoint.
func TestSetRolloutPct_AuthHeaderForwarded(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "my-secret-token", newNoopRedis(), zap.NewNop())

	_ = exec.SetRolloutPct(context.Background(), "auth-flag", "production", 50, "test")

	want := "Bearer my-secret-token"
	if gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

// TestIncreaseRolloutPct_CallsRecoveryStepEndpoint mirrors
// TestSetRolloutPct_CallsRollbackStepEndpoint for the recovery-step
// endpoint -- IncreaseRolloutPct had zero dedicated unit tests before this
// (only exercised indirectly via cmd's end-to-end integration test),
// found by adversarial review of PR #221's own fix for a different
// finding.
func TestIncreaseRolloutPct_CallsRecoveryStepEndpoint(t *testing.T) {
	var called atomic.Int32
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/flags/my-flag/recovery-step" {
			called.Add(1)
			capturedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "test-token", newNoopRedis(), zap.NewNop())

	err := exec.IncreaseRolloutPct(context.Background(), "my-flag", "production", 25, "circuit_breaker")
	if err != nil {
		t.Fatalf("IncreaseRolloutPct() returned unexpected error: %v", err)
	}
	if called.Load() != 1 {
		t.Errorf("recovery-step endpoint called %d times, want 1", called.Load())
	}

	var body map[string]any
	if jsonErr := json.Unmarshal(capturedBody, &body); jsonErr != nil {
		t.Fatalf("recovery-step body is not valid JSON: %v", jsonErr)
	}
	if body["environment"] != "production" {
		t.Errorf("body environment = %v, want production", body["environment"])
	}
	if body["rollout_pct"] != float64(25) {
		t.Errorf("body rollout_pct = %v, want 25", body["rollout_pct"])
	}
	if body["reason"] != "circuit_breaker" {
		t.Errorf("body reason = %v, want circuit_breaker", body["reason"])
	}
}

// TestIncreaseRolloutPct_409IsTreatedAsSuccess verifies a 409 (flag-api's
// atomic write refused because live exposure is ALREADY above this
// call's own target) returns nil, not an error -- the goal this call
// exists to establish ("exposure is now at least pct") already holds.
// Regression coverage for a real HIGH finding from adversarial review of
// PR #221: an earlier version treated 409 as an error here, which could
// retry the same, now-permanently-unreachable target forever.
func TestIncreaseRolloutPct_409IsTreatedAsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "test-token", newNoopRedis(), zap.NewNop())

	err := exec.IncreaseRolloutPct(context.Background(), "my-flag", "production", 25, "circuit_breaker")
	if err != nil {
		t.Errorf("IncreaseRolloutPct() with a 409 response returned an error: %v, want nil", err)
	}
}

// TestIncreaseRolloutPct_ReturnsErrorOnOtherHTTP4xx verifies a non-409 4xx
// (e.g. 403 permission denied, 400 a decrease request rejected as such)
// still returns an error, unlike 409.
func TestIncreaseRolloutPct_ReturnsErrorOnOtherHTTP4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "test-token", newNoopRedis(), zap.NewNop())

	err := exec.IncreaseRolloutPct(context.Background(), "my-flag", "production", 25, "circuit_breaker")
	if err == nil {
		t.Error("IncreaseRolloutPct() expected an error on HTTP 403, got nil")
	}
}

// TestIncreaseRolloutPct_RejectsOutOfRangePct mirrors
// TestSetRolloutPct_RejectsOutOfRangePct for the recovery-step endpoint.
func TestIncreaseRolloutPct_RejectsOutOfRangePct(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "test-token", newNoopRedis(), zap.NewNop())

	for _, pct := range []int{-1, 101} {
		if err := exec.IncreaseRolloutPct(context.Background(), "my-flag", "production", pct, "circuit_breaker"); err == nil {
			t.Errorf("IncreaseRolloutPct(pct=%d) expected an error, got nil", pct)
		}
	}
	if called.Load() != 0 {
		t.Errorf("recovery-step endpoint called %d times for out-of-range input, want 0", called.Load())
	}
}

// TestIncreaseRolloutPct_AuthHeaderForwarded mirrors
// TestSetRolloutPct_AuthHeaderForwarded for the recovery-step endpoint.
func TestIncreaseRolloutPct_AuthHeaderForwarded(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exec := NewExecutor(srv.URL, "my-secret-token", newNoopRedis(), zap.NewNop())

	_ = exec.IncreaseRolloutPct(context.Background(), "auth-flag", "production", 25, "test")

	want := "Bearer my-secret-token"
	if gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}
