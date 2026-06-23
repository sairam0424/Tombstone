---
kind: domain
status: active
goal: Continuously archive flags that have been at 100% rollout for 30+ days and remove their dead code via ast-rewriter
cadence: daily
domain: [flag-cleanup]
---

# Flag Cleanup Loop

Tombstone's intelligence service detects stale flags (100% rollout, no changes for 30+ days).
This loop reads those candidates, generates cleanup PRs via ast-rewriter, and keeps the flag
inventory under the 500-flag project limit.

**Entry point:** `scripts/loop-flag-cleanup.sh`
**Trigger:** daily cron at 02:00 UTC (`.github/workflows/loop-flag-cleanup.yml`)
**MCP tools used:** `tombstone_list_stale_flags`, `tombstone_generate_cleanup_pr`

## Current focus
- Wire daily cron trigger and verify collector writes to metrics/stale.jsonl
- Test first cleanup PR against `max-cart-items` flag (stale since seeding)

## Backlog
- [ ] Wire cron trigger and verify metrics collector runs clean
- [ ] Test first cleanup PR generation against a known stale flag
- [ ] Add slack notification via marketplace when cleanup PR is opened
- [ ] Add inventory-limit early warning (> 80% of 500 limit → signal)

## Evidence & analysis
- `signals/README.md` — signal schema reference
- Tombstone intelligence service: `GET /api/v1/stale?project_id=<uuid>`

## Metrics
Collector writes to `domains/flag-cleanup/metrics/stale.jsonl`:
```jsonl
{"date":"YYYY-MM-DD","stale_count":N,"archived_count":N,"prs_opened":N}
```

## Timeline
<!-- append one line per run: YYYY-MM-DD | what you did and found -->
2026-06-24 | Loop scaffolded and deployed to main. Cron trigger wired (daily 02:00 UTC). Pending GitHub Actions var TOMBSTONE_INTELLIGENCE_URL to activate.
