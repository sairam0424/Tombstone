-- 010_idempotency_keys.sql
-- Migration 010: idempotency-key support for flag-api mutation endpoints.
--
-- Client-supplied "Idempotency-Key" header lets flag-api collapse a network-retry-induced
-- duplicate call to CreateFlag/UpdateEnvironment/KillSwitch into a single audit_log write
-- and a single downstream Redis broadcast. See internal/middleware/idempotency.go.
CREATE TABLE IF NOT EXISTS idempotency_keys (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    idempotency_key TEXT NOT NULL,
    endpoint        TEXT NOT NULL,
    request_hash    TEXT NOT NULL,
    response_status INT,
    response_body   JSONB,
    locked_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '24 hours')
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_idempotency_key_endpoint ON idempotency_keys (idempotency_key, endpoint);
CREATE INDEX IF NOT EXISTS idx_idempotency_expires_at ON idempotency_keys (expires_at);
