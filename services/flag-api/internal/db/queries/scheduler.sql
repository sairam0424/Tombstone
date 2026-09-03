-- name: SelectDueScheduledChanges :many
SELECT id, flag_key, environment, change_payload, project_id
FROM scheduled_changes
WHERE (status = 'PENDING' AND scheduled_for <= NOW())
   OR (status = 'FAILED' AND retry_count < max_retries AND next_retry_at <= NOW())
ORDER BY scheduled_for ASC
FOR UPDATE SKIP LOCKED;

-- name: GetCurrentFlagEnvironmentState :one
SELECT fe.enabled, fe.rollout_pct, fe.flag_id
FROM flag_environments fe
JOIN flags f ON f.id = fe.flag_id
WHERE f.key = $1 AND fe.environment = $2 AND f.project_id = $3;

-- name: ApplyScheduledFlagEnvironmentUpdate :execrows
UPDATE flag_environments fe
SET enabled = $1, rollout_pct = $2, updated_at = now(), updated_by = 'scheduler'
FROM flags f
WHERE f.id = fe.flag_id AND f.key = $3 AND fe.environment = $4 AND f.project_id = $5;

-- name: MarkScheduledChangeExecuted :exec
UPDATE scheduled_changes
SET status = 'EXECUTED', executed_at = NOW()
WHERE id = $1;

-- name: GetScheduledChangeRetryState :one
SELECT retry_count, max_retries FROM scheduled_changes WHERE id = $1;

-- name: MarkScheduledChangeFailedNoRetryState :exec
UPDATE scheduled_changes
SET status = 'FAILED', error_message = $1
WHERE id = $2;

-- name: MarkScheduledChangeFailedRetryPending :exec
UPDATE scheduled_changes
SET status = 'FAILED', error_message = $1, retry_count = $2, next_retry_at = $3
WHERE id = $4;

-- name: MarkScheduledChangeFailedTerminal :exec
UPDATE scheduled_changes
SET status = 'FAILED', error_message = $1, retry_count = $2, next_retry_at = NULL
WHERE id = $3;
