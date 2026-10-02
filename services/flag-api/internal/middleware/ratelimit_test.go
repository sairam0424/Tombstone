package middleware

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/tombstone/flag-api/internal/secrets"
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

const (
	// keyLeakWindow is the shortest piece of a credential that must never
	// appear in a Redis key or command. Shorter pieces can collide with the key
	// prefix or a hex digest by chance, which says nothing about leakage.
	keyLeakWindow = 12

	// logLeakWindow is the same bound for log output. Log lines are short and
	// fixed, so a much shorter piece is still an unambiguous leak, and the
	// smaller window catches a line that prints only a credential prefix.
	logLeakWindow = 8

	// sdkKeyPrefix and ipKeyPrefix are deliberately literal, not the
	// production constants, so renaming either prefix fails these tests:
	// operators scan these namespaces.
	sdkKeyPrefix = "ratelimit:sdk:"
	ipKeyPrefix  = "ratelimit:ip:"
)

// startMiniredis starts an in-memory Redis and returns a client for it.
func startMiniredis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
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
	return rdb, mr
}

// newTestMiddlewareWithServer is newTestMiddleware for tests that also need
// to inspect the keys the limiter actually wrote to Redis.
func newTestMiddlewareWithServer(t *testing.T, opts ...RateLimitOption) (*RateLimitMiddleware, *miniredis.Miniredis) {
	t.Helper()
	rdb, mr := startMiniredis(t)
	return NewRateLimitMiddleware(rdb, opts...), mr
}

// newTestHasher returns a hasher on a fresh random pepper and a function that
// gives the SDK bucket key the limiter must derive for a credential. The key
// is recomputed here from the documented construction, HMAC(HMAC(pepper,
// scope), credential), rather than read back from production code.
func newTestHasher(t *testing.T) (*secrets.TokenHasher, func(cred string) string) {
	t.Helper()
	pepper := randomCredential(t)
	hasher, err := secrets.NewTokenHasher(pepper)
	if err != nil {
		t.Fatalf("failed to build hasher: %v", err)
	}
	keyFor := func(cred string) string {
		subKey := hmac.New(sha256.New, []byte(pepper))
		subKey.Write([]byte(bucketKeyScope))
		mac := hmac.New(sha256.New, subKey.Sum(nil))
		mac.Write([]byte(cred))
		return sdkKeyPrefix + hex.EncodeToString(mac.Sum(nil))
	}
	return hasher, keyFor
}

// keyWrittenFor sends one request bearing cred through a limiter built with
// opts and returns the single Redis key it wrote.
func keyWrittenFor(t *testing.T, cred string, opts ...RateLimitOption) string {
	t.Helper()
	m, mr := newTestMiddlewareWithServer(t, opts...)
	serveOnce(m.RateLimit(okHandler()), "Bearer "+cred, "203.0.113.5:1234")
	keys := mr.Keys()
	if len(keys) != 1 {
		t.Fatalf("expected exactly one bucket key, got %d", len(keys))
	}
	return keys[0]
}

// randomCredential returns a fresh 256-bit URL-safe string generated at run
// time, so no secret-looking literal is ever committed.
func randomCredential(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("failed to generate credential: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// sdkDigest is the hex SHA-256 digest of a credential, computed independently
// of the production code. It is what an attacker holding a dictionary would
// compute to test guesses against a key, and what a limiter with no hasher
// configured uses.
func sdkDigest(cred string) string {
	sum := sha256.Sum256([]byte(cred))
	return hex.EncodeToString(sum[:])
}

// digestOf returns the identifier part of an SDK bucket key.
func digestOf(key string) string {
	return strings.TrimPrefix(key, sdkKeyPrefix)
}

// serveOnce sends one GET through handler. An empty authorization leaves the
// Authorization header unset.
func serveOnce(handler http.Handler, authorization, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
	req.RemoteAddr = remoteAddr
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// captureLogs redirects the standard logger to a buffer for the rest of the
// test and returns a function reporting everything logged so far.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	return logs.String
}

// commandRecorder is a go-redis hook that records every command sent to Redis
// with its arguments, which is what MONITOR and SLOWLOG expose.
type commandRecorder struct {
	mu       sync.Mutex
	commands []string
}

func (r *commandRecorder) record(cmds ...redis.Cmder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cmd := range cmds {
		r.commands = append(r.commands, fmt.Sprintf("%v", cmd.Args()))
	}
}

func (r *commandRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.commands...)
}

func (r *commandRecorder) DialHook(next redis.DialHook) redis.DialHook { return next }

func (r *commandRecorder) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		r.record(cmd)
		return next(ctx, cmd)
	}
}

func (r *commandRecorder) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		r.record(cmds...)
		return next(ctx, cmds)
	}
}

