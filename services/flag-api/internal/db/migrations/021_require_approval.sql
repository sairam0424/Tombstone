-- Migration 021: per-project require_approval enforcement gate (SEC-3b, part 2).
--
-- SEC-3b's propose/quorum/apply-on-approval mechanism (migrations 019/020)
-- made change_requests actually functional, but nothing forced anyone to
-- use it: a project could set required_approvals to any N and it did
-- nothing, since UpdateEnvironment always wrote flag_environments directly
-- regardless. This is the gate itself. Defaults to false (opt-in, per the
-- locked v2.0.0 decision) so every existing project's direct-write
-- behavior is unchanged until an admin deliberately enables it.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS require_approval BOOLEAN NOT NULL DEFAULT false;
