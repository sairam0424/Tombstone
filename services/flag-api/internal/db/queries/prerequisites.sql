-- name: ResolveFlagIDByKey :one
SELECT id FROM flags WHERE key = $1 AND project_id = $2;

-- name: FlagExistsInProject :one
SELECT EXISTS(SELECT 1 FROM flags WHERE key = $1 AND project_id = $2);

-- name: InsertPrerequisite :one
INSERT INTO flag_prerequisites (flag_id, prereq_flag_key, required_variation, gate, priority)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, flag_id, prereq_flag_key, required_variation, gate, priority,
          EXTRACT(EPOCH FROM created_at)::bigint AS created_at;

-- name: ListPrerequisitesForFlag :many
SELECT fp.id, fp.flag_id, fp.prereq_flag_key, fp.required_variation, fp.gate, fp.priority,
       EXTRACT(EPOCH FROM fp.created_at)::bigint AS created_at
FROM flag_prerequisites fp
JOIN flags f ON f.id = fp.flag_id
WHERE f.key = $1 AND f.project_id = $2
ORDER BY fp.priority ASC, fp.created_at ASC;

-- name: DeletePrerequisite :execrows
DELETE FROM flag_prerequisites fp
USING flags f
WHERE f.id = fp.flag_id
  AND f.key = $1
  AND fp.id = $2
  AND f.project_id = $3;

-- name: ListPrereqFlagKeysForFlag :many
SELECT fp.prereq_flag_key
FROM flag_prerequisites fp
JOIN flags f ON f.id = fp.flag_id
WHERE f.key = $1 AND f.project_id = $2;
