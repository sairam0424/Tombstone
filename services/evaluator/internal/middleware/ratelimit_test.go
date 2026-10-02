package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/tombstone/evaluator/internal/clientip"
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

// TestExtractIP pins that the IP bucket is keyed on the connection peer and
// never on a header the caller controls. Every evaluator bucket is keyed on
// this value alone, so a key taken from a header would let any caller rotate
// it to get a fresh bucket on every request and evade the limit entirely.
// internal/clientip is the only place that decides when a header is believed.
func TestExtractIP(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		want       string
	}{
		{"peer host, port stripped", nil, "10.0.0.1:5432", "10.0.0.1"},
		{"bare IPv4 peer", nil, "10.0.0.1", "10.0.0.1"},
		{"bracketed IPv6 peer, port stripped", nil, "[2001:db8::1]:5432", "2001:db8::1"},
		{"bare IPv6 peer", nil, "2001:db8::1", "2001:db8::1"},
		{"ignores X-Real-IP", map[string]string{"X-Real-IP": "1.2.3.4"}, "9.9.9.9:80", "9.9.9.9"},
		{"ignores X-Forwarded-For", map[string]string{"X-Forwarded-For": "5.6.7.8, 9.9.9.9"}, "1.1.1.1:80", "1.1.1.1"},
		{"ignores True-Client-IP", map[string]string{"True-Client-IP": "6.6.6.6"}, "9.9.9.9:80", "9.9.9.9"},
		{"ignores all forwarded headers together", map[string]string{
			"X-Real-IP": "1.2.3.4", "X-Forwarded-For": "5.6.7.8", "True-Client-IP": "6.6.6.6",
		}, "9.9.9.9:80", "9.9.9.9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			for name, value := range tc.headers {
				req.Header.Set(name, value)
			}
			got := extractIP(req)
			if got != tc.want {
				t.Errorf("extractIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

// newTestMiddlewareWithServer is newTestMiddleware for tests that also need
// to inspect the keys the limiter actually wrote to Redis.
func newTestMiddlewareWithServer(t *testing.T) (*RateLimitMiddleware, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	return NewRateLimitMiddleware(rdb), mr
}

// exhaustBucket empties a bucket and stamps its last refill in the far future,
// so the next request against it is throttled however long the test takes.
// The field names are the ones bucketScriptSrc reads and writes.
func exhaustBucket(mr *miniredis.Miniredis, key string) {
	mr.HSet(key, "remaining", "0", "last_refill", "99999999999")
}

// TestRateLimit_BucketIgnoresForgedHeaders is the end-to-end form of the test
// above, for both route classes. The peer's own bucket is pre-exhausted, so a
// request from that peer is throttled only if it landed in it.
func TestRateLimit_BucketIgnoresForgedHeaders(t *testing.T) {
	const remoteAddr = "203.0.113.5:1234"
	routes := []struct {
		name    string
		method  string
		path    string
		peerKey string
	}{
		{"default route", http.MethodGet, "/api/v1/circuit/some-flag", "ratelimit:default:203.0.113.5"},
		{"telemetry route", http.MethodPost, telemetryPath, "ratelimit:telemetry:203.0.113.5"},
	}
	forgeries := []struct{ name, header, value string }{
		{"X-Real-IP naming another client", "X-Real-IP", "198.51.100.1"},
		{"X-Forwarded-For naming another client", "X-Forwarded-For", "198.51.100.1, 192.0.2.9"},
		{"True-Client-IP naming another client", "True-Client-IP", "198.51.100.1"},
		{"X-Real-IP that is not an IP at all", "X-Real-IP", "not-an-ip'; DROP TABLE audit_log; --"},
	}
	for _, route := range routes {
		for _, forgery := range forgeries {
			t.Run(route.name+"/"+forgery.name, func(t *testing.T) {
				m, mr := newTestMiddlewareWithServer(t)
				exhaustBucket(mr, route.peerKey)
				req := httptest.NewRequest(route.method, route.path, nil)
				req.RemoteAddr = remoteAddr
				req.Header.Set(forgery.header, forgery.value)
				rec := httptest.NewRecorder()

				m.RateLimit(okHandler()).ServeHTTP(rec, req)

				if rec.Code != http.StatusTooManyRequests {
					t.Fatalf("expected the request to land in the peer's exhausted bucket %q, got status %d", route.peerKey, rec.Code)
				}
				if keys := mr.Keys(); len(keys) != 1 || keys[0] != route.peerKey {
					t.Errorf("bucket keys = %q, want exactly [%q]", keys, route.peerKey)
				}
			})
		}
	}
}

// TestRateLimit_BucketKeyIsAValidIP pins that the key suffix is always a bare
// IP: never a bracketed or truncated IPv6 peer, and never a port.
func TestRateLimit_BucketKeyIsAValidIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		wantIP     string
	}{
		{"IPv4 with port", "203.0.113.5:1234", "203.0.113.5"},
		{"bare IPv4", "203.0.113.5", "203.0.113.5"},
		{"bracketed IPv6 with port", "[2001:db8::1]:1234", "2001:db8::1"},
		{"bare IPv6", "2001:db8::1", "2001:db8::1"},
		{"v4-mapped IPv6 folds to IPv4", "[::ffff:203.0.113.5]:1234", "203.0.113.5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, mr := newTestMiddlewareWithServer(t)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/circuit/some-flag", nil)
			req.RemoteAddr = tc.remoteAddr

			m.RateLimit(okHandler()).ServeHTTP(httptest.NewRecorder(), req)

			want := "ratelimit:default:" + tc.wantIP
			keys := mr.Keys()
			if len(keys) != 1 || keys[0] != want {
				t.Fatalf("bucket keys = %q, want exactly [%q]", keys, want)
			}
			if _, err := netip.ParseAddr(strings.TrimPrefix(keys[0], "ratelimit:default:")); err != nil {
				t.Errorf("bucket key suffix is not a valid IP: %v", err)
			}
		})
	}
}

