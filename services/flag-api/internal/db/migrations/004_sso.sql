-- 004_sso.sql
-- SSO session and state-token tables for OIDC/SAML flows.

CREATE TABLE IF NOT EXISTS sso_sessions (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_email TEXT        NOT NULL,
    provider   TEXT        NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sso_sessions_user_email_expires_at
    ON sso_sessions (user_email, expires_at);

CREATE TABLE IF NOT EXISTS sso_state_tokens (
    state      TEXT        PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + INTERVAL '10 minutes')
);