// assertNoCredentialLeak fails if s contains secret or any window-sized piece
// of it. It never prints s, which may itself hold the secret.
func assertNoCredentialLeak(t *testing.T, where, s, secret string, window int) {
	t.Helper()
	if strings.Contains(s, secret) {
		t.Errorf("%s contains the raw value", where)
		return
	}
	for i := 0; i+window <= len(secret); i++ {
		if strings.Contains(s, secret[i:i+window]) {
			t.Errorf("%s contains a %d-character piece of the value (offset %d)", where, window, i)
			return
		}
	}
}

// TestRateLimit_SDKBucketKeyDoesNotContainCredential proves the key handed to
// Redis for a bearer-authenticated request is a fixed-size digest, and that
// neither the credential nor the digest reaches anything an operator or
// attacker can read: key names (KEYS, SCAN, RDB/AOF), command arguments
// (MONITOR, SLOWLOG) or logs. The key is also not attacker-sized.
func TestRateLimit_SDKBucketKeyDoesNotContainCredential(t *testing.T) {
	hasher, keyFor := newTestHasher(t)
	tests := []struct {
		name string
		cred string
	}{
		{"random 256-bit token", randomCredential(t)},
		{"hex-encoded token", hex.EncodeToString([]byte(randomCredential(t)))},
		{"jwt-shaped token", strings.Join([]string{randomCredential(t), randomCredential(t), randomCredential(t)}, ".")},
		{"redis glob and separator characters", "*?[]:" + randomCredential(t)},
		{"oversized token", strings.Repeat(randomCredential(t), 256)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, mr := newTestMiddlewareWithServer(t, WithCredentialHasher(hasher))
			commands := &commandRecorder{}
			m.rdb.AddHook(commands)
			logs := captureLogs(t)

			rec := serveOnce(m.RateLimit(okHandler()), "Bearer "+tc.cred, "203.0.113.5:1234")
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}

			keys := mr.Keys()
			if len(keys) != 1 {
				t.Fatalf("expected exactly one bucket key, got %d", len(keys))
			}
			key := keys[0]

			assertNoCredentialLeak(t, "Redis key", key, tc.cred, keyLeakWindow)
			if !strings.HasPrefix(key, sdkKeyPrefix) {
				t.Errorf("Redis key lost the %q namespace prefix", sdkKeyPrefix)
			}
			if want := keyFor(tc.cred); key != want {
				t.Errorf("Redis key (length %d) is not the prefix plus the pepper-keyed digest of the credential", len(key))
			}

			sawKey := false
			for _, cmd := range commands.recorded() {
				assertNoCredentialLeak(t, "Redis command arguments", cmd, tc.cred, keyLeakWindow)
				sawKey = sawKey || strings.Contains(cmd, key)
			}
			if !sawKey {
				t.Error("the command recorder never saw the bucket key, so it checked nothing")
			}

			assertNoCredentialLeak(t, "log output", logs(), tc.cred, logLeakWindow)
			assertNoCredentialLeak(t, "log output", logs(), digestOf(key), logLeakWindow)
		})
	}
}

// TestRateLimit_BucketKeyCannotBeConfirmedWithoutThePepper covers the case a
// plain digest cannot: a weak, operator-chosen token (the documented dev
// token, SCIM_TOKEN). With a hasher configured, the SHA-256 an attacker with a
// dictionary computes, and the stored service_tokens.token_hash, both differ
// from the key, and a different pepper gives a different key.
func TestRateLimit_BucketKeyCannotBeConfirmedWithoutThePepper(t *testing.T) {
	// Built at run time so no credential-looking literal is committed.
	weak := strings.Join([]string{"dev", "token", "change", "me"}, "-")
	hasher, keyFor := newTestHasher(t)
	otherHasher, _ := newTestHasher(t)

	key := keyWrittenFor(t, weak, WithCredentialHasher(hasher))

	if key != keyFor(weak) {
		t.Errorf("Redis key is not the pepper-keyed digest of the credential")
	}
	if key == sdkKeyPrefix+sdkDigest(weak) {
		t.Error("Redis key equals the unkeyed SHA-256 of the credential, so a dictionary confirms a guess from the key alone")
	}
	if key == sdkKeyPrefix+hasher.Hash(weak) {
		t.Error("Redis key equals service_tokens.token_hash, copying the stored hash into Redis")
	}
	if other := keyWrittenFor(t, weak, WithCredentialHasher(otherHasher)); other == key {
		t.Error("a different pepper produced the same Redis key, so the key does not depend on the pepper")
	}
}

