-- Migration 012: scope idempotency_keys to the requesting actor (SEC-001).
--
-- The original unique index was (idempotency_key, endpoint) with no actor
-- column, which allowed two different authenticated callers sharing the same
-- key string to the same endpoint to share one cached response — including
-- kill-switch confirmations. Adding an actor column and re-keying the unique
-- index to (actor, idempotency_key, endpoint) means each caller's key is
-- isolated to their own cache namespace.

ALTER TABLE idempotency_keys ADD COLUMN IF NOT EXISTS actor TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_idempotency_key_endpoint;

CREATE UNIQUE INDEX IF NOT EXISTS idx_idempotency_actor_key_endpoint
    ON idempotency_keys (actor, idempotency_key, endpoint);
