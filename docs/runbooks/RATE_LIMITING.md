# Runbook: Rate Limiting

## Quick Reference

| Symptom | Likely Cause | Action |
|---------|-------------|--------|
| SDK clients getting HTTP 429 | Exceeded SDK token tier (1000 req/min) | Check if single process is polling instead of streaming |
| Dashboard showing rate limit errors | IP tier hit (200 req/min per IP) | Ensure dashboard uses Bearer token auth, not unauthenticated |
| Rate limits not enforced across replicas | Redis unavailable | Check Redis connectivity — limits fail-open without Redis |
| Retry-After header shows very high value | Bucket fully drained | Wait for bucket refill or reduce request frequency |

---

## How Rate Limiting Works

Rate limiting is implemented in `services/flag-api/internal/middleware/ratelimit.go` using a Redis-backed leaky bucket algorithm. Every replica of flag-api shares the same bucket state via Redis — so N replicas don't multiply the effective limit.

### Tiers

```go
// SDK token tier (Bearer token present)
sdkRatePerMin = 1000   // sustained requests/minute
sdkBurst      = 50     // burst capacity (tokens available immediately)
keyPrefix     = "ratelimit:sdk:"   // followed by a pepper-keyed HMAC of the token, never the token

// IP fallback tier (no Bearer token)
ipRatePerMin  = 200    // sustained requests/minute
ipBurst       = 20     // burst capacity
keyPrefix     = "ratelimit:ip:"
```

The evaluator's telemetry route uses separate, higher limits (see `services/evaluator/internal/middleware/ratelimit.go`).

### Leaky Bucket Mechanics

The Lua script runs atomically in Redis (single EVAL call — no WATCH/MULTI/EXEC race conditions):

1. Read `remaining` tokens and `last_refill` timestamp from Redis hash
2. Refill: `remaining = min(capacity, remaining + elapsed_seconds × refill_rate)`
3. If `remaining >= 1`: deduct 1 token, allow request
4. If `remaining < 1`: deny request, return `retry_after = (1 - remaining) / refill_rate`
5. Write updated bucket state back, set TTL = `capacity / refill_rate + 1` seconds

**Refill rate** = `ratePerMin / 60` tokens per second. For SDK tier: `1000/60 ≈ 16.67 tokens/sec`.

### Fail-Open Behavior

If Redis is unavailable or returns an error, `checkLimit()` returns an error and the middleware **allows the request through** (fail-open). This means rate limiting stops working during Redis outages — this is intentional to prevent cascading failures. Monitor Redis health separately.

### Exempt Paths

```go
if r.URL.Path == "/api/v1/health" || r.URL.Path == "/readyz" {
    next.ServeHTTP(w, r)
    return
}
```

Health and readiness probes are never rate-limited.

---

## Checking Bucket State in Redis

```bash
# SDK bucket keys are "ratelimit:sdk:" plus the hex HMAC-SHA256 of the
# credential under a key derived from TOKEN_HASH_PEPPER:
#   HMAC(HMAC(pepper, "flag-api/ratelimit/sdk-bucket/v1"), credential)
# The credential itself is never stored in Redis, and without the pepper a key
# cannot be checked against a guessed token, even a weak one. To find the key
# for a token you are investigating, derive it. SDK_TOKEN and TOKEN_HASH_PEPPER
# must already be exported in your shell; they are read from the environment so
# neither appears in `ps` output.
SDK_KEY="ratelimit:sdk:$(python3 -c '
import hashlib, hmac, os
sub_key = hmac.new(os.environ["TOKEN_HASH_PEPPER"].encode(), b"flag-api/ratelimit/sdk-bucket/v1", hashlib.sha256).digest()
print(hmac.new(sub_key, os.environ["SDK_TOKEN"].encode(), hashlib.sha256).hexdigest())
')"

# Check SDK token bucket for a specific credential
redis-cli HGETALL "$SDK_KEY"
# Returns: remaining, last_refill

# Check IP bucket
redis-cli HGETALL "ratelimit:ip:192.168.1.1"

# List all SDK buckets currently tracked (digests, not tokens)
redis-cli KEYS "ratelimit:sdk:*"

# Check TTL (when bucket expires/resets)
redis-cli TTL "$SDK_KEY"
```

### Finding which client a bucket belongs to

You cannot read it off the key. The digest is one-way, and it is derived under its own scope, so it does not match `service_tokens.token_hash` and cannot be joined to a service token row. Before this change the bucket key was the raw token, so a hot bucket showed which token was throttled; now the mapping only works from a token to its key, which needs the token and the pepper.

The 429 response and the limiter's own logs name no caller, and the request log line carries method, path, status and client address, not the credential. To find which client is throttled, correlate the 429s by client address in the request logs, or derive the key (above) for a token you suspect and check whether that bucket is drained.

