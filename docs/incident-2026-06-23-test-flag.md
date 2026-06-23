---
kind: doc
domain: [incident-response]
status: draft
type: learning
links: []
---

# Incident: test-flag circuit trip — 2026-06-23

Flag **test-flag** tripped its circuit breaker in **production** on 2026-06-23.
The evaluator automatically rolled back the flag. This document captures what happened.

## What happened
- **Flag key:** test-flag
- **Environment:** production
- **Triggered at:** 2026-06-23T18:03:28Z
- **Error rate at trip:** 0
- **Circuit trips (7d):** 0
- **Correlated flags (30m window):** none

## SLO data
{
    "error_rate": 0,
    "circuit_trips": 0,
    "slo_budget_remaining": 1
}

## Causal correlation (top candidates)
{
    "candidates": []
}

## Action items
- [ ] Review the correlated flags listed above for recent changes
- [ ] Check audit log: `GET /api/v1/audit?flag_key=test-flag&limit=20`
- [ ] Determine if rollout_pct should be reduced or flag retired
- [ ] Update this doc with root cause once identified

## Timeline
2026-06-23 | Circuit breaker trip — auto-rollback by evaluator. Error rate: 0. Correlated: none.
