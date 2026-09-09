#!/usr/bin/env bash
set -euo pipefail

# Load the same env the running stack was actually started with (docker
# compose reads infra/.env directly; this script has always run outside
# that container, so it never saw those values before).
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ -f "$REPO_ROOT/infra/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  source "$REPO_ROOT/infra/.env"
  set +a
fi

# SEC-4 (migration 014_hashed_tokens.sql): flag-api's auth middleware looks
# up an incoming bearer credential by HMAC-SHA256(pepper, credential),
# hex-encoded, against service_tokens.token_hash -- NOT by plain equality
# against the old, now-nullable `token` column. This script used to insert
# only the plaintext, so the seeded dev credential could never actually
# authenticate against any flag-api built after SEC-4 shipped -- confirmed
# by actually running the full local stack end to end and hitting
# "invalid or expired token" on every request. Computed here with the
# exact same construction as internal/secrets/tokenhash.go's
# TokenHasher.Hash, using whichever pepper the running flag-api actually
# loaded above (falls back to the .env.example placeholder only if
# infra/.env is missing entirely).
BEARER_VALUE=sdk-dev-token-change-in-prod
PEPPER_FALLBACK=change-me-at-least-32-chars-long-distinct-from-jwt-secret
SEED_BEARER="${SEED_DEV_TOKEN:-$BEARER_VALUE}"
SEED_BEARER_HASH=$(printf '%s' "$SEED_BEARER" | openssl dgst -sha256 -hmac "${TOKEN_HASH_PEPPER:-$PEPPER_FALLBACK}" -r | awk '{print $1}')

# docker compose exec, not a host-side `-h localhost -p 5433` TCP connection
# (this script's own previous approach, matching the port docker-compose.yml
# forwards postgres to): that port choice was ALREADY working around one
# local-postgres collision (its own comment says so) and, on at least one
# real dev machine, a second unrelated native postgres process was also
# bound to the exact same host port -- found by actually running this
# script and hitting "role tombstone does not exist" despite the role
# genuinely existing (confirmed via `docker exec ... psql`, which bypasses
# the host network entirely). Matches `make migrate`'s own already-correct
# convention exactly, and needs no password: the postgres image's default
# pg_hba.conf trusts local (same-container) connections.
docker compose -f "$REPO_ROOT/infra/docker-compose.yml" exec -T postgres \
  psql -U tombstone -d tombstone -v bearer="$SEED_BEARER" -v bearer_hash="$SEED_BEARER_HASH" <<'SQL'
-- Seed sample flags for local development (idempotent)
INSERT INTO flags (id, key, project_id, name, description, flag_type, state, owner_id, safe_default)
VALUES
  (
    'aaaaaaaa-0000-0000-0000-000000000001',
    'checkout-v2',
    '00000000-0000-0000-0000-000000000001',
    'Checkout V2',
    'New checkout flow with optimized payment UX',
    'BOOLEAN',
    'ACTIVE',
    'dev@example.com',
    'false'
  ),
  (
    'aaaaaaaa-0000-0000-0000-000000000002',
    'payment-gateway-fee-display',
    '00000000-0000-0000-0000-000000000001',
    'Payment Gateway Fee Display',
    'Controls which fee display variant is shown in checkout',
    'STRING',
    'ACTIVE',
    'dev@example.com',
    'hidden'
  ),
  (
    'aaaaaaaa-0000-0000-0000-000000000003',
    'max-cart-items',
    '00000000-0000-0000-0000-000000000001',
    'Max Cart Items',
    'Maximum number of items allowed in a single cart',
    'INTEGER',
    'ACTIVE',
    'dev@example.com',
    '50'
  )
ON CONFLICT (project_id, key) DO NOTHING;

-- Seed flag environment states
INSERT INTO flag_environments (flag_id, environment, enabled, rollout_pct, updated_by)
VALUES
  ('aaaaaaaa-0000-0000-0000-000000000001', 'development', true,  100, 'seed'),
  ('aaaaaaaa-0000-0000-0000-000000000001', 'staging',     true,   50, 'seed'),
  ('aaaaaaaa-0000-0000-0000-000000000001', 'production',  false,   0, 'seed'),
  ('aaaaaaaa-0000-0000-0000-000000000002', 'development', true,  100, 'seed'),
  ('aaaaaaaa-0000-0000-0000-000000000002', 'staging',     true,  100, 'seed'),
  ('aaaaaaaa-0000-0000-0000-000000000002', 'production',  false,   0, 'seed'),
  ('aaaaaaaa-0000-0000-0000-000000000003', 'development', true,  100, 'seed'),
  ('aaaaaaaa-0000-0000-0000-000000000003', 'staging',     true,  100, 'seed'),
  ('aaaaaaaa-0000-0000-0000-000000000003', 'production',  true,  100, 'seed')
ON CONFLICT (flag_id, environment) DO NOTHING;

-- Seed a dev service token. token_hash is what flag-api's auth middleware
-- actually looks up by (SEC-4, migration 014) -- the plaintext `token`
-- column is kept only as a human-readable reference for local dev, it is
-- never read by any authentication path. role defaults to 'VIEWER' when
-- omitted (schema.sql) -- a real, second bug in this same script found by
-- actually exercising it end to end: even once authentication itself
-- worked, this token could never create/toggle/kill a flag, only read.
-- ADMIN here matches this token's actual purpose (the one credential every
-- local dev tool -- dashboard, CLI, docs examples -- is told to use).
INSERT INTO service_tokens (id, project_id, environment, token, token_hash, role, name)
VALUES
  (
    'bbbbbbbb-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    'development',
    :'bearer',
    :'bearer_hash',
    'ADMIN',
    'Dev SDK Token'
  )
ON CONFLICT (id) DO UPDATE SET token = EXCLUDED.token, token_hash = EXCLUDED.token_hash, role = EXCLUDED.role;

SELECT 'Seeded ' || COUNT(*) || ' flags' AS result FROM flags;
SQL

echo "Seed complete."
