-- name: ListOrphanedFlags :many
SELECT f.key, f.owner_id, f.project_id
FROM flags f
WHERE f.state = 'ACTIVE'
  AND NOT EXISTS (
      SELECT 1 FROM scim_users su
      WHERE su.email = f.owner_id
        AND su.active = true
  )
ORDER BY f.key;

-- name: CreateOrphanChangeRequest :exec
INSERT INTO change_requests
    (flag_key, environment, requested_by, status, change_payload, project_id)
VALUES ($1, 'production', 'system-orphan-detector', 'PENDING', $2, $3);
