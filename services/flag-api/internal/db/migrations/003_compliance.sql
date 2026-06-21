-- Migration 003: SOC 2 compliance schema additions
-- Adds MFA event logging and weekly compliance snapshots.

-- MFA event log for SOC 2 CC6 evidence
CREATE TABLE IF NOT EXISTS user_mfa_log (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     TEXT NOT NULL,
    event_type  TEXT NOT NULL
                CHECK (event_type IN ('mfa_verified','mfa_required','mfa_bypassed')),
    flag_key    TEXT,
    environment TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_user_mfa_log_user_created
    ON user_mfa_log(user_id, created_at DESC);

-- Weekly compliance snapshot for trend reporting
CREATE TABLE IF NOT EXISTS compliance_snapshots (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    snapshot_week   DATE NOT NULL UNIQUE,
    total_flags     INT NOT NULL DEFAULT 0,
    active_flags    INT NOT NULL DEFAULT 0,
    stale_flags     INT NOT NULL DEFAULT 0,
    rbac_coverage   REAL NOT NULL DEFAULT 0.0,
    approval_rate   REAL NOT NULL DEFAULT 0.0,
    break_glass_uses INT NOT NULL DEFAULT 0,
    health_score    REAL NOT NULL DEFAULT 1.0,
    generated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed current-week placeholder row
INSERT INTO compliance_snapshots (snapshot_week)
VALUES (date_trunc('week', now())::date)
ON CONFLICT (snapshot_week) DO NOTHING;
