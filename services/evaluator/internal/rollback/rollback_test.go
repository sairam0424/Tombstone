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
