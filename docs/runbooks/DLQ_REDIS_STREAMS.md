# Runbook: Redis Streams DLQ

## Quick Reference

| Symptom | Likely Cause | Action |
|---------|-------------|--------|
| SDK clients not receiving flag updates | Messages stuck in PEL | Check XPENDING, replay via POST /internal/dlq/{env}/replay |
| DLQ stream depth growing | Poison messages (invalid JSON payload) | Inspect DLQ with XRANGE, identify bad messages, drop or fix |
| "message moved to dead-letter stream" in gateway logs | Normal — message failed 3 delivery attempts | Review message content, fix upstream publisher if systematic |
| Gateway log: "missing payload field, leaving pending" | Flag event published without `payload` key | Check flag-api/scheduler publish code for the stream |

---

## How Redis Streams DLQ Works

Tombstone uses Redis Streams (`tombstone:stream:{environment}`) for real-time flag event delivery to SDK clients via the gateway.

The gateway runs a consumer group (`gateway-workers`) with one named consumer per replica (`gateway-{hostname}`). Messages that fail processing enter the **Pending Entries List (PEL)** — a per-consumer-group accounting of messages that have been delivered but not yet acknowledged.

### DLQ Flow (from `services/gateway/internal/hub/dlq.go`)

```
Flag event published to tombstone:stream:production
         │
         ▼
gateway XREADGROUP reads message, attempts delivery to SSE clients
         │
    success → XACK → message leaves PEL
    failure → message stays in PEL (delivery count increments)
         │
         ▼ (after reclaimIdleThreshold = 30s idle)
ReclaimStalePending sweep (runs every 15s in background goroutine):
  - delivery count < 3  → XCLAIM + reprocess inline
  - delivery count >= 3 → deadLetter: XADD to tombstone:stream:{env}:dlq
                                       XACK off primary stream PEL
```

### Key Constants

From `dlq.go`:
```go
maxDeliveryAttempts  = 3
reclaimIdleThreshold = 30 * time.Second
dlqMaxLen            = 10_000  // DLQ stream cap (approximate)
reclaimScanCount     = 100     // PEL entries inspected per sweep
```

---

## Inspecting the PEL

```bash
# View pending entries for the gateway-workers consumer group
redis-cli XPENDING tombstone:stream:production gateway-workers - + 10

# Extended form (shows idle time and delivery count per entry)
redis-cli XPENDING tombstone:stream:production gateway-workers IDLE 0 - + 10

# Check DLQ stream depth
redis-cli XLEN tombstone:stream:production:dlq

# Read DLQ messages
redis-cli XRANGE tombstone:stream:production:dlq - + COUNT 10
```

---

## Checking DLQ via API

The gateway exposes authenticated DLQ management endpoints. Authentication requires the `FLAG_API_TOKEN` as a Bearer token.

```bash
# List DLQ messages for an environment
curl -H "Authorization: Bearer $FLAG_API_TOKEN" \
  http://localhost:8080/internal/dlq/production

# Replay DLQ messages (reprocesses and removes from DLQ)
curl -X POST -H "Authorization: Bearer $FLAG_API_TOKEN" \
  http://localhost:8080/internal/dlq/production/replay
```

**Note**: These endpoints are on the gateway (:8080), not flag-api (:8081). The `FLAG_API_TOKEN` env var must be set in the gateway for auth to work. When unset, the gateway fails-open (no auth check) — this is intentional for local dev but should not be the case in production.

---

## Identifying a Poison Message

A poison message is one that consistently fails unmarshal. Symptoms:
- Same message ID appears repeatedly in XPENDING with increasing delivery count.
- Gateway logs: `"dlq: reclaimed message still fails to unmarshal, leaving pending"`

```bash
# Find the message ID from XPENDING, then read its raw content
redis-cli XRANGE tombstone:stream:production {msg-id} {msg-id}

# The `payload` field should contain valid JSON matching FlagEvent:
# { "flag_key": "...", "enabled": true, "rollout_pct": 50, "ts": 123456789, "environment": "production" }
```

If the payload is malformed JSON (e.g. truncated, wrong field types), it will exhaust its 3 attempts and be moved to the DLQ automatically within ~90 seconds.

---

## Alert Thresholds

| Metric | Warning | Critical | How to check |
|--------|---------|----------|-------------|
| PEL depth | >10 messages | >50 messages | `XPENDING ... - + 1000` |
| DLQ stream depth | >5 messages | >20 messages | `XLEN tombstone:stream:{env}:dlq` |
| Message age in PEL | >5 min | >15 min | `XPENDING ... IDLE 300000` |

A DLQ depth of 0 after each `ReclaimStalePending` sweep is the healthy baseline. Any persistent DLQ growth indicates a systematic publishing problem that should be investigated at the flag-api or scheduler level.

---

## Clearing the DLQ After Investigation

```bash
# After confirming all DLQ messages are understood/resolved:
redis-cli XTRIM tombstone:stream:production:dlq MAXLEN 0

# Or delete the DLQ stream entirely (it will be recreated on next poison message)
redis-cli DEL tombstone:stream:production:dlq
```
