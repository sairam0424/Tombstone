-- name: GetEnvironmentSnapshot :many
SELECT fe.flag_id, f.key, fe.environment, fe.enabled, fe.rollout_pct, f.safe_default,
       EXTRACT(EPOCH FROM fe.updated_at)::bigint AS updated_at
FROM flag_environments fe
JOIN flags f ON f.id = fe.flag_id
WHERE fe.environment = $1 AND f.state = 'ACTIVE' AND f.project_id = $2
ORDER BY f.key;

-- name: GetEnvironmentSnapshotPrerequisites :many
SELECT fp.flag_id, fp.id, fp.prereq_flag_key, fp.required_variation, fp.gate, fp.priority
FROM flag_prerequisites fp
JOIN flag_environments fe ON fe.flag_id = fp.flag_id
JOIN flags f ON f.id = fp.flag_id
WHERE fe.environment = $1 AND f.state = 'ACTIVE' AND f.project_id = $2
ORDER BY fp.flag_id, fp.priority ASC, fp.created_at ASC;

-- name: GetEnvironmentSnapshotTargetingRules :many
SELECT tr.flag_id, tr.id, tr.rule_type, tr.attribute, tr.operator, tr.values, tr.variation, tr.priority
FROM targeting_rules tr
JOIN flags f ON f.id = tr.flag_id
WHERE tr.environment = $1 AND f.state = 'ACTIVE' AND f.project_id = $2
ORDER BY tr.flag_id, tr.priority ASC, tr.created_at ASC;
