-- Migration 014: store service tokens and break-glass tokens as keyed hashes (SEC-4)
--
-- Both tables previously held the bearer token in PLAINTEXT and authenticated by
-- exact match (`WHERE token = $1`). Any read of the database — backup, replica,
-- leaked dump, over-broad SELECT — therefore yielded working production
-- credentials. (break_glass_tokens was doubly misleading: the create response
-- says "Store this token securely. It cannot be retrieved again," which was
-- false while the plaintext sat in the table.)
--
-- Tokens are now stored as HMAC-SHA256(pepper, token), hex-encoded, where the
-- pepper comes from TOKEN_HASH_PEPPER and never lives in the database. A keyed
-- hash (not a per-row salted KDF) is required because lookup is BY TOKEN with no
-- other identifier — the stored value must be derivable from the presented token
-- to keep the lookup indexed. See internal/secrets/tokenhash.go for the rationale.
--
-- TWO-STEP by design; this file only prepares the schema:
--   1. this migration: add token_hash + unique index, make token nullable
--   2. `go run ./cmd/migrate -hash-tokens`: derive each hash from the existing
--      plaintext and NULL the plaintext out
--
-- Step 2 is Go, not SQL, because the pepper is application config and must not
-- be passed through psql (it would land in shell history and query logs).
--
-- No token rotation is required: because the plaintext is still present at
-- migration time, every existing hash is derivable from it. Rotating the PEPPER
-- later, by contrast, does require re-issuing tokens.

-- Service tokens ------------------------------------------------------------
ALTER TABLE service_tokens ADD COLUMN IF NOT EXISTS token_hash TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_service_tokens_token_hash
    ON service_tokens (token_hash);

-- The plaintext column must become nullable so step 2 can erase it. The old
-- UNIQUE constraint on token is dropped with it (the hash index replaces it).
ALTER TABLE service_tokens ALTER COLUMN token DROP NOT NULL;

-- Break-glass tokens --------------------------------------------------------
ALTER TABLE break_glass_tokens ADD COLUMN IF NOT EXISTS token_hash TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_break_glass_tokens_token_hash
    ON break_glass_tokens (token_hash);

ALTER TABLE break_glass_tokens ALTER COLUMN token DROP NOT NULL;
