---
kind: domain
status: active
goal: Surface Thompson Sampling / LinUCB rollout recommendations and create signals when experiments are ready to advance — bridging ML decisions to human review
cadence: daily
domain: [rollout-advisor]
---

# Rollout Advisor Loop

Tombstone's intelligence service runs Thompson Sampling and LinUCB bandits to recommend
rollout_pct advances. This loop reads those recommendations, checks blast radius, and
creates actionable signals — giving engineers the final approval gate before GitOps PRs advance.

**Entry point:** `scripts/loop-rollout-advisor.sh`
**Activation:** These loops run as local scripts. Activate by running: `bash scripts/loop-rollout-advisor.sh`
**APIs used:** intelligence `/api/v1/rollout/recommendations`, evaluator `/api/v1/blast-radius`

## Current focus
- Verify recommendations endpoint returns data after seeding experiment telemetry
- Test first rollout-ready signal generation

## Backlog
- [ ] Wire daily cron and verify metrics collector runs clean
- [ ] Test signal generation against a seeded experiment with ≥50 observations
- [ ] Add GitOps PR generation for LOW/MEDIUM blast radius advances (ship-change workflow)
- [ ] Add experiment collision detection: warn if two advancing experiments share > 70% population

## Evidence & analysis
- Intelligence service: `GET /api/v1/rollout/recommendations`
- Evaluator blast radius: `GET /api/v1/blast-radius?flag_key=<key>&environment=<env>&rollout_pct=<pct>`

## Metrics
Collector writes to `domains/rollout-advisor/metrics/recommendations.jsonl`:
```jsonl
{"date":"YYYY-MM-DD","flag_key":"<key>","environment":"<env>","current_pct":25,"suggested_pct":50,"confidence":0.92,"blast_risk":"LOW","signal_created":true}
```

## Timeline
<!-- append one line per run: YYYY-MM-DD | N recommendations reviewed, M signals created -->
2026-06-24 | Loop scaffolded and deployed to main. Local script ready (weekday 08:00 UTC schedule recommended). Signals-only design confirmed.
2026-06-27 | Converted to local-first v1.0.0 self-hosted activation via bash scripts/loop-rollout-advisor.sh
