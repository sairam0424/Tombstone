-- Migration 023: DATA-2 targeted indexes
--
-- Five indexes for query patterns that already run in production code but
-- had no supporting index — each verified against a real call site, not
-- added speculatively:
--   * flag_environments(environment): environments.go's SDK snapshot query
--     (GET /environments/snapshot, the hot path) filters `fe.environment = $1`
--     joined to flags; flag_environments' only existing index is its PK
--     (flag_id, environment), which can't be used for an environment-only
--     filter since flag_id is the leading column.
--   * change_requests(status, created_at): change_requests.go/scheduled.go/
--     compliance.go all filter `status = 'PENDING'`/`status != 'PENDING'`;
--     this table has no index at all beyond its PK today.
--   * flags(project_id, state): environments.go's snapshot query and
--     governance.go's project health rollup both filter `project_id = $N AND
--     state = 'ACTIVE'`; flags' only non-PK index is the pgvector embedding
--     index, unrelated to this filter.
--   * audit_log(created_at): the existing idx_audit_flag_key_ts/idx_audit_env_ts
--     indexes only help queries scoped to a specific flag_key or environment;
--     system-wide reads (GetEvidence, an unscoped Verify) have no index to use.
--   * scim_users(email): orphan_detector.go's daily detectAndReport scans
--     every ACTIVE flag and, for each one, runs a correlated
--     `WHERE su.email = f.owner_id AND su.active = true` subquery against
--     scim_users — currently a sequential scan per flag; scim_users has no
--     index beyond its PK (external_id) and a UNIQUE constraint on user_id.
--
-- All are additive (CREATE INDEX IF NOT EXISTS) — no application code change,
-- no data migration, safe to apply against a live database.

CREATE INDEX IF NOT EXISTS idx_flag_environments_environment
    ON flag_environments(environment);

CREATE INDEX IF NOT EXISTS idx_change_requests_status_created_at
    ON change_requests(status, created_at);

CREATE INDEX IF NOT EXISTS idx_flags_project_id_state
    ON flags(project_id, state);

CREATE INDEX IF NOT EXISTS idx_audit_log_created_at
    ON audit_log(created_at);

CREATE INDEX IF NOT EXISTS idx_scim_users_email
    ON scim_users(email);
