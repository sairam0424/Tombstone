-- Migration 019: per-project N-of-M approval quorum (SEC-3b).
--
-- change_requests.approved_by was already a TEXT[], but ApproveChangeRequest
-- flipped status to APPROVED after exactly one entry regardless of how many
-- distinct approvers a project actually wants -- there was no quorum concept
-- to configure at all. Defaults to 1 so every existing project's behavior is
-- unchanged until an admin deliberately raises it.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS required_approvals INTEGER NOT NULL DEFAULT 1
    CHECK (required_approvals >= 1);
