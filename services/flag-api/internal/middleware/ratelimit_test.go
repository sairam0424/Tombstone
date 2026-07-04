package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestMiddleware spins up an in-memory miniredis instance (which supports
// EVAL via gopher-lua) and returns a RateLimitMiddleware backed by it, plus a
// cleanup func.
func newTestMiddleware(t *testing.T) (*RateLimitMiddleware, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewRateLimitMiddleware(rdb), func() {
		_ = rdb.Close()
		mr.Close()
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestRateLimit_IPBucket_AllowsUpToBurstThen429 hammers the same IP-derived
// bucket past its burst capacity and confirms the (ip burst=20) requests
// succeed and the 21st is rejected with 429 + Retry-After.
func TestRateLimit_IPBucket_AllowsUpToBurstThen429(t *testing.T) {
	m, cleanup := newTestMiddleware(t)
	defer cleanup()

	handler := m.RateLimit(okHandler())

	var lastStatus int
	for i := 0; i < ipBurst; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
		req.RemoteAddr = "203.0.113.5:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		lastStatus = rec.Code
		if lastStatus != http.StatusOK {
			t.Fatalf("request %d: expected 200 within burst, got %d", i, lastStatus)
		}
	}

	// One more request should now exceed the burst capacity.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exceeding burst, got %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Errorf("expected Retry-After header to be set on 429 response")
	}
}

// TestRateLimit_TokenBucket_HigherLimitThanIP confirms a bearer token gets
// the higher SDK limit (burst 50) rather than the IP limit (burst 20), by
// verifying more than ipBurst requests succeed when authenticated.
func TestRateLimit_TokenBucket_HigherLimitThanIP(t *testing.T) {
	m, cleanup := newTestMiddleware(t)
	defer cleanup()

	handler := m.RateLimit(okHandler())

	for i := 0; i < ipBurst+5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
		req.Header.Set("Authorization", "Bearer sdk-test-credential")
		req.RemoteAddr = "203.0.113.9:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 (token burst=%d > ip burst=%d), got %d",
				i, sdkBurst, ipBurst, rec.Code)
		}
	}
}

// TestRateLimit_HealthEndpointExempt confirms /api/v1/health is never
// rate-limited, even after exhausting the IP bucket on other routes.
func TestRateLimit_HealthEndpointExempt(t *testing.T) {
	m, cleanup := newTestMiddleware(t)
	defer cleanup()

	handler := m.RateLimit(okHandler())

	// Exhaust the IP bucket on a normal route.
	for i := 0; i < ipBurst+1; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
		req.RemoteAddr = "198.51.100.7:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	// Health should still succeed regardless of bucket state.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "198.51.100.7:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected /api/v1/health to be exempt from rate limiting, got %d", rec.Code)
	}
}

// TestRateLimit_DistinctKeysDoNotShareBuckets confirms two different IPs (or
// tokens) get independent buckets — this is the whole point of keying by
// credential/IP rather than a single global bucket.
func TestRateLimit_DistinctKeysDoNotShareBuckets(t *testing.T) {
	m, cleanup := newTestMiddleware(t)
	defer cleanup()

	handler := m.RateLimit(okHandler())

	// Exhaust bucket for IP A.
	for i := 0; i < ipBurst; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected IP A to be rate-limited, got %d", rec.Code)
	}

	// IP B should be unaffected.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
	req2.RemoteAddr = "192.0.2.2:1234"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected IP B to have its own independent bucket, got %d", rec2.Code)
	}
}

// TestRateLimit_FailsOpenWhenRedisUnavailable confirms that if Redis is
// unreachable, requests are allowed through rather than blocked — matching
// the fail-open philosophy of the original in-memory implementation's
// panic-recovery behavior.
func TestRateLimit_FailsOpenWhenRedisUnavailable(t *testing.T) {
	// Point at a redis client with no reachable server.
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	m := NewRateLimitMiddleware(rdb)
	defer func() { _ = rdb.Close() }()

	handler := m.RateLimit(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
	req.RemoteAddr = "203.0.113.99:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected fail-open (200) when Redis is unavailable, got %d", rec.Code)
	}
}

// TestExtractBearerToken and TestExtractIP pin the exact keying-helper
// behavior carried over unchanged from the in-memory implementation.
func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"valid bearer", "Bearer abc123", "abc123"},
		{"missing header", "", ""},
		{"wrong scheme", "Basic abc123", ""},
		{"malformed no space", "Beareabc123", ""},
		{"case insensitive scheme", "bearer abc123", "abc123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			got := extractBearerToken(req)
			if got != tc.want {
				t.Errorf("extractBearerToken() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractIP(t *testing.T) {
	tests := []struct {
		name       string
		realIP     string
		forwarded  string
		remoteAddr string
		want       string
	}{
		{"prefers X-Real-IP", "1.2.3.4", "5.6.7.8", "9.9.9.9:80", "1.2.3.4"},
		{"falls back to X-Forwarded-For leftmost", "", "5.6.7.8, 9.9.9.9", "1.1.1.1:80", "5.6.7.8"},
		{"falls back to RemoteAddr, strips port", "", "", "10.0.0.1:5432", "10.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.realIP != "" {
				req.Header.Set("X-Real-IP", tc.realIP)
			}
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			got := extractIP(req)
			if got != tc.want {
				t.Errorf("extractIP() = %q, want %q", got, tc.want)
			}
		})
	}
}
