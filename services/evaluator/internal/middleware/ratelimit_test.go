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

// TestRateLimit_DefaultRoute_AllowsUpToBurstThen429 hammers the same IP on a
// non-telemetry route past its burst capacity (defaultBurst=20) and confirms
// the 21st request is rejected with 429 + Retry-After.
func TestRateLimit_DefaultRoute_AllowsUpToBurstThen429(t *testing.T) {
	m, cleanup := newTestMiddleware(t)
	defer cleanup()

	handler := m.RateLimit(okHandler())

	for i := 0; i < defaultBurst; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/circuit/some-flag", nil)
		req.RemoteAddr = "203.0.113.5:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 within burst, got %d", i, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/circuit/some-flag", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exceeding default burst, got %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Errorf("expected Retry-After header to be set on 429 response")
	}
}

// TestRateLimit_TelemetryRoute_HigherLimitThanDefault confirms the telemetry
// ingest endpoint gets its own higher-capacity bucket (burst 200) rather than
// the default route bucket (burst 20), by sending more than defaultBurst
// requests to /api/v1/telemetry and expecting them all to succeed.
func TestRateLimit_TelemetryRoute_HigherLimitThanDefault(t *testing.T) {
	m, cleanup := newTestMiddleware(t)
	defer cleanup()

	handler := m.RateLimit(okHandler())

	for i := 0; i < defaultBurst+5; i++ {
		req := httptest.NewRequest(http.MethodPost, telemetryPath, nil)
		req.RemoteAddr = "203.0.113.9:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 (telemetry burst=%d > default burst=%d), got %d",
				i, telemetryBurst, defaultBurst, rec.Code)
		}
	}
}

// TestRateLimit_HealthEndpointExempt confirms /health is never rate-limited,
// even after exhausting the default bucket on other routes.
func TestRateLimit_HealthEndpointExempt(t *testing.T) {
	m, cleanup := newTestMiddleware(t)
	defer cleanup()

	handler := m.RateLimit(okHandler())

	for i := 0; i < defaultBurst+1; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/circuit/some-flag", nil)
		req.RemoteAddr = "198.51.100.7:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "198.51.100.7:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected /health to be exempt from rate limiting, got %d", rec.Code)
	}
}

// TestRateLimit_TelemetryAndDefaultBucketsAreIndependentPerIP confirms that
// the SAME IP has separate buckets for the telemetry route vs everything
// else, so exhausting one does not affect the other.
func TestRateLimit_TelemetryAndDefaultBucketsAreIndependentPerIP(t *testing.T) {
	m, cleanup := newTestMiddleware(t)
	defer cleanup()

	handler := m.RateLimit(okHandler())

	// Exhaust the default bucket for this IP.
	for i := 0; i < defaultBurst; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/circuit/some-flag", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/circuit/some-flag", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected default bucket for IP to be exhausted, got %d", rec.Code)
	}

	// Telemetry route for the SAME IP should still work — separate bucket.
	req2 := httptest.NewRequest(http.MethodPost, telemetryPath, nil)
	req2.RemoteAddr = "192.0.2.1:1234"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected telemetry bucket to be independent of the default bucket, got %d", rec2.Code)
	}
}

// TestRateLimit_FailsOpenWhenRedisUnavailable confirms that if Redis is
// unreachable, requests are allowed through rather than blocked — matching
// the fail-open philosophy of the original in-memory implementation's
// panic-recovery behavior.
func TestRateLimit_FailsOpenWhenRedisUnavailable(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	m := NewRateLimitMiddleware(rdb)
	defer func() { _ = rdb.Close() }()

	handler := m.RateLimit(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/circuit/some-flag", nil)
	req.RemoteAddr = "203.0.113.99:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected fail-open (200) when Redis is unavailable, got %d", rec.Code)
	}
}

// TestExtractIP pins the exact keying-helper behavior carried over unchanged
// from the in-memory implementation.
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
