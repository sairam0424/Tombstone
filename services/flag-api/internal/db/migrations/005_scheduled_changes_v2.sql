-- Migration 005: Extend scheduled_changes for background executor
-- Adds error_message column and FAILED status for executor error tracking.

-- Add error_message column (nullable — only set on FAILED)
ALTER TABLE scheduled_changes
    ADD COLUMN IF NOT EXISTS error_message TEXT;

-- Expand status CHECK constraint to include FAILED
-- PostgreSQL requires dropping and re-adding the constraint.
ALTER TABLE scheduled_changes DROP CONSTRAINT IF EXISTS scheduled_changes_status_check;
ALTER TABLE scheduled_changes
    ADD CONSTRAINT scheduled_changes_status_check
    CHECK (status IN ('PENDING', 'EXECUTED', 'CANCELLED', 'FAILED'));
