package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/tombstone/flag-api/internal/secrets"
)

const (
	// sdkRatePerMin is the sustained request rate per SDK credential (requests/minute).
	sdkRatePerMin = 1000
	// sdkBurst is the burst capacity per SDK credential.
	sdkBurst = 50
	// ipRatePerMin is the sustained request rate per IP fallback (requests/minute).
	ipRatePerMin = 200
	// ipBurst is the burst capacity per IP.
	ipBurst = 20

	// keyPrefixSDK / keyPrefixIP namespace the Redis bucket hash keys so the
	// two credential classes never collide.
	keyPrefixSDK = "ratelimit:sdk:"
	keyPrefixIP  = "ratelimit:ip:"

	// bucketKeyScope separates the digests this limiter derives from the
	// pepper from every other use of it (see secrets.TokenHasher.HashScoped).
	// Changing it resets every SDK bucket, which is harmless: buckets live for
	// seconds.
	bucketKeyScope = "flag-api/ratelimit/sdk-bucket/v1"
)

// bucketScriptSrc is a single atomic Lua script implementing a leaky-bucket
// rate limiter directly in Redis. This is the storage layer for distributed
// rate limiting: bucket state (units remaining, last refill time) lives in
// Redis instead of in-process memory, so every replica of this service
// enforces the SAME limit instead of each replica tracking its own
// independent bucket (which would multiply the effective limit by the
// replica count).
//
// Per Redis's own documented guidance, the check-and-update step is a single
// EVAL/EVALSHA call rather than WATCH/MULTI/EXEC — optimistic WATCH-based
// transactions abort and retry precisely under the high-concurrency
// conditions rate limiting exists to handle, whereas a Lua script runs to
// completion atomically with no retries and no lost updates.
//
// KEYS[1] = bucket identifier
// ARGV[1] = capacity (burst size)
// ARGV[2] = refill rate, units per second
// ARGV[3] = requested units (always 1 for this middleware)
// ARGV[4] = current unix time, seconds (float), supplied by the caller
//
// Returns {allowed ("1"/"0"), remaining, retry_after_seconds} — all as
// strings. Values returned directly to Redis's RESP protocol are truncated to
// integers, so fractional values are stringified in the script to avoid
// losing precision.
const bucketScriptSrc = `
local bucket_id = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local requested = tonumber(ARGV[3])
local now = tonumber(ARGV[4])

local bucket = redis.call("HMGET", bucket_id, "remaining", "last_refill")
local remaining = tonumber(bucket[1])
local last_refill = tonumber(bucket[2])

if remaining == nil then
  remaining = capacity
  last_refill = now
end

local elapsed = now - last_refill
if elapsed < 0 then
  elapsed = 0
end
remaining = math.min(capacity, remaining + elapsed * refill_rate)

local allowed = 0
local retry_after = 0
if remaining >= requested then
  remaining = remaining - requested
  allowed = 1
else
  retry_after = (requested - remaining) / refill_rate
end

redis.call("HMSET", bucket_id, "remaining", tostring(remaining), "last_refill", tostring(now))
local ttl = math.ceil(capacity / refill_rate) + 1
redis.call("EXPIRE", bucket_id, ttl)

return {tostring(allowed), tostring(remaining), tostring(retry_after)}
`

var bucketScript = redis.NewScript(bucketScriptSrc)

// RateLimitMiddleware enforces per-credential and per-IP rate limits using
// Redis-backed leaky buckets. Buckets keyed on the Bearer credential take
// priority; IP is the fallback. State is shared across every replica of this
// service via Redis, rather than held in per-process memory.
type RateLimitMiddleware struct {
	rdb *redis.Client
	// credentialDigest maps a bearer credential to the identifier used in its
	// bucket key. It never returns the credential itself.
	credentialDigest func(cred string) string
}

// RateLimitOption configures a RateLimitMiddleware.
type RateLimitOption func(*RateLimitMiddleware)

// WithCredentialHasher keys SDK buckets with a pepper-keyed HMAC of the
// credential instead of the default unkeyed SHA-256. Production wiring should
// always pass it: operator-chosen tokens (the documented dev token,
// SCIM_TOKEN) are guessable, and without the pepper a reader of Redis cannot
// confirm a guess against a keyed digest. A nil hasher keeps the default.
func WithCredentialHasher(h *secrets.TokenHasher) RateLimitOption {
	return func(m *RateLimitMiddleware) {
		if h == nil {
			return
		}
		m.credentialDigest = func(cred string) string {
			return h.HashScoped(bucketKeyScope, cred)
		}
	}
}

