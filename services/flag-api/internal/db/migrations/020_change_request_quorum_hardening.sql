-- Migration 020: pin quorum policy + prevent duplicate concurrent proposals
-- (SEC-3b adversarial review).
--
-- required_approvals was read fresh from projects on every single approval
-- call, so lowering a project's quorum mid-flight silently downgraded an
-- in-flight proposal's bar with no record anyone did so. Snapshotting it
-- onto the row at propose time (default 1, matching the pre-existing
-- single-approval behavior for rows scim.go/orphan_detector.go insert
-- directly, which never set this column) means every approval call reads
-- the policy that was actually in force when the proposal was created, not
-- whatever is current now.
ALTER TABLE change_requests ADD COLUMN IF NOT EXISTS required_approvals INTEGER NOT NULL DEFAULT 1
    CHECK (required_approvals >= 1);

-- Two independently-quorum-approved change requests targeting the same
-- flag+environment could previously both apply, with whichever transaction
-- committed last silently overwriting the other's fully-approved result.
-- Scoped to ONLY applicable (flag-environment-shaped) payloads via the
-- jsonb `?` (key-exists) operator, so scim.go/orphan_detector.go's
-- informational rows (which have neither key) are completely unaffected.
CREATE UNIQUE INDEX IF NOT EXISTS idx_change_requests_one_pending_proposal
    ON change_requests (project_id, flag_key, environment)
    WHERE status = 'PENDING' AND change_payload ? 'enabled' AND change_payload ? 'rollout_pct';
