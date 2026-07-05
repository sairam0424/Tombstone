# Product Hunt Listing — Tombstone v1.2.1

## Status

HOLD — complete warm-up (4–6 weeks of engagement on PH) before launching.
Gate: be an active PH community member; upvote and comment on other products first.

---

## Tagline (60 chars max)

Self-hosted feature flags that auto-rollback bad deploys

---

## Description (260 chars max — for the product card)

Tombstone is a production intelligence layer for feature flags. When a flag causes >5% errors, the evaluator rolls it back automatically — no human required. Self-hosted, MIT licensed, 9 SDKs, OpenFeature-compatible.

---

## Full description (for the product page)

**The problem: feature flags become liabilities at scale.**

Knight Capital lost $440M in 45 minutes because a flag that should have been deleted was accidentally reactivated. Industry answer: "use a kill switch." Problem: kill switches require a human to notice, diagnose, and flip — and by then it's already 3am and the damage is done.

**What Tombstone does differently:**

Tombstone is not just a flag delivery system. It is a production intelligence layer that treats flags as causal production actors.

**Automatic circuit-breaker rollback.** The evaluator service tracks per-flag error rates in a rolling Redis window (10-second window, 100-request minimum). When error rate exceeds 5%, the flag is killed automatically. MTTR goes from minutes to seconds.

**Causal incident correlation.** Every flag change is Merkle-linked in an audit chain. Given an incident timestamp, the correlation endpoint returns which flags changed in the preceding window, ranked by exponential decay score, with one-click rollback URLs. "Was it a flag?" is now one API call.

**Blast radius gating.** Before any flag change, the evaluator computes a BLOCKED/HIGH/MEDIUM/LOW score based on traffic percentage, dependent flags, experiment collision groups, and historical incident count. BLOCKED flags cannot be changed without override.

**ML-driven rollout.** Thompson Sampling Beta posteriors + LinUCB contextual bandit advance rollout percentages automatically, holding back during bad windows (high error rate, off-hours patterns) and advancing when conditions look safe.

**Built for self-hosted production:**
- Go 1.22 (flag-api, gateway, evaluator, 6 more services) + Python 3.12 (ML/intelligence) + TypeScript (9 SDKs + React dashboard + CLI + MCP server)
- PostgreSQL 16 + pgvector, Redis Streams, Kafka
- OpenFeature-compatible (drop-in provider for all 9 SDKs)
- Kubernetes operator with FeatureFlag + FlagPolicy CRDs
- Helm chart with multi-region values files
- MIT licensed, no telemetry, no phone-home

**v1.2.1 highlights:**
- 10 resilience phases (retry/jitter, distributed rate limiting via Redis Lua, adaptive load shedding, Redis Streams DLQ with manual replay, snapshot reconciliation loop, and more)
- Complete documentation suite (evaluation model, K8s deployment, SDK integration guide, 5 operational runbooks)
- 26-agent adversarial pre-release validation with 28 sub-checks

`make dev` — full stack in one command.

---

## Topics / Categories

- Developer Tools
- Open Source
- DevOps
- Platform Engineering
- Productivity

---

## Links

- Website / GitHub: https://github.com/sairum0424/tombstone
- Demo: `make dev` (Docker Compose — API :8081, dashboard :3000)

---

## Maker comment (post immediately after launch goes live)

Hi PH! I'm the solo builder behind Tombstone.

The motivation was Knight Capital — a flag that should have been deleted got accidentally reactivated in 2012 and cost $440M in 45 minutes. I looked at every OSS feature flag platform and none of them have automated rollback. Unleash, Flagsmith, Flipt — they all have kill switches, which still require a human to flip.

Tombstone v1.2.1 is the production hardening release. The core innovation is treating flags as causal production actors rather than configuration delivery:

1. **Circuit-breaker auto-rollback** — no human required to roll back a bad flag
2. **Causal incident correlation** — "which flag caused this?" answered in one API call
3. **Blast radius gating** — a BLOCKED flag cannot be changed without explicit override

v1.2.1 added 10 resilience phases (distributed rate limiting via Redis Lua, Redis Streams DLQ with manual replay, adaptive load shedding, etc.) and a complete documentation suite.

Happy to answer questions about the architecture, the Thompson Sampling rollout engine, or why I chose this problem. What would you want from a self-hosted flag platform?

---

## Media / Screenshots checklist

- [ ] Hero GIF: `make dev` → dashboard → flag creation → circuit breaker trip → auto-rollback
- [ ] Screenshot 1: Dashboard overview — 5,000 flags with blast radius scores
- [ ] Screenshot 2: Incident correlation view — ranked flag candidates with rollback buttons
- [ ] Screenshot 3: Circuit breaker state machine — real-time error rate window
- [ ] Screenshot 4: SDK code snippet — TypeScript + OpenFeature provider setup
- [ ] Screenshot 5: Kubernetes operator — FeatureFlag CRD YAML
- [ ] Product thumbnail: 240×240 logo or icon

---

## Warm-up checklist (complete before launching)

- [ ] Be active on Product Hunt for 4–6 weeks (upvote, comment, follow)
- [ ] Identify 10+ PH hunters in the developer tools / DevOps space to notify on launch day
- [ ] Schedule launch for Tuesday–Thursday, 12:01am Pacific (right at PH day start)
- [ ] Prepare 3–5 community members to upvote + comment in first 30 minutes
- [ ] Draft responses to top 5 expected questions in advance