### Rotating `TOKEN_HASH_PEPPER`

Do not rotate the pepper to reset rate-limit buckets. The same pepper keys the stored hashes of service tokens and break-glass tokens (`service_tokens.token_hash`, `break_glass_tokens.token_hash`), so changing it invalidates every one of them and locks out every SDK client until each service and break-glass token is re-issued (see `services/flag-api/internal/db/migrations/014_hashed_tokens.sql`). The rate-limit effect alone is harmless: every SDK bucket key changes, so SDK buckets start full, and they live for seconds. All replicas must run the same pepper, as they already must for service-token lookups.

### Upgrading from raw-credential keys

Before this change the key was `ratelimit:sdk:<bearer value>`, so every credential that reached the API, real or guessed, was a key name in Redis. This has been the case since the Redis-backed limiter shipped in v1.2.0 (2026-07-05), so the exposure window runs from that release until the last old replica is replaced.

- **Live keys need no purge.** Each bucket expires on its own: 4 s for SDK buckets, 7 s for IP buckets. During a rolling deploy, old replicas keep writing raw keys until the last one is replaced, and a credential briefly has one bucket per key format.
- **Copies outlive the TTL.** Treat anything that existed before the last old replica went away as possibly holding credentials: RDB and AOF files, backups, replicas, `SLOWLOG`, `MONITOR` captures, and the command or key logs of a managed Redis provider.
- Do not try to tell raw keys from digests by shape: a token made with `openssl rand -hex 32` is 64 hex characters, exactly like a digest. Never `FLUSHALL` or `FLUSHDB` to clean up.

What you can clean up depends on who runs Redis.

**Self-managed Redis.** After the rollout, wait more than 5 s, then run `SLOWLOG RESET`, run `BGREWRITEAOF` if AOF is on, and delete snapshots and backups taken before the rollout. Rotate the service tokens and `SCIM_TOKEN` if anyone outside the token owners could read Redis before the rollout (shared console, APM, backups).

**Managed Redis (for example Upstash).** The repo's production configs point flag-api at a managed Redis (`infra/oracle/docker-compose.prod.yml`, `infra/northflank/README.md`, `infra/helm/flagmind/values-region-primary.yaml`). There you generally cannot purge the provider's logs, backups or replicas, and the provider's documentation decides which of the commands above are allowed at all. Unless the provider confirms the key history is gone, assume raw credentials from the whole exposure window are retained outside your control: after the rollout, rotate every service token and `SCIM_TOKEN` by default. Rotating the pepper does not help with this and breaks every stored token hash (see above).

### Known limitation: any Bearer value skips the IP tier

The limiter picks its bucket from the `Authorization` header before authentication runs. A request with `Authorization: Bearer <anything>` gets its own full SDK bucket keyed by that value, valid or not, and never touches the IP bucket. A caller who sends a different value on every request therefore avoids the IP ceiling on every route, including the unauthenticated ones (`/auth/login`, `/auth/callback`, `/metrics`, `/api/v1/docs`) and guess-and-check against `SCIM_TOKEN`. On `/api/v1` each such request also costs an authentication lookup. Hashing the key does not change this. Fixing it needs a failed-authentication throttle, which is not implemented.

---

## Capacity Planning

### Estimating Limits for Your Traffic

For SDK tokens authenticating server-side applications:

```
Sustained SDK connections: N services × polls per minute
Example: 5 services × 10 req/min = 50 req/min  →  well within 1000/min limit
```

For browser SDKs connecting via gateway SSE (not flag-api REST):
- SSE connections don't hit the rate limiter (they go through the gateway, port 8080)
- Only the initial snapshot fetch hits flag-api (:8081)

### When to Increase Limits

If a single SDK token legitimately needs >1000 req/min sustained:
1. Prefer switching to SSE streaming via gateway — this eliminates polling entirely
2. If polling is required, split load across multiple SDK tokens
3. Adjusting `sdkRatePerMin` requires a code change and redeploy

### Evaluator Telemetry Limits

The evaluator's telemetry ingestion route uses a higher limit:
- SDK telemetry: 5000 req/min, burst 200 (from `services/evaluator/internal/middleware/ratelimit.go`)
- This is intentionally higher because telemetry is write-once-per-evaluation, not polled

---

## Rate Limit Response Headers

On 429 responses:
```
HTTP/1.1 429 Too Many Requests
Retry-After: 4
Content-Type: application/json

{"error":"rate limit exceeded","retry_after":4,"key_type":"token"}
```

`key_type` is `"token"` for SDK-authenticated requests, `"ip"` for unauthenticated.
`retry_after` is in whole seconds (minimum 1).
