-- Migration 023: DATA-2 targeted indexes
--
-- Five indexes for query patterns that already run in production code but
-- had no supporting index — each verified against a real call site, not
-- added speculatively. (Re-verified by an adversarial review pass after the
-- first draft of this file: four of the five justifications below were
-- corrected — miscited or incomplete call sites, not unsafe/duplicate
-- indexes — and the audit_log index changed shape as a result. See each
-- bullet.)
--   * flag_environments(environment): environments.go's SDK snapshot query
--     (GET /environments/snapshot, the hot path) filters `fe.environment = $1`
--     joined to flags; flag_environments' only existing index is its PK
--     (flag_id, environment), which can't be used for an environment-only
--     filter since flag_id is the leading column.
--   * change_requests(status, created_at): the real, sargable beneficiary is
--     ListChangeRequests (change_requests.go) — `WHERE status = $1 AND
--     project_id = $2 ORDER BY created_at DESC LIMIT 100`. compliance.go's
--     `status != 'PENDING'` filter is an inequality and can't seek off this
--     index's leading column; scheduled.go's status filters run against the
--     separate scheduled_changes table, not this one — both were miscited in
--     an earlier draft. change_requests already carries idx_change_requests_
--     project_id (018) and a partial unique index on (project_id, flag_key,
--     environment) (020); neither overlaps (status, created_at)'s columns,
--     so this index is still new and non-duplicate — the table was not
--     actually index-free before this migration, as an earlier draft of this
--     comment claimed.
--   * flags(project_id, state): environments.go's snapshot query and
--     governance.go's project health rollup both filter `project_id = $N AND
--     state = 'ACTIVE'`. flags also already carries an implicit UNIQUE
--     (project_id, key) index and the pgvector embedding index; neither
--     overlaps (project_id, state)'s columns.
--   * audit_log(project_id, created_at) — NOT a bare created_at index (an
--     earlier draft of this migration added one, justified by GetEvidence/
--     Verify; on review, GetEvidence's Verify(ctx, "") call is an unscoped
--     full scan with a 4-key ORDER BY where created_at is only third, and its
--     other audit_log read is a bare COUNT(*) — a single-column created_at
--     index serves neither. The real, frequent beneficiary is ListAuditLog
--     (audit.go), which is ALWAYS project_id-scoped: `WHERE project_id = $6
--     AND created_at BETWEEN ... ORDER BY created_at DESC LIMIT $5`. Leading
--     with project_id lets Postgres seek directly to this project's rows in
--     created_at order instead of scanning past every other project's more
--     recent entries first — the difference matters once audit_log spans
--     many of this product's 5,000+ flags' worth of projects.
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

CREATE INDEX IF NOT EXISTS idx_audit_log_project_id_created_at
    ON audit_log(project_id, created_at);

CREATE INDEX IF NOT EXISTS idx_scim_users_email
    ON scim_users(email);
