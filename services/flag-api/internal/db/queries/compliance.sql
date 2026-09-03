-- name: CountAuditLogEntries :one
SELECT COUNT(*) FROM audit_log;

-- name: ChangeRequestApprovalStats :one
SELECT
    COUNT(*) FILTER (WHERE status IN ('APPROVED','APPLIED')) AS approved,
    COUNT(*) AS total
FROM change_requests
WHERE status != 'PENDING';

-- name: CountRecentBreakGlassUses :one
SELECT COUNT(*)
FROM break_glass_tokens
WHERE used = true
  AND used_at >= now() - INTERVAL '90 days';

-- name: CountActiveServiceTokens :one
SELECT COUNT(*) FROM service_tokens WHERE revoked_at IS NULL;

-- name: CountRoleAssignments :one
SELECT COUNT(*) FROM user_roles;

-- name: ExportAuditLogForProject :many
SELECT id, COALESCE(flag_key,'') AS flag_key, COALESCE(environment,'') AS environment, actor, event_type,
       COALESCE(prev_state::text,'null')::text AS prev_state, COALESCE(new_state::text,'null')::text AS new_state,
       COALESCE(ip_address,'') AS ip_address, COALESCE(prev_hash,'') AS prev_hash,
       EXTRACT(EPOCH FROM created_at)::bigint AS created_at,
       COALESCE(rekor_log_id,'') AS rekor_log_id, rekor_log_index
FROM audit_log
WHERE project_id = $1
ORDER BY created_at ASC;
