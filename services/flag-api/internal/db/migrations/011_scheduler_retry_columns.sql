-- Migration 011: retry support for the scheduled-change executor.
--
-- Today, scheduler.markFailed sets status='FAILED' permanently on ANY error —
-- including purely transient ones (a momentary DB blip, a connection hiccup).
-- The poll query only ever selects WHERE status = 'PENDING', so a FAILED row
-- is never retried; a human must manually recreate it via the API.
--
-- This migration adds the columns needed for bounded exponential-backoff
-- retry: retry_count/max_retries gate how many attempts a row gets, and
-- next_retry_at tells the poll query when a FAILED row becomes eligible
-- again. See internal/scheduler/scheduler.go for the retry/backoff logic.
--
-- NOTE ON MIGRATION NUMBERING: this repo is mid-flight on several concurrent
-- resilience-initiative phases, each cut from `develop` in its own worktree.
-- A sibling phase (idempotency-keys table) is independently claiming 010 at
-- the same time. Since neither phase can see the other's in-progress branch,
-- 011 was chosen here specifically to leave 010 free and avoid a numbering
-- collision when both PRs land. If 010 turns out to already be taken by
-- something else by the time this merges, a human reviewer should re-check
-- for collisions before merging both PRs.
ALTER TABLE scheduled_changes ADD COLUMN IF NOT EXISTS retry_count INT NOT NULL DEFAULT 0;
ALTER TABLE scheduled_changes ADD COLUMN IF NOT EXISTS max_retries INT NOT NULL DEFAULT 3;
ALTER TABLE scheduled_changes ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ;