// TestRateLimit_WithoutHasherKeyIsUnkeyedDigest pins the fallback: with no
// hasher (or a nil one) the raw credential still never becomes a key, it is
// replaced by its plain SHA-256 digest.
func TestRateLimit_WithoutHasherKeyIsUnkeyedDigest(t *testing.T) {
	tests := []struct {
		name string
		opts []RateLimitOption
	}{
		{"no option", nil},
		{"nil hasher", []RateLimitOption{WithCredentialHasher(nil)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cred := randomCredential(t)

			key := keyWrittenFor(t, cred, tc.opts...)

			assertNoCredentialLeak(t, "Redis key", key, cred, keyLeakWindow)
			if want := sdkKeyPrefix + sdkDigest(cred); key != want {
				t.Errorf("Redis key (length %d) is not the prefix plus the hex SHA-256 digest of the credential", len(key))
			}
		})
	}
}

// TestRateLimit_SDKBucketKeyIsStablePerCredential confirms hashing keeps the
// bucket identity exactly as fine-grained as the raw credential was: the same
// credential maps to one key, any difference maps to another.
func TestRateLimit_SDKBucketKeyIsStablePerCredential(t *testing.T) {
	hasher, keyFor := newTestHasher(t)
	a, b := randomCredential(t), randomCredential(t)
	tests := []struct {
		name        string
		first       string
		second      string
		wantBuckets int
	}{
		{"same credential twice", "Bearer " + a, "Bearer " + a, 1},
		{"scheme case does not change the bucket", "Bearer " + a, "bearer " + a, 1},
		{"different credentials", "Bearer " + a, "Bearer " + b, 2},
		{"one extra trailing character", "Bearer " + a, "Bearer " + a + "x", 2},
		{"one extra leading space", "Bearer " + a, "Bearer  " + a, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, mr := newTestMiddlewareWithServer(t, WithCredentialHasher(hasher))
			handler := m.RateLimit(okHandler())

			serveOnce(handler, tc.first, "203.0.113.5:1234")
			serveOnce(handler, tc.second, "203.0.113.5:1234")

			keys := mr.Keys()
			if len(keys) != tc.wantBuckets {
				t.Fatalf("expected %d bucket key(s), got %d", tc.wantBuckets, len(keys))
			}
			if tc.wantBuckets == 1 && keys[0] != keyFor(a) {
				t.Errorf("shared bucket key is not the prefix plus the pepper-keyed digest of the credential")
			}
		})
	}
}

// exhaustBucket pre-seeds key as an empty bucket whose refill clock is in the
// far future, so the next request against it is throttled however long the
// test takes to run. Sending burst+1 requests instead would race the wall-clock
// refill (one token per ~60ms at the SDK rate), which -race and loaded CI lose.
// The field names are the ones bucketScriptSrc reads and writes.
func exhaustBucket(mr *miniredis.Miniredis, key string) {
	mr.HSet(key, "remaining", "0", "last_refill", "99999999999")
}

// TestRateLimit_TokenBucketsAreIsolatedPerCredential confirms the limit is
// still enforced per credential after hashing: a credential whose bucket is
// exhausted is throttled while a different credential, from the same address,
// is not. Neither the throttled request's credential nor its digest may be
// logged on the way.
func TestRateLimit_TokenBucketsAreIsolatedPerCredential(t *testing.T) {
	const remoteAddr = "203.0.113.5:1234"
	hasher, keyFor := newTestHasher(t)
	m, mr := newTestMiddlewareWithServer(t, WithCredentialHasher(hasher))
	logs := captureLogs(t)
	handler := m.RateLimit(okHandler())
	a, b := randomCredential(t), randomCredential(t)
	exhaustBucket(mr, keyFor(a))

	if rec := serveOnce(handler, "Bearer "+a, remoteAddr); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for the credential with the exhausted bucket, got %d", rec.Code)
	}
	if rec := serveOnce(handler, "Bearer "+b, remoteAddr); rec.Code != http.StatusOK {
		t.Fatalf("expected the other credential to have its own bucket, got %d", rec.Code)
	}

	for _, cred := range []string{a, b} {
		assertNoCredentialLeak(t, "log output", logs(), cred, logLeakWindow)
		assertNoCredentialLeak(t, "log output", logs(), digestOf(keyFor(cred)), logLeakWindow)
	}
}

// TestRateLimit_IPBucketKeyUnchanged pins the IP fallback key: hashing the
// credential must not touch how unauthenticated requests are keyed.
func TestRateLimit_IPBucketKeyUnchanged(t *testing.T) {
	hasher, _ := newTestHasher(t)
	tests := []struct {
		name       string
		remoteAddr string
		wantKey    string
	}{
		{"documentation-range address", "203.0.113.5:1234", ipKeyPrefix + "203.0.113.5"},
		{"another address and port", "198.51.100.77:65535", ipKeyPrefix + "198.51.100.77"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, mr := newTestMiddlewareWithServer(t, WithCredentialHasher(hasher))

			serveOnce(m.RateLimit(okHandler()), "", tc.remoteAddr)

			keys := mr.Keys()
			if len(keys) != 1 || keys[0] != tc.wantKey {
				t.Errorf("bucket keys = %q, want exactly [%q]", keys, tc.wantKey)
			}
		})
	}
}

