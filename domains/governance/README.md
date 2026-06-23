---
kind: domain
status: active
goal: Track weekly flag governance health — health score, stale count, RBAC coverage, break-glass events — and create signals when thresholds are breached
cadence: weekly
domain: [governance]
---

# Governance Loop

Weekly collector that reads compliance evidence from flag-api and stale flag
metrics from intelligence, writes to metrics/health.jsonl, and creates a
signal when health_score < 0.8 or stale count > 50.

**Entry point:** `scripts/loop-governance.sh`
**Trigger:** weekly Monday 06:00 UTC (`.github/workflows/loop-governance.yml`)
**APIs used:** flag-api `/api/v1/compliance/evidence`, intelligence `/api/v1/stale`

## Current focus
- Wire weekly cron trigger and verify first metrics write

## Backlog
- [ ] Wire weekly cron and verify metrics collector runs clean
- [ ] Add health trend chart to dashboard (7-week rolling)
- [ ] Create governance-YYYY-WW.md doc when health < 0.8 for 2+ consecutive weeks
- [ ] SOC2 evidence export integration

## Metrics
Collector writes to `domains/governance/metrics/health.jsonl`:
```jsonl
{"date":"YYYY-MM-DD","health_score":0.92,"stale_count":12,"rbac_coverage":1.0,"break_glass_uses":0,"active_flags":47}
```

## Timeline
<!-- append one line per run: YYYY-MM-DD | health_score=X stale=Y -->
2026-06-24 | Loop scaffolded and deployed to main. Weekly Monday cron wired (06:00 UTC). Pending GitHub Actions var TOMBSTONE_API_URL to activate.
