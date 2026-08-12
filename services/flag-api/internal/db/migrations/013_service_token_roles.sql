-- Migration 013: per-token roles for service tokens (SEC-1)
--
-- Before this migration every service token resolved to OPERATOR
-- (middleware/rbac.go resolveRole hardcoded it), which grants flags:write and
-- environments:write. Any third-party SDK token could therefore create flags,
-- archive flags, and change production rollout percentages.
--
-- Roles are now stored per token and default to the least-privileged VIEWER.
--
-- BREAKING (upgrade action required): existing rows are backfilled to VIEWER,
-- so first-party machine callers that WRITE through flag-api must be
-- re-provisioned explicitly with the role they need:
--   * gitops-sync        POST /flags, PATCH /flags/{key}/environments/{env} -> OPERATOR
--   * tombstone-operator flag create/update                                -> OPERATOR
--   * evaluator rollback POST /flags/{key}/kill                            -> OWNER
--     (kill_switch is NOT held by OPERATOR — auto-rollback with a service
--      token was already failing authorization before this migration.)
--
-- e.g. UPDATE service_tokens SET role='OPERATOR' WHERE name='gitops-sync';

ALTER TABLE service_tokens
    ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'VIEWER';

-- Constrain to the roles defined in middleware.permissionMatrix so a typo can
-- never silently produce a role with no permissions.
ALTER TABLE service_tokens DROP CONSTRAINT IF EXISTS service_tokens_role_check;
ALTER TABLE service_tokens
    ADD CONSTRAINT service_tokens_role_check
    CHECK (role IN ('VIEWER', 'OPERATOR', 'OWNER', 'ADMIN'));