// NewRateLimitMiddleware constructs the middleware backed by rdb.
func NewRateLimitMiddleware(rdb *redis.Client, opts ...RateLimitOption) *RateLimitMiddleware {
	m := &RateLimitMiddleware{rdb: rdb, credentialDigest: unkeyedDigest}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Stop is a no-op kept for interface compatibility with callers that defer
// Stop() during shutdown (e.g. cmd/main.go). There is no background cleaner
// goroutine to stop now that bucket state lives in Redis and expires via TTL
// instead of an in-process stale-entry sweep.
func (m *RateLimitMiddleware) Stop() {}

// RateLimit returns an http.Handler middleware.
// Exempt path: /api/v1/health (exact match).
// On limit: HTTP 429 + Retry-After header.
// Fail-open: any panic, or any Redis error (e.g. Redis unavailable), is
// logged and the request is passed through rather than blocked.
func (m *RateLimitMiddleware) RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fail-open: recover from any unexpected panic so a bug here
		// never takes down the entire service.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("ratelimit: recovered panic: %v", rec)
				next.ServeHTTP(w, r)
			}
		}()

		// Exempt the health and readiness endpoints.
		if r.URL.Path == "/api/v1/health" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}

		// Determine the rate limit bucket.
		// Prefer the Bearer credential; fall back to the client IP.
		var (
			bucketID   string
			ratePerMin float64
			burst      float64
			keyType    string
		)
		if cred := extractBearerToken(r); cred != "" {
			bucketID = m.sdkBucketKey(cred)
			ratePerMin = sdkRatePerMin
			burst = sdkBurst
			keyType = "token"
		} else {
			ip := extractIP(r)
			bucketID = keyPrefixIP + ip
			ratePerMin = ipRatePerMin
			burst = ipBurst
			keyType = "ip"
		}

		allowed, retryAfter, err := m.checkLimit(r.Context(), bucketID, burst, ratePerMin)
		if err != nil {
			// Fail-open: if Redis is unreachable, let the request through
			// rather than blocking legitimate traffic on an infra outage.
			log.Printf("ratelimit: redis error, failing open: %v", err)
			next.ServeHTTP(w, r)
			return
		}

		if !allowed {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprintf(w, `{"error":"rate limit exceeded","retry_after":%d,"key_type":%q}`,
				retryAfter, keyType)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// checkLimit runs the atomic leaky-bucket Lua script against Redis and
// reports whether the request is allowed plus the Retry-After value (whole
// seconds, minimum 1) to report when it is not.
func (m *RateLimitMiddleware) checkLimit(ctx context.Context, bucketID string, capacity, ratePerMin float64) (bool, int, error) {
	refillRate := ratePerMin / 60.0 // requests/min -> units/sec
	now := float64(time.Now().UnixNano()) / 1e9

	res, err := bucketScript.Run(ctx, m.rdb, []string{bucketID}, capacity, refillRate, 1, now).Result()
	if err != nil {
		return false, 0, err
	}

	fields, ok := res.([]interface{})
	if !ok || len(fields) != 3 {
		return false, 0, fmt.Errorf("ratelimit: unexpected script result shape: %#v", res)
	}

	allowedStr, _ := fields[0].(string)
	retryAfterStr, _ := fields[2].(string)

	allowed := allowedStr == "1"

	retryAfterSecs, _ := strconv.ParseFloat(retryAfterStr, 64)
	retryAfter := int(math.Ceil(retryAfterSecs))
	if retryAfter < 1 {
		retryAfter = 1
	}

	return allowed, retryAfter, nil
}

// extractBearerToken returns the credential from "Authorization: Bearer <token>",
// or "" if absent or malformed.
func extractBearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return ""
	}
	return parts[1]
}

// sdkBucketKey returns the Redis key of the token bucket for a bearer
// credential: the SDK namespace prefix plus a fixed-size digest of the
// credential.
//
// The raw credential must never be a Redis key name. This limiter runs before
// authentication, so the value is attacker-supplied as often as it is real,
// and key names are readable through KEYS/SCAN/MONITOR/SLOWLOG, keyspace
// events, RDB/AOF files and replicas. The digest also bounds the key to a
// fixed size however large a header the caller sends.
//
// How much the digest protects depends on how it is computed. Nothing in
// flag-api enforces token entropy, and the documented dev token and
// SCIM_TOKEN are human-readable, so a pepper-keyed digest
// (WithCredentialHasher) is what stops a Redis reader confirming guesses. It
// is deliberately derived under its own scope, not secrets.TokenHasher.Hash:
// that value is service_tokens.token_hash, and copying it into Redis would
// spread the stored hash to a second, less-protected datastore.
func (m *RateLimitMiddleware) sdkBucketKey(cred string) string {
	return keyPrefixSDK + m.credentialDigest(cred)
}

// unkeyedDigest is the hex SHA-256 of the credential, used when no hasher is
// configured. It keeps the raw credential out of Redis and bounds the key
// size, but it only resists guessing for high-entropy credentials: a weak
// token can be recovered from its digest by anyone who can read the key.
func unkeyedDigest(cred string) string {
	digest := sha256.Sum256([]byte(cred))
	return hex.EncodeToString(digest[:])
}

// extractIP returns the most-specific client IP.
// Prefers X-Real-IP (set by RealIP middleware), then X-Forwarded-For first entry,
// then RemoteAddr.
func extractIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// X-Forwarded-For: client, proxy1, proxy2 — take the leftmost
		if idx := strings.Index(fwd, ","); idx != -1 {
			return strings.TrimSpace(fwd[:idx])
		}
		return strings.TrimSpace(fwd)
	}
	// Strip port from RemoteAddr ("1.2.3.4:5678" → "1.2.3.4")
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}
