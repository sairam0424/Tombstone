-- name: LockAuditChain :exec
-- Serializes appends to one chain. pg_advisory_xact_lock releases
-- automatically at commit/rollback, so a crashed writer cannot wedge it.
-- flag_key is the plain (possibly empty) string, not a NULL sentinel — "" is
-- a perfectly valid hashtext() input and locks by it consistently.
SELECT pg_advisory_xact_lock(sqlc.arg(namespace)::int, hashtext(sqlc.arg(flag_key)::text));

-- Deliberately NOT converted to sqlc: the combined "render prev_state/
-- new_state exactly as Postgres will store them, and read the chain tip's
-- entry_hash, in one round-trip" query in audit.go's Append. Empirically
-- confirmed (a throwaway probe against real Postgres) that sqlc infers a
-- NON-nullable Go output type for a bare `sqlc.narg(x)::jsonb[::text]`
-- computed column no matter how the cast is wrapped (CAST(...AS text),
-- an extra dummy column, a standalone SELECT with no FROM) — even though
-- the parameter is genuinely nullable and IS null on most Append calls
-- (most audit events have an empty PrevState or NewState). Scanning that
-- NULL into the generated non-nullable string/json.RawMessage destination
-- is a hard database/sql error, reproduced live: "unsupported Scan, storing
-- driver.Value type <nil> into type *json.RawMessage". This is the same
-- CRITICAL bug class DATA-1b PR 1/4 found for NULL-into-plain-string, just
-- for a computed expression sqlc has no override mechanism for. Matches
-- PR 1's precedent for ExportAuditLog: not every query safely fits sqlc's
-- generated shape, and this is the audit hash chain — the wrong call here
-- is not worth whatever uniformity converting it would buy.

-- name: InsertAuditEntry :exec
INSERT INTO audit_log
    (id, flag_key, environment, actor, event_type, prev_state, new_state,
     ip_address, prev_hash, entry_hash, created_at, project_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12);

-- name: CountAuditEntries :one
SELECT COUNT(*) FROM audit_log;

-- name: ListAuditLogForVerification :many
-- The project_id parameter is cast to ::uuid (never the column to text) on
-- both sides of the OR — casting a uuid column to text to compare it against
-- a bare parameter silently turns a case-insensitive uuid comparison into a
-- case-sensitive text one (DATA-1b PR 2/4 found and fixed exactly this bug
-- in breakglass.sql's ConsumeBreakGlassToken). This query already followed
-- the safe pattern before any sqlc conversion — preserved as-is.
SELECT id, COALESCE(flag_key,''), COALESCE(environment,''), actor, event_type,
       CAST(COALESCE(prev_state::text,'') AS text) AS prev_state_text,
       CAST(COALESCE(new_state::text,'') AS text) AS new_state_text,
       COALESCE(ip_address,''), COALESCE(prev_hash,''), COALESCE(entry_hash,''),
       created_at, CAST(COALESCE(project_id::text,'') AS text) AS project_id_text
FROM audit_log
WHERE sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid
ORDER BY COALESCE(project_id::text,''), COALESCE(flag_key,''), created_at ASC, id ASC;

-- name: ListAuditRetentionCheckpoints :many
SELECT CAST(COALESCE(project_id::text,'') AS text) AS project_id_text, flag_key, pruned_through_hash,
       pruned_through_created_at, signature
FROM audit_retention_checkpoints
WHERE sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid;
