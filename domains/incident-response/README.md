---
kind: domain
status: active
goal: Automatically document every circuit breaker trip as a structured incident post-mortem that compounds into institutional knowledge
cadence: event-driven
domain: [incident-response]
---

# Incident Response Loop

When a flag's circuit breaker trips (error rate > 5% over 10s window), the evaluator
auto-rolls back. This loop captures that event, runs the causal correlator, and writes
a structured post-mortem doc — so each incident teaches the system.

**Entry point:** `scripts/loop-incident-response.sh <flag_key> <environment>`
**Activation:** These loops run as local scripts. Activate by running: `bash scripts/loop-incident-response.sh <flag_key> <environment>`
**APIs used:** evaluator `/api/v1/flags/{key}/slo`, intelligence `/api/v1/correlate`

## Current focus
- Test first post-mortem generation with a manually triggered circuit trip
- Verify correlator links the doc to prior related incidents via [[slug]]

## Backlog
- [ ] Wire marketplace webhook to call this script on `flag.rollback` events
- [ ] Test post-mortem generation against a real or simulated circuit trip
- [ ] Add cross-link logic: auto-append to existing incident doc if same flag tripped within 7 days
- [ ] Generate "incident pattern" signal after 3+ trips on same flag within 30 days

## Evidence & analysis
- Evaluator circuit breaker: `GET /api/v1/flags/{key}/slo?window=7d`
- Incident correlator: `POST /api/v1/correlate` (intelligence service)
- Audit log: `GET /api/v1/audit?flag_key=<key>&limit=20`

## Metrics
Collector writes to `domains/incident-response/metrics/trips.jsonl`:
```jsonl
{"date":"YYYY-MM-DD","flag_key":"<key>","environment":"<env>","error_rate":0.07,"rollback":true,"correlated_flags":[]}
```

## Timeline
<!-- append one line per run: YYYY-MM-DD | flag_key — what happened -->
2026-06-24 | Loop scaffolded and deployed to main. Manual invocation via script ready. Webhook trigger available for event-driven automation.
2026-06-27 | Converted to local-first v1.0.0 self-hosted activation via bash scripts/loop-incident-response.sh <flag_key> <environment>
