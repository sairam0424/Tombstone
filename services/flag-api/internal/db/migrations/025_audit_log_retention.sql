-- Migration 025: partition audit_log for retention (DATA-2).
--
-- audit_log's no_audit_update/no_audit_delete RULEs (schema.sql) make
-- ordinary DML incapable of ever shrinking this table -- by design, since
-- that's exactly what makes it append-only. But that also means nothing
-- could ever prune it either: there was no mechanically-viable way to
-- remove old rows at all. Postgres native partitioning changes that --
-- DETACH PARTITION (used by internal/audit/retention.go, not DROP, so old
-- data is archived rather than destroyed) is DDL, not a DELETE, so the
-- RULEs never see it and don't need to change.
--
-- audit_retention_checkpoints exists because archiving a partition removes
-- the oldest rows of every chain that has any row in it. Without a record
-- of what was removed, internal/audit/verify.go's Verify() would see the
-- new-oldest row of such a chain, find no predecessor, and -- via the SAME
-- leniency that already lets a legacy (pre-AUD-1, unhashed) row restart a
-- chain -- silently accept it as a fresh genesis. That leniency was written
-- for "we have no hash to check," not "we chose not to look here anymore,"
-- and today it cannot tell the two apart: a chain quietly truncated by an
-- attacker with enough privilege to bypass no_audit_delete would verify
-- exactly the same way. A checkpoint closes that gap. It is signed with the
-- SAME AUDIT_HMAC_KEY that keys the chain itself, deliberately -- unlike
-- COMPLIANCE_SIGNING_KEY (SEC-4), which is kept separate from JWT_SECRET
-- because an export verifier and a token minter must never share a key,
-- there is no new party here who should be able to check a checkpoint but
-- not the chain: whoever can forge one could already forge chain entries
-- with the same key, so introducing a second key would add rotation cost
-- without closing any additional attack surface.
CREATE TABLE audit_retention_checkpoints (
    id                        UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id                UUID,
    flag_key                  TEXT NOT NULL,
    pruned_through_hash       TEXT NOT NULL,
    pruned_through_created_at TIMESTAMPTZ NOT NULL,
    signature                 TEXT NOT NULL,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Verify() looks up the checkpoint for a chain by (project_id, flag_key)
-- and the exact hash it needs to explain, so the lookup is a point query,
-- not a scan -- this index makes that so even once checkpoints accumulate
-- over years of retention runs.
CREATE INDEX idx_audit_retention_checkpoints_chain
    ON audit_retention_checkpoints (project_id, flag_key, pruned_through_hash);

-- Append-only for the same reason audit_log is: a checkpoint that could be
-- edited or removed after the fact could hide the very truncation it exists
-- to attest to.
CREATE OR REPLACE RULE no_checkpoint_update AS ON UPDATE TO audit_retention_checkpoints DO INSTEAD NOTHING;
CREATE OR REPLACE RULE no_checkpoint_delete AS ON DELETE TO audit_retention_checkpoints DO INSTEAD NOTHING;

-- Postgres has no ALTER TABLE ... PARTITION BY for an existing table, so the
-- table is rebuilt: renamed aside, recreated partitioned, data copied back,
-- old copy dropped. No live production database has ever been bootstrapped
-- for this repo (Docker Compose / CI only), but this is written to be
-- data-safe regardless -- it copies whatever rows exist rather than
-- assuming there are none.
ALTER TABLE audit_log RENAME TO audit_log_pre_partition;

CREATE TABLE audit_log (
    id              UUID NOT NULL DEFAULT uuid_generate_v4(),
    project_id      UUID REFERENCES projects(id),
    flag_key        TEXT,
    environment     TEXT,
    actor           TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    prev_state      JSONB,
    new_state       JSONB,
    ip_address      TEXT,
    prev_hash       TEXT,
    entry_hash      TEXT,
    rekor_log_id    TEXT,
    rekor_log_index BIGINT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Postgres requires a partitioned table's PRIMARY KEY to include the
    -- partition key. id alone is still effectively unique in practice
    -- (uuid_generate_v4()), but Postgres itself only enforces uniqueness
    -- of (id, created_at) now, not id alone, across partitions.
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Every row must land in SOME partition -- Postgres rejects an INSERT with
-- no matching partition rather than silently accepting it. This catches
-- both the data copied back below and any future row older than the
-- earliest explicitly-created monthly partition (retention.go creates
-- partitions for the current month and ahead, not the past).
CREATE TABLE audit_log_default PARTITION OF audit_log DEFAULT;

INSERT INTO audit_log (id, project_id, flag_key, environment, actor, event_type,
                        prev_state, new_state, ip_address, prev_hash, entry_hash,
                        rekor_log_id, rekor_log_index, created_at)
SELECT id, project_id, flag_key, environment, actor, event_type,
       prev_state, new_state, ip_address, prev_hash, entry_hash,
       rekor_log_id, rekor_log_index, created_at
FROM audit_log_pre_partition;

DROP TABLE audit_log_pre_partition;

CREATE INDEX idx_audit_flag_key_ts ON audit_log (flag_key, created_at DESC);
CREATE INDEX idx_audit_env_ts ON audit_log (environment, created_at DESC);
CREATE INDEX idx_audit_chain_walk ON audit_log (flag_key, created_at ASC, id ASC);
CREATE INDEX idx_audit_log_project_id ON audit_log (project_id);
CREATE INDEX idx_audit_log_project_flag_key_ts ON audit_log (project_id, flag_key, created_at DESC);
CREATE INDEX idx_audit_log_project_id_created_at ON audit_log (project_id, created_at);

-- RULEs are dropped along with the table they were attached to and must be
-- recreated on the new one. They apply only to the audit_log parent name --
-- every write in this codebase goes through that name, never a partition's
-- own name directly, so this is the only place they need to exist.
CREATE OR REPLACE RULE no_audit_update AS ON UPDATE TO audit_log DO INSTEAD NOTHING;
CREATE OR REPLACE RULE no_audit_delete AS ON DELETE TO audit_log DO INSTEAD NOTHING;
