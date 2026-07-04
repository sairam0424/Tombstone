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
| 011 | `011_scheduler_retry_columns.sql` | Scheduler retry/backoff columns (retry_count, max_retries, next_retry_at). Numbered 011 rather than the next sequential 006 because a concurrent sibling phase (idempotency-keys table), developed independently from the same `develop` base, claims 010 — see file header comment for full reasoning. Numbers 006-010 are reserved for that and other in-flight concurrent phases; confirm no gap collision before merging. |

## Why 001 Is Skipped

`schema.sql` is the baseline — it creates every table, index, and extension from
scratch. It is the logical equivalent of migration 001. Rather than renaming it
(which would break the Makefile, docker-compose, and existing documentation),
the convention is:

- Run `schema.sql` first (migration 000/baseline).
- Incremental migrations begin at **002**.
- 001 is permanently reserved / skipped to make the lineage explicit.

## How to Apply

### Fresh database (dev / CI)

```bash
# From repo root
psql $DATABASE_URL < services/flag-api/internal/db/schema.sql

# Then apply incremental migrations in order
psql $DATABASE_URL < services/flag-api/internal/db/migrations/002_enterprise.sql
psql $DATABASE_URL < services/flag-api/internal/db/migrations/003_compliance.sql
psql $DATABASE_URL < services/flag-api/internal/db/migrations/004_sso.sql
psql $DATABASE_URL < services/flag-api/internal/db/migrations/005_scheduled_changes_v2.sql
psql $DATABASE_URL < services/flag-api/internal/db/migrations/011_scheduler_retry_columns.sql
```

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
