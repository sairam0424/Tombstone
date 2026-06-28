#!/usr/bin/env bash
set -euo pipefail

# Read password from infra/.env if it exists, otherwise use default
PGPASS="${POSTGRES_PASSWORD:-tombstone-local-dev}"
PGPASSWORD="$PGPASS" psql -h localhost -p 5433 -U tombstone -d tombstone <<'SQL'
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

-- Seed a dev service token
INSERT INTO service_tokens (id, project_id, environment, token, name)
VALUES
  (
    'bbbbbbbb-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    'development',
    'sdk-dev-token-change-in-prod',
    'Dev SDK Token'
  )
ON CONFLICT DO NOTHING;

SELECT 'Seeded ' || COUNT(*) || ' flags' AS result FROM flags;
SQL

echo "Seed complete."
