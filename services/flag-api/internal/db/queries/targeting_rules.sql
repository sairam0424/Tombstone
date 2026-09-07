-- name: InsertTargetingRule :one
INSERT INTO targeting_rules (flag_id, environment, rule_type, attribute, operator, values, variation, priority)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, flag_id, environment, rule_type, attribute, operator, values, variation, priority,
          EXTRACT(EPOCH FROM created_at)::bigint AS created_at;

-- name: ListTargetingRulesForFlag :many
SELECT tr.id, tr.flag_id, tr.environment, tr.rule_type, tr.attribute, tr.operator, tr.values, tr.variation, tr.priority,
       EXTRACT(EPOCH FROM tr.created_at)::bigint AS created_at
FROM targeting_rules tr
JOIN flags f ON f.id = tr.flag_id
WHERE f.key = $1 AND tr.environment = $2 AND f.project_id = $3
ORDER BY tr.priority ASC, tr.created_at ASC;

-- name: DeleteTargetingRule :execrows
DELETE FROM targeting_rules tr
USING flags f
WHERE f.id = tr.flag_id
  AND f.key = $1
  AND tr.environment = $2
  AND tr.id = $3
  AND f.project_id = $4;
