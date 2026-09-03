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

-- ExportAuditLog (ComplianceHandler.ExportAuditLog) is deliberately NOT
-- converted here -- see the doc comment at its call site in compliance.go.
-- sqlc's generated :many methods always fully materialize the result set
-- into a slice, which would regress this handler from O(1)-memory streaming
-- to O(rows)-memory buffering for what can be a large, unbounded per-project
-- audit history.
