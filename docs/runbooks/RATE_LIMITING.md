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
keyPrefix     = "ratelimit:sdk:"

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
# Check SDK token bucket for a specific credential
redis-cli HGETALL "ratelimit:sdk:{your-sdk-token}"
# Returns: remaining, last_refill

# Check IP bucket
redis-cli HGETALL "ratelimit:ip:192.168.1.1"

# List all SDK buckets currently tracked
redis-cli KEYS "ratelimit:sdk:*"

# Check TTL (when bucket expires/resets)
redis-cli TTL "ratelimit:sdk:{your-sdk-token}"
```

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