// TestRateLimit_BucketBehindTrustedProxy pins the whole chain the deployed
// service runs: with the trusted-proxy middleware in front, every client behind
// one proxy gets its own bucket, named by the proxy's X-Forwarded-For entry,
// and a caller prepending forged entries to that header cannot leave it.
func TestRateLimit_BucketBehindTrustedProxy(t *testing.T) {
	const proxyAddr = "10.1.2.3:4444"
	trust, err := clientip.ParseTrust("10.0.0.0/8")
	if err != nil {
		t.Fatalf("ParseTrust: %v", err)
	}
	serve := func(m *RateLimitMiddleware, forwardedFor string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/circuit/some-flag", nil)
		req.RemoteAddr = proxyAddr
		req.Header.Set("X-Forwarded-For", forwardedFor)
		rec := httptest.NewRecorder()
		trust.Middleware(m.RateLimit(okHandler())).ServeHTTP(rec, req)
		return rec.Code
	}

	t.Run("a forged prefix does not leave the client's bucket", func(t *testing.T) {
		m, mr := newTestMiddlewareWithServer(t)
		exhaustBucket(mr, "ratelimit:default:198.51.100.7")

		for _, forwardedFor := range []string{
			"198.51.100.7",
			"6.6.6.6, 198.51.100.7",
			"7.7.7.7, 8.8.8.8, 198.51.100.7",
		} {
			if got := serve(m, forwardedFor); got != http.StatusTooManyRequests {
				t.Errorf("X-Forwarded-For %q: status %d, want 429 from the client's exhausted bucket", forwardedFor, got)
			}
		}
	})

	t.Run("another client behind the same proxy has its own bucket", func(t *testing.T) {
		m, mr := newTestMiddlewareWithServer(t)
		exhaustBucket(mr, "ratelimit:default:198.51.100.7")

		if got := serve(m, "198.51.100.8"); got != http.StatusOK {
			t.Errorf("status %d, want 200: a different client must not share the exhausted bucket", got)
		}
		if !slices.Contains(mr.Keys(), "ratelimit:default:198.51.100.8") {
			t.Errorf("bucket keys = %q, want one named for the client 198.51.100.8", mr.Keys())
		}
	})
}