// TestRateLimit_AuthorizationHeaderBucketSelection pins which bucket class
// (token or IP) and which exact key each Authorization shape selects, so
// hashing cannot change how malformed or unusual headers are treated. The
// expected bucket is pre-exhausted: the request is throttled, with the right
// key_type, only if it landed in exactly that bucket.
func TestRateLimit_AuthorizationHeaderBucketSelection(t *testing.T) {
	const remoteAddr = "203.0.113.5:1234"
	hasher, keyFor := newTestHasher(t)
	ipKey := ipKeyPrefix + "203.0.113.5"
	tok := randomCredential(t)

	tests := []struct {
		name          string
		authorization string
		wantKey       string
		wantKeyType   string
	}{
		{"no header", "", ipKey, "ip"},
		{"empty bearer token", "Bearer ", ipKey, "ip"},
		{"scheme without a space", "Bearer", ipKey, "ip"},
		{"non-bearer scheme", "Basic " + tok, ipKey, "ip"},
		{"unknown scheme", "Token " + tok, ipKey, "ip"},
		{"canonical bearer", "Bearer " + tok, keyFor(tok), "token"},
		{"lowercase scheme", "bearer " + tok, keyFor(tok), "token"},
		{"uppercase scheme", "BEARER " + tok, keyFor(tok), "token"},
		{"extra leading space stays in the credential", "Bearer  " + tok, keyFor(" " + tok), "token"},
		{"trailing space stays in the credential", "Bearer " + tok + " ", keyFor(tok + " "), "token"},
		{"inner space stays in the credential", "Bearer " + tok + " extra", keyFor(tok + " extra"), "token"},
		{"whitespace-only credential", "Bearer   ", keyFor("  "), "token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, mr := newTestMiddlewareWithServer(t, WithCredentialHasher(hasher))
			exhaustBucket(mr, tc.wantKey)

			rec := serveOnce(m.RateLimit(okHandler()), tc.authorization, remoteAddr)

			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("expected the request to land in the exhausted bucket %q, got status %d", tc.wantKey, rec.Code)
			}
			if want := `"key_type":"` + tc.wantKeyType + `"`; !strings.Contains(rec.Body.String(), want) {
				t.Errorf("429 body %q does not report %s", rec.Body.String(), want)
			}
			if keys := mr.Keys(); len(keys) != 1 {
				t.Errorf("expected no bucket besides %q, got keys %q", tc.wantKey, keys)
			}
		})
	}
}

// TestRateLimit_FailsOpenWithoutLoggingCredential covers both bucket classes
// under three kinds of failure (Redis unreachable, Redis returning errors, and
// a panic inside the limiter): the request must still reach the handler
// exactly once, and the log line must carry neither the credential nor its
// digest.
func TestRateLimit_FailsOpenWithoutLoggingCredential(t *testing.T) {
	hasher, keyFor := newTestHasher(t)
	tok := randomCredential(t)
	outages := []struct {
		name      string
		wantLog   string
		newClient func(t *testing.T) *redis.Client
	}{
		{"redis unreachable", "failing open", func(t *testing.T) *redis.Client {
			// MaxRetries -1 turns off go-redis retries, which would otherwise
			// make each request wait out several backoff rounds.
			rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
			t.Cleanup(func() { _ = rdb.Close() })
			return rdb
		}},
		{"redis returns errors", "failing open", func(t *testing.T) *redis.Client {
			rdb, mr := startMiniredis(t)
			mr.SetError("ERR injected failure")
			return rdb
		}},
		{"nil client panics inside the limiter", "recovered panic", func(t *testing.T) *redis.Client {
			return nil
		}},
	}
	paths := []struct {
		name          string
		authorization string
	}{
		{"ip path", ""},
		{"token path", "Bearer " + tok},
	}

	for _, outage := range outages {
		for _, path := range paths {
			t.Run(outage.name+"/"+path.name, func(t *testing.T) {
				logs := captureLogs(t)
				calls := 0
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.WriteHeader(http.StatusOK)
				})
				m := NewRateLimitMiddleware(outage.newClient(t), WithCredentialHasher(hasher))

				rec := serveOnce(m.RateLimit(next), path.authorization, "203.0.113.99:1234")

				if rec.Code != http.StatusOK || calls != 1 {
					t.Fatalf("expected fail-open (200, handler called once), got %d with %d call(s)", rec.Code, calls)
				}
				out := logs()
				if !strings.Contains(out, outage.wantLog) {
					t.Errorf("expected a %q log line", outage.wantLog)
				}
				assertNoCredentialLeak(t, "log output", out, tok, logLeakWindow)
				assertNoCredentialLeak(t, "log output", out, digestOf(keyFor(tok)), logLeakWindow)
			})
		}
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
