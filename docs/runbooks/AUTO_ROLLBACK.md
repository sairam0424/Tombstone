# Runbook: Auto-Rollback

## Quick Reference

| Symptom | Likely Cause | Action |
|---------|-------------|--------|
| Flag disabled unexpectedly in production | Auto-rollback fired (error rate >5%) | Check audit_log for `kill_switch_activated` by actor `evaluator` |
| Auto-rollback not firing despite errors | SDK telemetry not wired up | Verify SDKs are calling POST /api/v1/telemetry at :8082 |
| Rollback fires too frequently | Error rate threshold too sensitive | Check if errors are from the flag or unrelated systems |
| Flag re-enabled but rolls back again immediately | Root cause not fixed | Fix the underlying error before re-enabling |

---

## How Auto-Rollback Works End-to-End

Auto-rollback is triggered by the **circuit breaker** in the evaluator service (`services/evaluator/internal/circuit/breaker.go`). Here is the full causal chain:

### Step 1: SDK Telemetry Ingestion

Application SDKs POST evaluation results to the evaluator:

```
POST http://localhost:8082/api/v1/telemetry
Authorization: Bearer {SDK_KEY}
Content-Type: application/json

{
  "flag_key": "checkout-v2",
  "environment": "production",
  "result": "error",  // or "success"
  "latency_ms": 45
}
```

The evaluator accumulates these in a **10-second sliding window** per flag.

### Step 2: Circuit Breaker Evaluation

After each window, the evaluator checks `ShouldTrip()`:

```go
func (b *Breaker) ShouldTrip(w Window) bool {
    if w.TotalCount < 100 { return false }  // minimum traffic gate
    return b.ErrorRate(w) > 0.05           // 5% error rate threshold
}
```

**The circuit will NOT trip if total requests in the window < 100.** This prevents false trips on low-traffic flags.

### Step 3: OnTrip Fires — Kill Switch Called

When `ShouldTrip()` returns true, the evaluator calls the flag-api kill-switch:

```
POST http://flag-api:8081/api/v1/flags/{flagKey}/kill-switch
Authorization: Bearer {FLAG_API_TOKEN}
Body: { "environment": "production", "reason": "auto-rollback: error rate 6.2% exceeded threshold 5.0%" }
```

The kill-switch endpoint:
1. Sets `flag_environments.enabled = false`
2. Writes audit log entry (`event_type: kill_switch_activated`, actor: `evaluator`)
3. Publishes Redis event → gateway fans out to all connected SDKs
4. All SDKs serve `safeDefault` within milliseconds

---

## Verifying a Rollback Happened

```sql
-- Find recent auto-rollback events
SELECT id, flag_key, environment, actor, event_type, new_state, created_at
FROM audit_log
WHERE event_type = 'kill_switch_activated'
  AND actor = 'evaluator'
ORDER BY created_at DESC
LIMIT 10;
```

Or via REST:
```bash
curl "http://localhost:8081/api/v1/audit?flag_key=checkout-v2&limit=20" \
  -H "Authorization: Bearer $FLAG_API_TOKEN"
```

Look for `event_type: "kill_switch_activated"` with `actor: "evaluator"`. The `new_state` field contains the error rate at time of trip.

---

## Re-Enabling a Flag After Auto-Rollback

**Never re-enable at full rollout immediately.** Follow this procedure:

1. **Confirm root cause is fixed** — check application error logs, deployment history
2. **Re-enable at low rollout** (5-10%):
   ```bash
   curl -X POST http://localhost:8081/api/v1/flags/checkout-v2/environments/production \
     -H "Authorization: Bearer $FLAG_API_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"enabled": true, "rollout_pct": 5}'
   ```
3. **Monitor error rates** for at least 2 minutes (the CB window is 10s but allow several cycles)
4. **Gradually increase**: 5% → 25% → 50% → 100% with monitoring between each step
5. **Use a change request** for high-blast-radius flags (see `POST /api/v1/change-requests`)

---

## The ResilientClient Used for Kill-Switch Calls

The evaluator calls flag-api using a resilient HTTP client configured with:
- `MaxRetries: 1` (single retry on transient failure)
- `OpenDuration: 15s` (evaluator's own CB for the flag-api call stays open 15s before probing)

If flag-api is unreachable during an auto-rollback attempt, the evaluator will retry once then log a warning. The Redis CB state is still set to OPEN, so further evaluation is affected, but the actual `flag_environments.enabled` flip may not have happened. In this case, re-enabling the flag will work normally once flag-api recovers.

---

## Disabling Auto-Rollback for a Specific Flag

There is no per-flag CB disable toggle in v1.2.1. Options:
- Set `safe_default` to a value that won't cause harm if the flag is auto-disabled
- Use break-glass tokens for emergency human overrides: `POST /api/v1/break-glass/tokens`
- Increase the evaluator's `MinRequests` threshold via environment variable (affects all flags)
