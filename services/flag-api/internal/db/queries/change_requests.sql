-- name: ListChangeRequests :many
SELECT id, flag_key, environment, requested_by, status,
       change_payload, COALESCE(approved_by, '{}'),
       rejected_by, rejection_reason,
       EXTRACT(EPOCH FROM created_at)::bigint AS created_at,
       EXTRACT(EPOCH FROM updated_at)::bigint AS updated_at
FROM change_requests
WHERE status = $1 AND project_id = $2
ORDER BY created_at DESC
LIMIT 100;

-- name: ChangeRequestTargetExists :one
SELECT true AS exists FROM flag_environments fe JOIN flags f ON f.id = fe.flag_id
WHERE f.key = $1 AND fe.environment = $2 AND f.project_id = $3;

-- name: GetProjectRequiredApprovals :one
SELECT required_approvals FROM projects WHERE id = $1;

-- name: CreateChangeRequest :one
INSERT INTO change_requests (flag_key, environment, requested_by, status, change_payload, project_id, required_approvals)
VALUES ($1, $2, $3, 'PENDING', $4, $5, $6)
RETURNING id, EXTRACT(EPOCH FROM created_at)::bigint AS created_at, EXTRACT(EPOCH FROM updated_at)::bigint AS updated_at;

-- name: GetChangeRequestForApproval :one
SELECT flag_key, environment, requested_by, change_payload, COALESCE(approved_by, '{}'), required_approvals
FROM change_requests
WHERE id = $1 AND project_id = $2 AND status = 'PENDING'
FOR UPDATE;

-- name: RecordApproval :exec
UPDATE change_requests SET approved_by = $1, status = $2, updated_at = now() WHERE id = $3;

-- name: FinalizeAppliedChangeRequest :exec
UPDATE change_requests SET approved_by = $1, status = 'APPLIED', updated_at = now() WHERE id = $2;

-- name: RejectChangeRequest :one
UPDATE change_requests
SET status           = 'REJECTED',
    rejected_by      = $1,
    rejection_reason = $2,
    updated_at       = $3
WHERE id = $4 AND status = 'PENDING' AND project_id = $5
RETURNING flag_key, environment;
