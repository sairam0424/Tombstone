-- name: FlagExistsInProjectNotArchived :one
SELECT EXISTS(SELECT 1 FROM flags WHERE key=$1 AND project_id=$2 AND state != 'ARCHIVED');

-- name: CreateScheduledChange :one
INSERT INTO scheduled_changes
    (id, flag_key, environment, scheduled_for, change_payload, created_by, status, project_id)
VALUES ($1, $2, $3, $4, $5, $6, 'PENDING', $7)
RETURNING
    id, flag_key, environment,
    EXTRACT(EPOCH FROM scheduled_for)::bigint AS scheduled_for,
    change_payload, created_by, status,
    executed_at,
    error_message,
    EXTRACT(EPOCH FROM created_at)::bigint AS created_at;

-- name: ListScheduledChanges :many
-- $3/$4 are always plain strings, "" meaning "no filter" — a single static
-- query using an empty-string sentinel instead of conditionally appending
-- clauses with a variable placeholder count. environment/status are both
-- plain TEXT columns compared to a TEXT parameter, so (unlike a uuid column)
-- there is no cast-direction ambiguity to worry about here at all.
SELECT
    id, flag_key, environment,
    EXTRACT(EPOCH FROM scheduled_for)::bigint AS scheduled_for,
    change_payload, created_by, status,
    executed_at,
    error_message,
    EXTRACT(EPOCH FROM created_at)::bigint AS created_at
FROM scheduled_changes
WHERE flag_key = $1 AND project_id = $2
  AND (sqlc.arg(environment_filter)::text = '' OR environment = sqlc.arg(environment_filter)::text)
  AND (sqlc.arg(status_filter)::text = '' OR status = sqlc.arg(status_filter)::text)
ORDER BY scheduled_for ASC;

-- name: CancelScheduledChange :execrows
UPDATE scheduled_changes
SET status = 'CANCELLED'
WHERE id = $1 AND flag_key = $2 AND status = 'PENDING' AND project_id = $3;

-- name: GetScheduledChangeStatus :one
SELECT status FROM scheduled_changes WHERE id=$1 AND flag_key=$2 AND project_id=$3;
