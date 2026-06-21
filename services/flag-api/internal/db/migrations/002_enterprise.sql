-- Migration 002: Enterprise schema additions
-- Adds RBAC, SCIM, scheduled changes, inventory limits, and break-glass tokens.

-- User roles table for RBAC
CREATE TABLE IF NOT EXISTS user_roles (
    user_id    TEXT NOT NULL,
    project_id UUID NOT NULL REFERENCES projects(id),
    role       TEXT NOT NULL DEFAULT 'VIEWER'
               CHECK (role IN ('VIEWER','OPERATOR','OWNER','ADMIN')),
    granted_by TEXT NOT NULL,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, project_id)
);

-- Custom role definitions (Phase 3+ extension)
CREATE TABLE IF NOT EXISTS custom_roles (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    name        TEXT NOT NULL,
    permissions JSONB NOT NULL DEFAULT '[]',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(project_id, name)
);

-- SCIM provisioned users (for IdP sync + orphan detection)
CREATE TABLE IF NOT EXISTS scim_users (
    external_id  TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL UNIQUE,
    email        TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    active       BOOLEAN NOT NULL DEFAULT true,
    synced_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Break-glass emergency tokens
CREATE TABLE IF NOT EXISTS break_glass_tokens (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    token        TEXT NOT NULL UNIQUE,
    scope        TEXT NOT NULL,
    created_by   TEXT NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    used         BOOLEAN NOT NULL DEFAULT false,
    used_by      TEXT,
    used_at      TIMESTAMPTZ,
    incident_ref TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_break_glass_token ON break_glass_tokens(token)
    WHERE used = false;

-- Scheduled flag changes (flip at a specific time)
CREATE TABLE IF NOT EXISTS scheduled_changes (
    id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    flag_key       TEXT NOT NULL,
    environment    TEXT NOT NULL,
    scheduled_for  TIMESTAMPTZ NOT NULL,
    change_payload JSONB NOT NULL,
    created_by     TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'PENDING'
                   CHECK (status IN ('PENDING','EXECUTED','CANCELLED')),
    executed_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_scheduled_pending ON scheduled_changes(scheduled_for)
    WHERE status = 'PENDING';

-- Flag inventory limits per project
CREATE TABLE IF NOT EXISTS inventory_limits (
    project_id    UUID PRIMARY KEY REFERENCES projects(id),
    max_flags     INTEGER NOT NULL DEFAULT 500,
    current_count INTEGER NOT NULL DEFAULT 0
);

-- Seed default inventory limit
INSERT INTO inventory_limits (project_id, max_flags)
VALUES ('00000000-0000-0000-0000-000000000001', 500)
ON CONFLICT DO NOTHING;

-- Seed default admin user
INSERT INTO user_roles (user_id, project_id, role, granted_by)
VALUES ('admin@example.com', '00000000-0000-0000-0000-000000000001', 'ADMIN', 'system')
ON CONFLICT DO NOTHING;
