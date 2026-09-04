-- Migration 015: self-hash column for audit_log (AUD-1)
--
-- audit_log only stored prev_hash — a pointer at the PREVIOUS row's contents.
-- With no record of a row's OWN hash, verification had to recompute every
-- predecessor from scratch, and there was no way to detect that a row itself had
-- been altered. Worse, prev_hash was produced by two incompatible formulas
-- (flags.go/scheduler.go used six pipe-joined fields; scheduled.go used
-- `id + timestamp` with no separator), so a given value could not even be
-- attributed to a formula.
--
-- entry_hash stores each row's own keyed hash:
--     HMAC-SHA256(AUDIT_HMAC_KEY, canonical(row fields || prev_hash))
-- Because it commits to prev_hash, each entry commits to its whole history.
--
-- NOT backfilled, deliberately. A keyed hash for a historical row could only be
-- minted by this service, which would fabricate evidence that those rows were
-- verified when they never were. Rows stay NULL and GET /api/v1/audit/verify
-- reports them as "legacy_entries_unverifiable" instead of claiming integrity.
-- Verified coverage therefore begins at the first entry written after this
-- migration is deployed.

ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS entry_hash TEXT;

-- Verification walks chains in (flag_key, created_at, id) order; this index
-- keeps that walk from becoming a full sort on a large audit_log.
CREATE INDEX IF NOT EXISTS idx_audit_chain_walk
    ON audit_log (flag_key, created_at ASC, id ASC);
