package v1

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// newNoopRedis returns a go-redis client pointed at an unreachable address.
// GetSnapshot treats a Redis cache-read error as a cache miss and falls
// through to the flag-api fetch path, so this lets us test the resilient
// HTTP retry behavior without a real Redis server.
func newNoopRedis() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: "localhost:0"})
}

// TestGetSnapshot_RetriesTransientFlagAPIFailure verifies that a flag-api
// handler which fails a couple of times before succeeding still results in
// a 200 response — proving the resilient client retried rather than
// surfacing the first failure immediately.
func TestGetSnapshot_RetriesTransientFlagAPIFailure(t *testing.T) {
	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"flags":[]}`))
	}))
	defer upstream.Close()

	proxy := NewSnapshotProxy(newNoopRedis(), upstream.URL, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot?environment=production", nil)
	w := httptest.NewRecorder()

	proxy.GetSnapshot(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GetSnapshot() status = %d; want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}
	if got := atomic.LoadInt32(&attempts); got < 3 {
		t.Fatalf("expected at least 3 attempts (retried through transient 500s), got %d", got)
	}
}

// TestGetSnapshot_UpstreamAlwaysFails verifies that when flag-api never
// succeeds, GetSnapshot returns 502 rather than hanging indefinitely.
func TestGetSnapshot_UpstreamAlwaysFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	proxy := NewSnapshotProxy(newNoopRedis(), upstream.URL, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot?environment=production", nil)
	w := httptest.NewRecorder()

	proxy.GetSnapshot(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("GetSnapshot() status = %d; want %d", w.Code, http.StatusBadGateway)
	}
}
