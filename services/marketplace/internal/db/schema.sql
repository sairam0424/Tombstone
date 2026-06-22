-- marketplace_integrations persists installed third-party integration records.
-- Created by H5 fix: add optional PostgreSQL persistence to the marketplace registry.
CREATE TABLE IF NOT EXISTS marketplace_integrations (
    id           TEXT        PRIMARY KEY,
    webhook_url  TEXT        NOT NULL,
    config       JSONB       NOT NULL DEFAULT '{}',
    installed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
