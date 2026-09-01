-- Migration 017: project_id on audit_log (TEN-1a-2).
--
-- An adversarial review of TEN-1a-1 confirmed GET /api/v1/audit as a live
-- HIGH-severity leak: audit_log had no project_id column, so ListAuditLog
-- returned every project's full audit history -- including prev_state/
-- new_state flag snapshots -- to any authenticated caller holding the
-- lowest role (VIEWER). Separately, the audit hash chain groups entries by
-- flag_key alone (internal/audit/verify.go's chain grouping, and Append's
-- chain-tip lookup), and flags.key is unique only per (project_id, key), so
-- two projects with a same-keyed flag were already sharing one hash chain --
-- a correctness bug independent of the read leak.
--
-- Nullable, not NOT NULL: existing rows cannot be reliably attributed to a
-- project after the fact (flag_key alone does not disambiguate which
-- project's flag an old row was about, and break-glass/some system events
-- are legitimately project-less by design). Every project-scoped query added
-- alongside this migration matches with `=`/`IS NOT DISTINCT FROM` against a
-- specific project_id, so legacy and intentionally-project-less rows are
-- excluded from any single project's view rather than guessed into one.
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id);
CREATE INDEX IF NOT EXISTS idx_audit_log_project_id ON audit_log(project_id);
-- Supports both the project-scoped chain-tip lookup in Append and Verify's
-- per-(project_id, flag_key) chain grouping.
CREATE INDEX IF NOT EXISTS idx_audit_log_project_flag_key_ts ON audit_log(project_id, flag_key, created_at DESC);
