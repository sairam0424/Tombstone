-- name: GetTokenWatermark :one
SELECT valid_after FROM user_token_watermarks WHERE user_email = lower($1);

-- name: ResolveServiceToken :one
SELECT name, role, project_id FROM service_tokens
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: InsertIdempotencyKey :one
INSERT INTO idempotency_keys (actor, idempotency_key, endpoint, request_hash)
VALUES ($1, $2, $3, $4)
ON CONFLICT (actor, idempotency_key, endpoint) DO NOTHING
RETURNING id;

-- name: UpdateIdempotencyKeyResponse :exec
UPDATE idempotency_keys
SET response_status = $1, response_body = $2, completed_at = now()
WHERE id = $3;

-- name: GetIdempotencyKey :one
SELECT request_hash, completed_at, response_status, response_body
FROM idempotency_keys
WHERE actor = $1 AND idempotency_key = $2 AND endpoint = $3;

-- name: PurgeExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE expires_at < NOW();

-- name: GetUserRole :one
SELECT role FROM user_roles WHERE user_id = $1 AND project_id = $2;

-- name: IsProjectMember :one
SELECT EXISTS(SELECT 1 FROM user_roles WHERE user_id=$1 AND project_id=$2);

-- name: InsertMFALogEvent :exec
INSERT INTO user_mfa_log (user_id, event_type) VALUES ($1, $2);
