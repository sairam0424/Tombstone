---
kind: domain
domain: audit-retention
status: active
goal: Keep audit_log's live/hot table bounded by archiving partitions older than AUDIT_LOG_RETENTION_DAYS, without ever weakening the Merkle chain's tamper-detection guarantee
cadence: daily
---

# audit-retention — bounded, verifiable audit-log retention

Daily job that ensures upcoming monthly `audit_log` partitions exist, then archives
(detach + rename, never DROP) any partition wholly older than the server-configured
retention window (DATA-2). Unlike `flag-cleanup`/`governance`, this loop's collector
step **mutates production storage** — it is not read-only — so it calls flag-api's
own ADMIN-gated endpoint rather than touching Postgres directly, and every archive
run seals a signed checkpoint first so `GET /api/v1/audit/verify` can still tell a
legitimately archived chain start apart from a tampered one.

**Entry point:** `scripts/loop-audit-retention.sh`
**Activation:** `bash scripts/loop-audit-retention.sh`
**API used:** flag-api `POST /api/v1/audit/retention/run` (`admin:admin`)

## Why archive, not delete

`audit_log`'s `no_audit_delete`/`no_audit_update` RULEs make ordinary DML incapable
of shrinking the table — that is what makes it append-only. Native Postgres
partitioning (migration 025) makes `DETACH PARTITION` + rename the only mechanism
that can remove rows from the *live* table without touching those RULEs at all: DDL,
not DML, so the RULEs never see it. The detached table is renamed to
`<partition>_archived`, kept in the same database, and has the same append-only
RULEs reapplied — this is a deliberately minimal cold tier that needs no new
infrastructure. A true cold tier (export to object storage, drop the local archive
after a much longer legal-hold window) is a natural next step, left for when a real
compliance requirement drives it — there is no live production deployment yet to
justify building it speculatively.

## Why a checkpoint

Archiving a partition removes the oldest rows of every chain that has any row in
it. Without a record of what was removed, `Verify()` would see the new-oldest row
of such a chain, find no predecessor, and treat it as a fresh genesis — exactly the
leniency that already lets a legacy (pre-AUD-1) row restart a chain. That leniency
cannot otherwise tell "we archived this" apart from "an attacker with enough
privilege to bypass `no_audit_delete` truncated this chain." Every archive run
seals a signed `audit_retention_checkpoints` row per continuing chain before
detaching, so `Verify()` can confirm the gap is legitimate — and still flags a gap
with no matching checkpoint as tampering.

## Environment variables

| Variable | Required | Default | Purpose |
|----------|----------|---------|---------|
| `TOMBSTONE_API_URL` | no | `http://localhost:8081` | flag-api base URL |
| `AUDIT_RETENTION_ADMIN_TOKEN` | yes | — | Bearer token for a caller holding `admin:admin`. The run is skipped (not silently treated as success) if unset. |

The retention *duration* itself is `AUDIT_LOG_RETENTION_DAYS`, read server-side by
flag-api at startup (default 365 days) — deliberately not a parameter this script
can pass or override, so a compromised or buggy caller can trigger a run but can
never widen how aggressively it archives.

## Current focus
- Wire daily cron trigger and verify first metrics write against a real deployment

## Backlog
- [ ] Wire daily cron and verify metrics collector runs clean
- [ ] Alert (Slack, matching governance's pattern) when a run fails outright
- [ ] True cold tier: export `_archived` tables to object storage once a real
      compliance/legal-hold window is defined

## Metrics
Collector writes to `domains/audit-retention/metrics/retention.jsonl`:
```jsonl
{"date":"YYYY-MM-DD","partitions_archived":["audit_log_2025_01"],"checkpoints_written":3,"status":"ok"}
```

## Timeline
<!-- append one line per run: YYYY-MM-DD | partitions_archived=[...] checkpoints=N -->
2026-09-03 | Loop scaffolded alongside DATA-2 (audit_log partitioning + retention checkpoints).
