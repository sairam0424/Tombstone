-- name: ListSCIMUsers :many
SELECT external_id, user_id, email, display_name, active
FROM scim_users
ORDER BY synced_at DESC;

-- name: UpsertSCIMUser :exec
INSERT INTO scim_users (external_id, user_id, email, display_name, active, synced_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (external_id) DO UPDATE
    SET user_id      = EXCLUDED.user_id,
        email        = EXCLUDED.email,
        display_name = EXCLUDED.display_name,
        active       = EXCLUDED.active,
        synced_at    = now();

-- name: GetSCIMUser :one
SELECT external_id, user_id, email, display_name, active
FROM scim_users
WHERE external_id = $1;

-- name: GetSCIMUserEmail :one
SELECT email FROM scim_users WHERE external_id = $1;

-- name: UpdateSCIMUser :execrows
UPDATE scim_users
SET user_id      = $1,
    email        = $2,
    display_name = $3,
    active       = $4,
    synced_at    = now()
WHERE external_id = $5;

-- name: DeprovisionSCIMUser :one
UPDATE scim_users
SET active = false, synced_at = now()
WHERE external_id = $1
RETURNING email;

-- Case-insensitive on purpose (SEC-5): user_roles.user_id is populated
-- out-of-band with no case guarantee relative to what an IdP later asserts.
-- Matching case-insensitively only ever revokes MORE broadly, never less —
-- the safe direction to err in for a deprovisioning action. Do not collapse
-- to `user_id = lower($1)`, which would make the column-side comparison
-- case-sensitive again.
-- name: RevokeUserRoles :many
DELETE FROM user_roles WHERE lower(user_id) = lower(sqlc.arg(user_email)::text) RETURNING project_id;

-- name: UpsertUserTokenWatermark :exec
INSERT INTO user_token_watermarks (user_email, valid_after)
VALUES (lower(sqlc.arg(user_email)::text), now())
ON CONFLICT (user_email) DO UPDATE SET valid_after = now();

-- name: ListActiveFlagsByOwner :many
SELECT key, project_id FROM flags
WHERE owner_id = $1 AND state = 'ACTIVE';

-- A separate query from orphan_detector.sql's CreateOrphanChangeRequest even
-- though the column list is identical: requested_by differs ('system' here
-- vs 'system-orphan-detector' there), so sharing one query would require
-- turning that literal into a bound parameter and touching
-- orphan_detector.go's already-shipped, out-of-scope call site.
-- name: CreateSCIMOrphanChangeRequest :exec
INSERT INTO change_requests
    (flag_key, environment, requested_by, status, change_payload, project_id)
VALUES ($1, 'production', 'system', 'PENDING', $2, $3);
