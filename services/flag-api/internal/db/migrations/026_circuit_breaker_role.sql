-- EVAL-4: a new role, assignable ONLY to service_tokens -- user_roles' own
-- CHECK constraint (migration 002) is deliberately left untouched, so no
-- human project-membership grant can ever hold it. Exists to scope the new
-- automated rollback-step capability (flags:circuit_breaker) away from the
-- human-held OWNER/ADMIN roles that already hold flags:kill_switch, closing
-- a HIGH-severity finding from PR #220's adversarial review: reusing
-- kill_switch for a graduated (non-zero) rollout change would have let any
-- OWNER/ADMIN bypass require_approval for routine rollout tuning, not just
-- genuine incident response.
ALTER TABLE service_tokens DROP CONSTRAINT IF EXISTS service_tokens_role_check;
ALTER TABLE service_tokens
    ADD CONSTRAINT service_tokens_role_check
    CHECK (role IN ('VIEWER', 'OPERATOR', 'OWNER', 'ADMIN', 'CIRCUIT_BREAKER'));
