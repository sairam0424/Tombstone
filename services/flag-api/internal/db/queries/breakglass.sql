-- name: ConsumeBreakGlassToken :one
-- $3 is always a plain string, "" meaning "no project scope requested" — a
-- single static query using an empty-string sentinel, not "$3=''" alongside
-- a same-parameter ::uuid cast in another branch: AUD-1 found that mixing
-- two type contexts for one parameter within one statement is exactly what
-- confuses lib/pq's extended-protocol type inference. Casting the COLUMN to
-- text (not the parameter to uuid) keeps $3 in a single, consistent type
-- context throughout.
UPDATE break_glass_tokens
SET used = true, used_at = now(), used_by = $1
WHERE token_hash = $2
  AND used = false
  AND expires_at > now()
  AND (sqlc.arg(project_id)::text = '' OR project_id IS NULL OR project_id::text = sqlc.arg(project_id)::text)
RETURNING id, scope;

-- name: GetBreakGlassTokenDiagnostics :one
SELECT used, expires_at, project_id FROM break_glass_tokens WHERE token_hash = $1;

-- name: CreateBreakGlassToken :exec
INSERT INTO break_glass_tokens (token_hash, scope, created_by, expires_at, incident_ref, project_id)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListBreakGlassTokens :many
SELECT id, scope, created_by, expires_at, used, COALESCE(used_by,''), COALESCE(incident_ref,'')
FROM break_glass_tokens
WHERE project_id = $1 OR project_id IS NULL
ORDER BY created_at DESC LIMIT 50;
