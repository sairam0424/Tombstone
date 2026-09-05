-- name: ListFlags :many
SELECT f.id, f.key, f.project_id, f.name, f.description,
       f.flag_type, f.state, f.owner_id, f.safe_default,
       EXTRACT(EPOCH FROM f.created_at)::bigint AS created_at,
       EXTRACT(EPOCH FROM f.updated_at)::bigint AS updated_at
FROM flags f
WHERE f.project_id = $1 AND f.state != 'ARCHIVED'
ORDER BY f.created_at DESC;

-- name: FlagTombstoneExists :one
SELECT EXISTS(SELECT 1 FROM flag_tombstones WHERE key=$1);

-- name: CreateFlag :one
INSERT INTO flags (key, project_id, name, description, flag_type, state, owner_id, safe_default)
VALUES ($1,$2,$3,$4,$5,'ACTIVE',$6,$7)
RETURNING id, key, project_id, name, description, flag_type, state, owner_id, safe_default,
          EXTRACT(EPOCH FROM created_at)::bigint AS created_at, EXTRACT(EPOCH FROM updated_at)::bigint AS updated_at;

-- name: CreateDefaultFlagEnvironment :exec
INSERT INTO flag_environments (flag_id, environment, enabled, rollout_pct, updated_by)
VALUES ($1,$2,false,0,$3) ON CONFLICT DO NOTHING;

-- name: GetFlag :one
SELECT id, key, project_id, name, description, flag_type, state, owner_id, safe_default,
       EXTRACT(EPOCH FROM created_at)::bigint AS created_at, EXTRACT(EPOCH FROM updated_at)::bigint AS updated_at
FROM flags WHERE key=$1 AND project_id=$2;

-- name: GetProjectRequireApproval :one
SELECT require_approval FROM projects WHERE id = $1;

-- Shared by flags.go's UpdateEnvironment and change_requests.go's
-- ApproveChangeRequest apply path — byte-identical SQL in both original
-- call sites, so this is one query, not two.
-- name: GetFlagEnvironmentPrevState :one
SELECT fe.flag_id, f.key, fe.environment, fe.enabled, fe.rollout_pct, f.safe_default,
       EXTRACT(EPOCH FROM fe.updated_at)::bigint AS updated_at
FROM flag_environments fe JOIN flags f ON f.id = fe.flag_id
WHERE f.key=$1 AND fe.environment=$2 AND f.project_id=$3;

-- Shared by flags.go's UpdateEnvironment and change_requests.go's
-- ApproveChangeRequest apply path — byte-identical SQL in both original
-- call sites, so this is one query, not two.
-- name: UpdateFlagEnvironment :execrows
UPDATE flag_environments fe SET enabled=$1, rollout_pct=$2, updated_at=now(), updated_by=$3
FROM flags f WHERE f.id=fe.flag_id AND f.key=$4 AND fe.environment=$5 AND f.project_id=$6;

-- name: KillSwitchFlagEnvironment :execrows
UPDATE flag_environments fe SET enabled=false, updated_at=now(), updated_by=$1
FROM flags f WHERE f.id=fe.flag_id AND f.key=$2 AND fe.environment=$3 AND f.project_id=$4;

-- name: ArchiveFlag :execrows
UPDATE flags SET state='ARCHIVED', archived_at=now() WHERE key=$1 AND project_id=$2;

-- INT-4: ArchiveFlag has no single environment of its own -- it archives
-- a flag across every environment it has state in at once. Used to
-- resolve exactly which environments to publish the eviction event to,
-- instead of hardcoding "production" (found by adversarial review of
-- PR #210 -- the hardcoded value only worked by coincidence of every
-- current deployment config happening to use "production").
-- name: ListFlagEnvironmentsForKey :many
SELECT fe.environment
FROM flag_environments fe JOIN flags f ON f.id = fe.flag_id
WHERE f.key=$1 AND f.project_id=$2;

-- name: CreateFlagTombstone :exec
INSERT INTO flag_tombstones (key, archived_by) VALUES ($1,$2) ON CONFLICT DO NOTHING;

-- rekor_log_id/rekor_log_index are nullable (migration 009); the values
-- passed at the call site are always real/valid when this fires (guarded by
-- `if subErr != nil || logID == "" { return }` before it's ever called), but
-- the columns are genuinely nullable so the generated params are sql.NullString/
-- sql.NullInt64.
-- name: BackfillAuditLogRekor :exec
UPDATE audit_log SET rekor_log_id=$1, rekor_log_index=$2 WHERE id=$3;
