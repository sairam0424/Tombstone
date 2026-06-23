CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
-- pgvector: required for NLP search (available in pgvector/pgvector:pg16 image)
DO $$ BEGIN
  CREATE EXTENSION IF NOT EXISTS vector;
EXCEPTION WHEN OTHERS THEN
  RAISE NOTICE 'pgvector extension not available — NLP vector search will use fallback';
END $$;

-- Permanently reserved flag keys (Knight Capital prevention)
CREATE TABLE IF NOT EXISTS flag_tombstones (
    key         TEXT PRIMARY KEY,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_by TEXT NOT NULL,
    reason      TEXT
);

-- Top-level project namespace
CREATE TABLE IF NOT EXISTS projects (
    id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name       TEXT NOT NULL,
    slug       TEXT UNIQUE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Flag definitions (environment-agnostic)
CREATE TABLE IF NOT EXISTS flags (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    key         TEXT NOT NULL,
    project_id  UUID NOT NULL REFERENCES projects(id),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    flag_type   TEXT NOT NULL CHECK (flag_type IN ('BOOLEAN','STRING','INTEGER','FLOAT','JSON')),
    state       TEXT NOT NULL DEFAULT 'DRAFT' CHECK (state IN ('DRAFT','ACTIVE','COMPLETE','ARCHIVED')),
    owner_id    TEXT NOT NULL,
    safe_default TEXT NOT NULL DEFAULT 'false',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at TIMESTAMPTZ,
    UNIQUE(project_id, key)
);

-- Enforce tombstone at DB level
CREATE OR REPLACE FUNCTION check_tombstone() RETURNS trigger AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM flag_tombstones WHERE key = NEW.key) THEN
        RAISE EXCEPTION 'Flag key % is tombstoned and cannot be reused', NEW.key;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS enforce_tombstone ON flags;
CREATE TRIGGER enforce_tombstone
    BEFORE INSERT ON flags
    FOR EACH ROW EXECUTE FUNCTION check_tombstone();

-- Live flag state per environment
CREATE TABLE IF NOT EXISTS flag_environments (
    flag_id     UUID NOT NULL REFERENCES flags(id) ON DELETE CASCADE,
    environment TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT false,
    rollout_pct INTEGER NOT NULL DEFAULT 0 CHECK (rollout_pct >= 0 AND rollout_pct <= 100),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  TEXT NOT NULL DEFAULT 'system',
    PRIMARY KEY (flag_id, environment)
);

-- Reusable targeting segments
CREATE TABLE IF NOT EXISTS segments (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    rules       JSONB NOT NULL DEFAULT '[]',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-flag targeting rules
CREATE TABLE IF NOT EXISTS targeting_rules (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    flag_id     UUID NOT NULL REFERENCES flags(id) ON DELETE CASCADE,
    environment TEXT NOT NULL,
    rule_type   TEXT NOT NULL CHECK (rule_type IN ('USER','ORG','SEGMENT','CUSTOM')),
    attribute   TEXT NOT NULL,
    operator    TEXT NOT NULL CHECK (operator IN (
                  'IN','NOT_IN','EQ','NEQ','LT','LTE','GT','GTE','CONTAINS','PREFIX','SUFFIX',
                  'REGEX','SEMVER_GTE','SEMVER_LTE','GEO_COUNTRY','GEO_REGION','DATE_BEFORE','DATE_AFTER'
                )),
    values      JSONB NOT NULL DEFAULT '[]',
    variation   TEXT NOT NULL,
    priority    INTEGER NOT NULL DEFAULT 0
);

-- Migration 007: multivariate flag variations
CREATE TABLE IF NOT EXISTS flag_variations (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  flag_id     UUID NOT NULL REFERENCES flags(id) ON DELETE CASCADE,
  key         TEXT NOT NULL,
  value       TEXT NOT NULL,
  weight      INT NOT NULL CHECK (weight >= 0 AND weight <= 10000),
  description TEXT,
  UNIQUE(flag_id, key)
);
CREATE INDEX IF NOT EXISTS idx_flag_variations_flag_id ON flag_variations(flag_id);

-- Append-only audit log with Merkle chain
CREATE TABLE IF NOT EXISTS audit_log (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    flag_key    TEXT,
    environment TEXT,
    actor       TEXT NOT NULL,
    event_type  TEXT NOT NULL,
    prev_state  JSONB,
    new_state   JSONB,
    ip_address  TEXT,
    prev_hash   TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_flag_key_ts ON audit_log(flag_key, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_env_ts ON audit_log(environment, created_at DESC);

-- Block UPDATE/DELETE on audit_log
CREATE OR REPLACE RULE no_audit_update AS ON UPDATE TO audit_log DO INSTEAD NOTHING;
CREATE OR REPLACE RULE no_audit_delete AS ON DELETE TO audit_log DO INSTEAD NOTHING;

-- Change requests (four-eyes approval)
CREATE TABLE IF NOT EXISTS change_requests (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    flag_key         TEXT NOT NULL,
    environment      TEXT NOT NULL,
    requested_by     TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','APPROVED','REJECTED','APPLIED')),
    change_payload   JSONB NOT NULL,
    approved_by      TEXT[],
    rejected_by      TEXT,
    rejection_reason TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Break-glass emergency tokens
CREATE TABLE IF NOT EXISTS break_glass_tokens (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    token       TEXT UNIQUE NOT NULL,
    scope       TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    used        BOOLEAN NOT NULL DEFAULT false,
    used_at     TIMESTAMPTZ,
    used_by     TEXT,
    incident_ref TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Service tokens for SDK authentication
CREATE TABLE IF NOT EXISTS service_tokens (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    environment TEXT NOT NULL,
    token       TEXT UNIQUE NOT NULL,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ
);

-- Seed default project
INSERT INTO projects (id, name, slug)
VALUES ('00000000-0000-0000-0000-000000000001', 'Default', 'default')
ON CONFLICT DO NOTHING;

-- Migration 006: flag prerequisites (GrowthBook ParentConditions pattern)
-- gate=true  → prerequisite blocks the entire feature evaluation if not met
-- gate=false → prerequisite only skips the current targeting rule if not met
CREATE TABLE IF NOT EXISTS flag_prerequisites (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  flag_id           UUID NOT NULL REFERENCES flags(id) ON DELETE CASCADE,
  prereq_flag_key   TEXT NOT NULL,
  required_variation TEXT NOT NULL DEFAULT 'true',
  gate              BOOLEAN NOT NULL DEFAULT true,
  priority          INT NOT NULL DEFAULT 0,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(flag_id, prereq_flag_key)
);
CREATE INDEX IF NOT EXISTS idx_flag_prerequisites_flag_id ON flag_prerequisites(flag_id);
