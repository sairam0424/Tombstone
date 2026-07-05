# Runbook: Circuit Breaker

## Quick Reference

| Symptom | Likely Cause | Action |
|---------|-------------|--------|
| Flag auto-disabled in production | CB tripped (error rate >5% / 100 req / 10s) | Check Redis state, verify audit_log for kill-switch event |
| Flag stays OFF after incident resolved | CB in OPEN state | Manual reset via Redis |
| CB trips on noisy-neighbor flag | Too-low traffic threshold | Check TotalCount < 100 (CB won't trip below minimum) |
| Multiple flags rolled back simultaneously | Widespread error event hit multiple CBs | Investigate upstream infra, re-enable flags one-by-one after fix |

---

## How the Circuit Breaker Works

The circuit breaker (`services/evaluator/internal/circuit/breaker.go`) protects production by automatically disabling a flag when its error rate exceeds configured thresholds.

### State Machine

```
CLOSED (normal) ──► OPEN (tripped, flag disabled) ──► HALF_OPEN (probing)
     ▲                                                       │
     └───────────────────── recovery ───────────────────────┘
```

- **CLOSED**: Normal operation. Error rates within threshold.
- **OPEN**: Flag disabled. Auto-rollback has fired. `safeDefault` is returned to all SDK callers.
- **HALF_OPEN**: Probe period (5 minutes). Evaluator monitors whether errors have subsided before returning to CLOSED.

### Trip Thresholds

From `NewBreaker()` defaults:

```go
ErrorRateThreshold: 0.05   // 5% error rate
MinRequests:        100    // minimum 100 requests in window before tripping
WindowDuration:     10s    // measurement window
ObservationWindow:  5m     // HALF_OPEN probe duration
```

The breaker will NOT trip if `TotalCount < 100`, regardless of error rate. This prevents false trips during low-traffic windows.

### Redis State Keys

Circuit state per flag is stored in Redis:

```
circuit:{flagKey}:state    → "CLOSED" | "OPEN" | "HALF_OPEN"
```

TTL is set by `SetState()` and varies by state — OPEN state is set with the `ObservationWindow` (5m) TTL so it naturally expires back to CLOSED if the evaluator doesn't keep refreshing it.

### OnTrip: What Auto-Rollback Actually Does

When `ShouldTrip()` returns true, the evaluator's rollback executor calls the flag-api kill-switch endpoint:

```
POST /api/v1/flags/{flagKey}/kill-switch
Authorization: Bearer {FLAG_API_TOKEN}
Body: { "environment": "production", "reason": "auto-rollback: error rate exceeded threshold" }
```

This flips `flag_environments.enabled = false` and publishes a Redis event. All connected SDKs receive the update via SSE within milliseconds and begin serving `safeDefault`.

An audit log entry is written with `event_type: kill_switch_activated`, actor: `evaluator`, visible at `GET /api/v1/audit?flag_key={key}`.

---

## Checking Current Circuit State

```bash
# Check state for a specific flag
redis-cli GET "circuit:checkout-v2:state"
# → "CLOSED" | "OPEN" | "HALF_OPEN" | (nil = no state = CLOSED)

# List all flags in OPEN state
redis-cli KEYS "circuit:*:state" | xargs -I{} sh -c 'echo "{}: $(redis-cli GET {})"' | grep OPEN
```

---

## Manually Resetting a Circuit Breaker

After resolving the underlying incident, re-enable the flag via the API (not directly in Redis):

```bash
# 1. Re-enable the flag via flag-api
curl -X POST http://localhost:8081/api/v1/flags/{flagKey}/environments/production \
  -H "Authorization: Bearer $FLAG_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true, "rollout_pct": 5}'

# 2. Clear the Redis CB state (optional — it will also expire naturally)
redis-cli DEL "circuit:checkout-v2:state"
```

**Important**: Start the rollout at a low percentage (5-10%) after a CB trip. Validate error rates are stable before increasing. The CB will re-trip immediately if the root cause was not fixed.

---

## Troubleshooting

**CB tripped but no incident occurred**
- Check if a deploy happened in the window. New deploys can spike error rates transiently.
- Check `audit_log` for `kill_switch_activated` entries — the `new_state` field includes the CB's error rate reading.
- If it was a false positive, re-enable at low rollout percentage and monitor.

**CB not tripping despite high errors**
- Verify traffic is reaching the evaluator (check `/readyz` at :8082).
- The SDK telemetry endpoint (`POST /api/v1/telemetry`) must be called by SDKs — verify SDK is wired up correctly.
- Check `MinRequests`: if the flag has fewer than 100 requests in the 10s window, the CB will not trip.

**HALF_OPEN state lasts longer than expected**
- `ObservationWindow` is 5 minutes. This is intentional.
- Do not forcibly clear HALF_OPEN state — let the evaluator's probe cycle complete naturally.
