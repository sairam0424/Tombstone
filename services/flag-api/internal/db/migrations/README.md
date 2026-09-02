# Database Migrations

## Sequence Convention

| Number | File | Description |
|--------|------|-------------|
| 000 | `../schema.sql` | Baseline schema — applied once at DB init |
| 001 | _(skipped)_ | Reserved; schema.sql is migration 001 conceptually |
| 002 | `002_enterprise.sql` | Enterprise multi-tenancy |
| 003 | `003_compliance.sql` | Compliance and audit extensions |
| 004 | `004_sso.sql` | SSO provider support |
| 005 | `005_scheduled_changes_v2.sql` | Scheduled flag change improvements |
| 006 | _(inline in `../schema.sql`)_ | Flag prerequisites (GrowthBook ParentConditions pattern) |
| 007 | _(inline in `../schema.sql`)_ | Multivariate flag variations |
| 008 | _(inline in `../schema.sql`)_ | pgvector embeddings for semantic search |
| 009 | _(inline in `../schema.sql`)_ | Rekor transparency log integration |
| 010 | `010_idempotency_keys.sql` | Idempotency-key support for flag-api mutation endpoints |
| 011 | `011_scheduler_retry_columns.sql` | Scheduler retry/backoff columns (retry_count, max_retries, next_retry_at). Confirmed no collision with 010 above — both were developed concurrently from the same `develop` base and landed cleanly. |
| 012 | `012_idempotency_actor_scope.sql` | Adds actor column to idempotency_keys and re-keys unique index to (actor, idempotency_key, endpoint) — fixes SEC-001 cross-caller cache poisoning. |
| 013 | `013_service_token_roles.sql` | Per-token `role` on `service_tokens` (default `VIEWER`, CHECK-constrained) — fixes SEC-1: every service token previously resolved to OPERATOR, so any SDK token could create/archive flags and change production rollouts. **Breaking:** existing rows backfill to `VIEWER`; machine writers must be re-provisioned (see the migration header for the exact roles). |
| 014 | `014_hashed_tokens.sql` | Adds `token_hash` + unique index to `service_tokens` and `break_glass_tokens` and makes the plaintext `token` nullable — fixes SEC-4: both tables stored bearer tokens in PLAINTEXT, so any DB read yielded working credentials. **Two-step:** apply this migration, then run `go run ./cmd/migrate -hash-tokens` (needs `TOKEN_HASH_PEPPER`) to derive each hash and erase the plaintext. No token rotation required. |
| 015 | `015_audit_entry_hash.sql` | Adds `entry_hash` (each row's own keyed hash) + a chain-walk index to `audit_log` — fixes AUD-1: four writers each built the chain themselves and disagreed (`flags.go`/`scheduler.go` hashed six pipe-joined fields, `scheduled.go` hashed `id + timestamp`), the hash was unkeyed (so anyone who could INSERT could forge a valid-looking chain), and the unlocked SELECT-then-INSERT forked the chain under concurrency. **Deliberately NOT backfilled:** minting keyed hashes for historical rows would fabricate evidence they were verified. Those rows stay NULL and `GET /api/v1/audit/verify` reports them as `legacy_entries_unverifiable`. Verified coverage starts at the first entry written after deploy. |
| 016-022 | _(see git log)_ | project_id scoping for `scheduled_changes`/`audit_log`/`change_requests` (TEN-1a), SEC-3b propose/quorum/apply + require_approval gate, break-glass hardening — this table fell behind; not backfilled retroactively, see the plan memory for the full history. |
| 023 | `023_targeted_indexes.sql` | DATA-2: five indexes for existing hot-path query patterns that had no supporting index — `flag_environments(environment)`, `change_requests(status, created_at)`, `flags(project_id, state)`, `audit_log(project_id, created_at)`, `scim_users(email)`. Purely additive, no application code change. |
| 024 | `024_user_token_watermarks.sql` | SEC-5: adds `user_token_watermarks(user_email PK, valid_after)` — lets `validateJWT` reject a token issued before the subject's most recent forced-logout timestamp, closing the gap where SCIM deprovisioning revoked `user_roles` but left an already-issued JWT valid until natural expiry. |

## Why 001 Is Skipped

`schema.sql` is the baseline — it creates every table, index, and extension from
scratch. It is the logical equivalent of migration 001. Rather than renaming it
(which would break the Makefile, docker-compose, and existing documentation),
the convention is:

- Run `schema.sql` first (migration 000/baseline).
- Incremental migrations begin at **002**.
- 001 is permanently reserved / skipped to make the lineage explicit.

## How to Apply

### Recommended: the migration runner (`cmd/migrate`)

The runner applies the baseline (`schema.sql` = version 1) plus every pending
`migrations/NNN_*.sql` in ascending order, records each in a `schema_migrations`
ledger (so re-runs are no-ops), and holds a Postgres advisory lock so concurrent
flag-api replicas can't double-apply.

```bash
cd services/flag-api
DB_URL="$DATABASE_URL" go run ./cmd/migrate            # apply all pending
DB_URL="$DATABASE_URL" go run ./cmd/migrate -baseline  # adopt on a hand-built DB
```

`-baseline` records every version as applied **without running any SQL** — use
it exactly once when adopting the runner on a database that was already built by
hand (via the manual steps below), so the ledger reflects reality and the runner
never tries to re-apply a non-idempotent statement. flag-api's own startup does
**not** auto-migrate: running migrations stays an explicit, auditable step (CI,
docker-compose init, or ops).

### Manual (reference / fallback)

```bash
# From repo root
psql $DATABASE_URL < services/flag-api/internal/db/schema.sql

# Then apply incremental migrations in order
psql $DATABASE_URL < services/flag-api/internal/db/migrations/002_enterprise.sql
psql $DATABASE_URL < services/flag-api/internal/db/migrations/003_compliance.sql
psql $DATABASE_URL < services/flag-api/internal/db/migrations/004_sso.sql
psql $DATABASE_URL < services/flag-api/internal/db/migrations/005_scheduled_changes_v2.sql
psql $DATABASE_URL < services/flag-api/internal/db/migrations/010_idempotency_keys.sql
psql $DATABASE_URL < services/flag-api/internal/db/migrations/011_scheduler_retry_columns.sql
psql $DATABASE_URL < services/flag-api/internal/db/migrations/012_idempotency_actor_scope.sql
```

Migrations 006-009 are not separate files — they are applied inline as part of
`schema.sql` itself (see the `-- Migration 00N` comments inside that file for
prerequisites, variations, pgvector embeddings, and Rekor integration
respectively). Migrations 010, 011, and 012 are the next standalone files after
005, applied in that order.

The `make dev` target (Docker Compose) runs `schema.sql` automatically via the
`db` service init scripts. Manual migrations must be applied separately unless
wired into a migration runner.

### Adding a New Migration

1. Pick the next number: `NNN = last_number + 1` (e.g. `006`).
2. Create `NNN_<short_description>.sql`.
3. Write idempotent SQL (`CREATE TABLE IF NOT EXISTS`, `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`, etc.).
4. Add a row to the table above in this README.
5. Test against a fresh `schema.sql` baseline before committing.

## Notes

- `schema.sql` is referenced by name in `infra/docker-compose.yml` and the
  root `Makefile`. Do not rename it.
- All migrations are applied with `psql` directly — there is no ORM migration
  runner. Keep SQL compatible with PostgreSQL 16.
- The audit log table (`audit_log`) is append-only. Never add `DELETE` or
  `UPDATE` statements that target it in any migration.
