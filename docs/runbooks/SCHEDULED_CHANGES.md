# Runbook: Scheduled Changes

## Quick Reference

| Symptom | Likely Cause | Action |
|---------|-------------|--------|
| Scheduled change didn't fire at expected time | Status still PENDING | Check `scheduled_for` time, verify scheduler is running |
| Change shows FAILED status | Transient DB error or flag not found | Check `error_message`, decide if worth recreating |
| Same change executed twice | Two flag-api replicas raced | Should not happen with SKIP LOCKED — check if FOR UPDATE is working |
| Change stuck in FAILED with retries remaining | Persistent error (flag deleted?) | Manually resolve or recreate the change |

---

## How the Scheduler Works

The scheduled-change executor (`services/flag-api/internal/scheduler/scheduler.go`) runs as a background goroutine inside flag-api. It:

1. **Ticks every 30 seconds** (`tickInterval = 30 * time.Second`)
2. Selects due changes: `PENDING` rows where `scheduled_for <= NOW()`, plus `FAILED` rows with remaining retries where `next_retry_at <= NOW()`
3. Executes each change: updates `flag_environments`, writes audit log, publishes Redis event
4. Marks executed changes as `EXECUTED` or `FAILED`

### FOR UPDATE SKIP LOCKED

The SELECT query uses `FOR UPDATE SKIP LOCKED`:

```sql
SELECT id, flag_key, environment, change_payload
FROM scheduled_changes
WHERE (status = 'PENDING' AND scheduled_for <= NOW())
   OR (status = 'FAILED' AND retry_count < max_retries AND next_retry_at <= NOW())
ORDER BY scheduled_for ASC
FOR UPDATE SKIP LOCKED
```

This is critical for multi-replica deployments: each flag-api instance claims a disjoint batch of rows. Two replicas will never execute the same change simultaneously — one wins the lock, the other skips that row and moves to the next. Without this, duplicate audit entries and duplicate Redis events would occur.

### Retry Schedule

From `scheduler.go` constants:

```go
baseRetryDelay = 1 * time.Minute   // attempt 1
maxRetryDelay  = 4 * time.Minute   // cap
// attempt 1: 1 min, attempt 2: 2 min, attempt 3: 4 min (max_retries=3 default)
```

Formula: `delay = baseRetryDelay × 2^(retryCount-1)`, capped at `maxRetryDelay`.

**Terminal FAILED**: when `retry_count >= max_retries`, the row is permanently terminal. `next_retry_at` is set to NULL. The row will never be picked up again.

---

## Querying Scheduled Changes

```sql
-- Pending changes due in the next hour
SELECT id, flag_key, environment, scheduled_for, status
FROM scheduled_changes
WHERE status = 'PENDING' AND scheduled_for <= NOW() + INTERVAL '1 hour'
ORDER BY scheduled_for;

-- Failed changes with retries remaining
SELECT id, flag_key, environment, retry_count, max_retries, next_retry_at, error_message
FROM scheduled_changes
WHERE status = 'FAILED' AND retry_count < max_retries
ORDER BY next_retry_at;

-- Permanently failed (terminal) changes
SELECT id, flag_key, environment, error_message, retry_count, created_at
FROM scheduled_changes
WHERE status = 'FAILED' AND retry_count >= max_retries
ORDER BY created_at DESC
LIMIT 20;

-- Recent activity for a specific flag
SELECT id, status, scheduled_for, executed_at, error_message
FROM scheduled_changes
WHERE flag_key = 'checkout-v2'
ORDER BY created_at DESC
LIMIT 10;
```

---

## Manually Triggering a Stuck Change

The scheduler picks up due changes every 30 seconds automatically. If a change appears stuck:

1. **Verify the scheduler is running** — check flag-api logs for `"scheduled-change executor starting"` on startup
2. **Check the status directly** — `SELECT status, next_retry_at, error_message FROM scheduled_changes WHERE id = '{id}'`
3. **For PENDING changes not executing**: check `scheduled_for <= NOW()`. If yes and still PENDING after 60+ seconds, the scheduler may have crashed. Restart flag-api.
4. **For FAILED with retries remaining**: wait for `next_retry_at` to pass — the next 30s tick will pick it up
5. **For terminal FAILED**: recreate the change via `POST /api/v1/flags/{key}/scheduled-changes`

### Forcing Immediate Retry

Reset a FAILED change's `next_retry_at` to force it on the next tick:

```sql
UPDATE scheduled_changes
SET next_retry_at = NOW() - INTERVAL '1 second'
WHERE id = '{change-id}' AND status = 'FAILED' AND retry_count < max_retries;
```

---

## Understanding FAILED vs Terminal FAILED

| State | `status` | `retry_count` vs `max_retries` | `next_retry_at` | Meaning |
|-------|----------|-------------------------------|-----------------|---------|
| Retrying | `FAILED` | `retry_count < max_retries` | set to future time | Will be retried at `next_retry_at` |
| Terminal | `FAILED` | `retry_count >= max_retries` | NULL | Permanently failed — must be recreated |
| Executed | `EXECUTED` | any | any | Successfully applied |
| Pending | `PENDING` | 0 | NULL | Not yet due |

**All error types use the same retry logic** — including "flag not found" which will never succeed. This is intentional (see comments in `markFailed()`): the cost is at most 7 minutes of extra retries vs the complexity of classifying transient vs permanent errors.

---

## Checking Scheduler Logs

```bash
# Via Docker Compose
bash scripts/dev-local.sh logs flag-api | grep scheduler

# Key log lines to look for:
# "scheduled-change executor starting" — scheduler initialized successfully
# "scheduler: applied scheduled change" — success
# "scheduler: change failed, will retry" — transient failure
# "scheduler: retry budget exhausted, permanently FAILED" — terminal failure
```
