# Outreach — v1.2.1 Follow-ups

Covers: newsletter follow-ups, StackShare listing, devhunt.org submission, Slack messages.

---

## Newsletter Follow-ups

### DevOps Weekly — v1.2.1 follow-up
**To:** gareth@morethanseven.net
**Subject:** Re: Tombstone — v1.2.1 production hardening release

Hi Gareth,

Following up on the Tombstone submission from 2026-06-28. Wanted to flag (no pun intended) the v1.2.1 release that shipped this week — it's the production hardening release that closes a lot of the "interesting demo" vs "I'd run this in production" gap.

The highlights: 10 resilience phases (distributed rate limiting via Redis Lua, Redis Streams DLQ with manual replay, adaptive load shedding, snapshot reconciliation loop), plus a full documentation suite (evaluation model, K8s deployment guide, 5 operational runbooks). We also ran a 26-agent adversarial pre-release validation with 28 sub-checks — all PASS.

The one-line summary: self-hosted feature flag platform that auto-rolls back bad flags via circuit breaker, with causal incident correlation for post-incident "was it a flag?" analysis.

GitHub: https://github.com/sairum0424/tombstone
Release notes: docs/RELEASE_NOTES_v1.2.1.md

Happy to answer any questions. Thanks for considering it.

---

### Go Weekly — v1.2.1 follow-up
**To:** peter@cooperpress.com
**Subject:** Re: Tombstone — v1.2.1 Go resilience patterns

Hi Peter,

Following up on the Tombstone submission from 2026-06-28. The v1.2.1 production hardening release shipped this week and has some Go-specific patterns that might be interesting for Go Weekly readers:

**New Go patterns in v1.2.1:**
- Distributed rate limiting via Redis Lua atomic script (replaces `sync.Map` token buckets — ensures global rate limit across replicas, not per-process)
- TCP Vegas-style concurrency limiter for adaptive load shedding under Postgres connection pool exhaustion
- Redis Streams consumer group with DLQ: `XREADGROUP` → 3-attempt retry → dead-letter → manual `POST /api/v1/dlq/{id}/replay`
- `FOR UPDATE SKIP LOCKED` in the scheduled-change scheduler (prevents dual-claim across flag-api replicas)
- Per-client circuit breaker on the rollback executor using a shared wrapped `*http.Client`

All implemented across Go 1.22 services (flag-api, gateway, evaluator, gitops-sync, ast-rewriter, marketplace, tombstone-operator).

GitHub: https://github.com/sairum0424/tombstone

Thanks for considering it for inclusion.

---

## StackShare Listing

**URL:** https://stackshare.io/tools/new (requires account)

**Tool Name:** Tombstone

**Website URL:** https://github.com/sairum0424/tombstone

**Tagline:** Self-hosted feature flag platform with circuit-breaker auto-rollback

**Description (500 chars):**
Tombstone is a production intelligence layer for feature flags. It auto-rolls back flags when error rates spike (circuit breaker), correlates incidents to flag changes (causal correlation with exponential decay scoring), and gates changes via blast radius scoring. Self-hosted: Go + Python + TypeScript, PostgreSQL + Redis + Kafka, OpenFeature-compatible, MIT licensed.

**Category:** Feature Flags / DevOps Tools / Developer Tools

**Tags:** feature-flags, go, python, typescript, self-hosted, open-source, devops, circuit-breaker, kubernetes, openfeature

**Compared to:**
- LaunchDarkly — self-hosted alternative, automated rollback (LaunchDarkly requires manual kill switch)
- Unleash — adds circuit-breaker auto-rollback and causal incident correlation
- Flagsmith — adds blast radius gating and ML-driven rollout
- Flipt — adds intelligence layer (anomaly detection, Thompson Sampling, LinUCB)

---

## devhunt.org Submission

**URL:** https://devhunt.org/submit (requires account)

**Name:** Tombstone

**Tagline:** Self-hosted feature flags with circuit-breaker auto-rollback

**Description:**
Tombstone is a production intelligence layer for feature flags at scale. When a flag causes >5% errors over 100 requests in a 10-second window, the evaluator rolls it back automatically — no human required. Causal incident correlation answers "was it a flag?" in one API call. Blast radius gating prevents high-risk flag changes. ML-driven rollout (Thompson Sampling + LinUCB) advances rollout percentages automatically during safe windows.

Self-hosted. MIT licensed. No telemetry. `make dev` to start.

**Website:** https://github.com/sairum0424/tombstone

**Category:** DevOps / Developer Tools

**Tags:** feature-flags, golang, devops, self-hosted, circuit-breaker, open-source

---

## Slack Messages

### CNCF Slack — #openfeature channel
**Status:** HOLD until open-feature/community PR #554 is merged

---

Hi everyone — I wanted to share Tombstone, an OpenFeature-compatible self-hosted feature flag platform I've been building.

The differentiator: it treats flags as causal production actors rather than just configuration delivery. The evaluator service has a circuit breaker that auto-rolls back flags when error rates exceed 5% threshold — no manual kill switch needed.

We implemented the OpenFeature provider in all 9 SDKs (TypeScript/Node, React, Edge/CF Workers, Browser, WASM, Python, Java, .NET, Ruby). The provider interface maps cleanly to Tombstone's in-process 5-step evaluation pipeline — no evaluation over the network.

GitHub: https://github.com/sairum0424/tombstone

Happy to discuss the OpenFeature integration, or how the circuit breaker interacts with the flag evaluation lifecycle. Would also love feedback from the community on the provider implementation.

---

### Platform Engineering Slack — #show-and-tell channel
**Status:** Send any time (no gate required)

---

Hi all — sharing a project I've been working on: Tombstone, a self-hosted feature flag platform with production intelligence features.

The tl;dr: it auto-rolls back feature flags when error rates spike. When a flag causes >5% errors over 100 requests in a 10-second window, the evaluator kills it automatically. MTTR for "bad flag" incidents goes from minutes (waiting for on-call to wake up) to seconds.

Other capabilities that might be relevant to platform engineers:
- Causal incident correlation — "which flag caused this?" answered via `GET /api/v1/incidents/{id}/correlation`
- Blast radius gating — BLOCKED/HIGH/MEDIUM/LOW before any flag change, based on traffic %, dependent flags, experiment collision groups
- Kubernetes operator with FeatureFlag + FlagPolicy CRDs
- GitOps sync (YAML-as-code flag definitions)
- Merkle-linked audit chain (tamper-evident, append-only)

v1.2.1 just shipped with 10 resilience phases and a complete documentation suite. Self-hosted, MIT licensed.

GitHub: https://github.com/sairum0424/tombstone
`make dev` — full stack in one command.

Happy to answer questions!

---

## Tracking

| Channel | Status | Date |
|---------|--------|------|
| DevOps Weekly follow-up | 📝 Ready to send | — |
| Go Weekly follow-up | 📝 Ready to send | — |
| StackShare | 📝 Ready to submit | — |
| devhunt.org | 📝 Ready to submit | — |
| CNCF Slack #openfeature | HOLD — awaiting PR #554 merge | — |
| Platform Engineering Slack #show-and-tell | 📝 Ready to send | — |
