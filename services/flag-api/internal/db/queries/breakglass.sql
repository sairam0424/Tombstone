-- name: ConsumeBreakGlassToken :one
-- $3 is nullable — NULL means "no project scope requested". A DATA-1b PR2
-- adversarial review found that an earlier version of this query used an
-- empty-string sentinel with the project_id COLUMN cast to ::text, which
-- silently turned this into a case-SENSITIVE text compare; project_id is a
-- client-controlled HTTP header with no case normalization, so a
-- same-project caller whose header casing merely differed from the
-- canonical lowercase stored value got wrongly rejected with 403. Using
-- sqlc.narg + casting the PARAMETER (not the column) to ::uuid in every
-- occurrence keeps $3 in one single, consistent type context (satisfying
-- the AUD-1 lesson this was originally written to avoid) while restoring
-- uuid's case-insensitive equality semantics the original dynamically-built
-- "project_id = $3::uuid" clause had.
UPDATE break_glass_tokens
SET used = true, used_at = now(), used_by = $1
WHERE token_hash = $2
  AND used = false
  AND expires_at > now()
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id IS NULL OR project_id = sqlc.narg(project_id)::uuid)
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
