-- Migration 028: flags.embedding vector(768) -> vector(1024), ivfflat -> HNSW index
--
-- Why the column changes: migration 008 (inline in schema.sql) declared it as
-- vector(768), but both embedding backends in services/intelligence return
-- 1024-dimensional vectors (local BAAI/bge-m3, and Bedrock Titan Text Embeddings
-- V2 requested with dimensions=1024). pgvector rejected every write ("expected
-- 768 dimensions, not 1024"), so no flag ever received an embedding, the dense
-- arm of the 3-way RRF search always returned nothing, and "semantic" search was
-- lexical-only in every deployment built from that schema.
--
-- Why the index changes: the baseline built idx_flags_embedding as ivfflat
-- (lists = 100) on the empty column. ivfflat picks its list centroids from the
-- rows present when it is built, so an index built empty has random lists, and
-- pgvector's default of probing one of 100 lists then misses most true
-- neighbours. On 6000 synthetic 1024-dimensional flags, recall@10 was 0.44-0.53
-- (pgvector 0.8.0 and 0.6.2) and only 0.67-0.72 after REINDEX on the filled
-- table; lists = 100 is also far more than a table of a few thousand rows needs.
-- HNSW needs no training data, so it is correct when built on an empty column and
-- stays correct as the backfill inserts (same data: recall@10 0.98-0.99), and
-- there is no REINDEX step to forget. It needs pgvector 0.5.0 or newer (the
-- pgvector/pgvector:pg16 image is newer). The default hnsw.ef_search of 40 caps
-- one dense query at 40 neighbours; the search endpoint asks for 2 x limit (20 by
-- default).
--
-- What it does, each step only where the database is not already there:
--   1. Drops idx_flags_embedding unless it is already an HNSW index. An ivfflat
--      index is built for one dimension; dropping it first makes the rebuild
--      explicit instead of relying on ALTER TYPE's implicit one.
--   2. Adds flags.embedding as vector(1024) if the column is missing (a database
--      built before migration 008 and adopted with `cmd/migrate -baseline`).
--      If the column has another width, NULLs every embedding whose dimension is
--      not 1024 and retypes the column. A 768-dimensional vector cannot be
--      compared with a 1024-dimensional query vector, and the intelligence
--      service re-embeds every flag with embedding IS NULL at startup
--      (EmbeddingSyncService._backfill). In practice the column is already all
--      NULL, because no write ever succeeded.
--   3. Creates the HNSW idx_flags_embedding if step 1 dropped it or it was
--      missing.
--
-- Idempotent: on a database already in the target state (fresh install from the
-- current schema.sql, an earlier run, a manual fix) it only reads the catalogs:
-- it changes nothing and takes no lock on flags. The runner records this version
-- once, but `make migrate` re-applies every file, so a re-run must never discard
-- valid 1024-dimensional embeddings or rebuild the index.
--
-- Locking: DROP INDEX takes ACCESS EXCLUSIVE on flags and the lock is held until
-- the transaction commits -- through the ALTER COLUMN TYPE table rewrite and the
-- index build. Every read and write of flags waits meanwhile. flags has one row
-- per feature flag (thousands at most) and the values being rewritten are NULL,
-- so the hold is short; still, apply it in a quiet moment rather than during a
-- deploy storm. Everything runs inside the one DO block, so a single
-- lock_timeout bounds all of it however the file is run: a CREATE INDEX after
-- the block would, under psql's autocommit, run outside the block's transaction
-- and outside its lock_timeout. The block waits at most 5s for the lock, then
-- fails and changes nothing. Readers of flags queue behind a pending ACCESS
-- EXCLUSIVE request, so a blocked attempt stalls them for up to those 5s before
-- it gives up; on "canceling statement due to lock timeout", find the holder in
-- pg_stat_activity and re-run.
--
-- Running it by hand: `psql -v ON_ERROR_STOP=1 -f`. Without ON_ERROR_STOP psql
-- exits 0 after a lock timeout, and so does `make migrate`; check the outcome
-- with:
--   SELECT format_type(atttypid, atttypmod) FROM pg_attribute
--    WHERE attrelid = 'flags'::regclass AND attname = 'embedding';
-- `cmd/migrate -baseline` records this version WITHOUT running it, so a database
-- adopted that way keeps its old column and index until this file is applied by
-- hand.
--
-- After it: restart the intelligence service. EmbeddingSyncService backfills
-- every NULL embedding in the background, but only at startup, so a service
-- that was already running does not pick the rows up on its own.
DO $$
DECLARE
  column_type  text;
  index_method text;
BEGIN
  PERFORM set_config('lock_timeout', '5s', true);

  SELECT format_type(a.atttypid, a.atttypmod) INTO column_type
    FROM pg_attribute a
   WHERE a.attrelid = 'flags'::regclass
     AND a.attname = 'embedding'
     AND NOT a.attisdropped;

  SELECT am.amname INTO index_method
    FROM pg_class c
    JOIN pg_am am ON am.oid = c.relam
   WHERE c.oid = to_regclass('idx_flags_embedding');

  IF index_method IS NOT NULL AND index_method <> 'hnsw' THEN
    DROP INDEX idx_flags_embedding;
    index_method := NULL;
  END IF;

  IF column_type IS NULL THEN
    ALTER TABLE flags ADD COLUMN embedding vector(1024);
  ELSIF column_type <> 'vector(1024)' THEN
    UPDATE flags SET embedding = NULL
     WHERE embedding IS NOT NULL AND vector_dims(embedding) <> 1024;
    ALTER TABLE flags ALTER COLUMN embedding TYPE vector(1024);
  END IF;

  IF index_method IS NULL THEN
    CREATE INDEX idx_flags_embedding ON flags USING hnsw (embedding vector_cosine_ops);
  END IF;
END $$;
