# Newsletter Submission Emails

Send these after a substantive release or when the Show HN is live.

---

## DevOps Weekly — gareth@morethanseven.net

**Subject:** Tombstone — self-hosted feature flag platform with circuit-breaker auto-rollback

```
Tombstone is a self-hosted production intelligence layer for feature flags — blast-radius
gating before rollouts, circuit-breaker auto-rollback on SLO breach, and "What Changed?"
causal incident correlation. MIT, Docker Compose, Go + Python + TypeScript.
GitHub: https://github.com/sairam0424/Tombstone
```

(3 sentences max — that's the format Gareth uses)

### v1.2.1 Follow-Up (send after v1.0.0 was sent 2026-06-28)

**Subject:** Re: Tombstone — v1.2.1 released

```
Quick update on Tombstone (sent you v1.0.0 on 2026-06-28): v1.2.1 is out with 10
production-hardening patterns — distributed Redis Lua rate limiting, dead-letter queue
for poison stream messages, SKIP LOCKED scheduler deduplication, adaptive load shedding,
and idempotency keys on mutation endpoints — plus a full documentation suite (architecture
reference, API guide, operator runbook).
https://github.com/sairam0424/Tombstone
```

---

## Golang Weekly — cooperpress.com/submit

**Form fields:**
- URL: https://github.com/sairam0424/Tombstone
- Title: Tombstone — Self-Hosted Feature Flag Platform with Circuit Breaker Auto-Rollback
- Description: A production intelligence layer for feature flags built in Go. Implements circuit-breaker auto-rollback (5%/100req/10s threshold), blast-radius scoring, OPA hot-reload RBAC, Merkle-chained audit logs, and sync.Map lock-free SSE hub. Multi-module workspace (go.work) across 7 services.

**Best angle for Go Weekly:** lead with the Go architecture (sync.Map SSE hub, sqlc, OPA hot-reload, go.work workspace pattern) — not the product pitch.

### v1.2.1 Follow-Up (send after v1.0.0 was sent 2026-06-28)

**Subject:** Re: Tombstone Go feature flag platform — v1.2.1

```
Quick follow-up on Tombstone (Go feature flag platform, sent 2026-06-28): v1.2.1 ships
three Go-specific improvements worth noting — distributed rate limiting via a single
atomic Redis Lua script replacing per-process sync.Map so multi-replica deployments
share one limit; SKIP LOCKED scheduler preventing duplicate execution across replicas;
and a Redis Streams DLQ that reclaims poison messages via XPENDING + XCLAIM after
3 delivery attempts with a 15 s sweep interval.
https://github.com/sairam0424/Tombstone
```

---

## console.dev — hello@console.dev

**Subject:** Tombstone — self-hosted feature flag intelligence layer for production

```
I built Tombstone: a self-hosted alternative to LaunchDarkly/Unleash that adds
blast-radius gates, circuit-breaker auto-rollback, and causal incident correlation.
The core idea: treat 5,000 active flags as a causal graph of production behavior
so you can answer "which flag caused this incident?" rather than "what's the flag value?"

- MIT licensed, self-hostable via Docker Compose (`make dev` — zero config for local dev)
- CLI (@tombstone/cli), REST API, MCP server (8 tools), VS Code + JetBrains plugins
- Dashboard with dark mode at localhost:3000
- Go + Python + TypeScript, v1.0.0
- GitHub: https://github.com/sairam0424/Tombstone

Let me know if it's a fit for the newsletter.
```

---

## CNCF Slack #openfeature — Hold until interested-parties.md PR is merged

**Channel:** slack.cncf.io → #openfeature

**Message (paste exactly once PR #554 is merged):**

```
Hey everyone — I wanted to share Tombstone, a self-hosted feature flag intelligence
platform that ships a fully spec-compliant OpenFeature provider out of the box.

The OpenFeature provider is in `packages/sdks/@tombstone/core/src/provider.ts` and
implements the full evaluation lifecycle (BEFORE/AFTER/ERROR/FINALLY hooks, typed
ResolutionDetails, ProviderEvents). Drop-in with `OpenFeature.setProvider(new TombstoneProvider(client))`.

What's different about Tombstone vs other OSS flag platforms: it treats flags as a
live causal graph of production behavior — blast-radius scoring before rollouts,
circuit-breaker auto-rollback on SLO breach, and "What Changed?" incident correlation.

GitHub: https://github.com/sairam0424/Tombstone
Happy to answer questions or take feedback on the provider implementation.
```

---

## Platform Engineering Slack #show-and-tell

**Workspace:** platformengineering.org Slack
**Channel:** #show-and-tell

**Message:**

```
Built something I wanted to share: Tombstone — a self-hosted feature flag platform
designed specifically for platform engineering use cases.

The core idea: most flag platforms answer "what is this flag's value?" — Tombstone
answers "which of my 5,000 active flags is responsible for what's happening in
production right now?"

Key bits:
• Blast-radius scoring (BLOCKED/HIGH/MEDIUM/LOW) gated before every rollout
• Circuit-breaker auto-rollback — >5% error rate on a flag's traffic → auto-disable,
  no pager required
• "What Changed?" causal incident correlation — millisecond query over the flag
  change graph
• Kubernetes operator (FeatureFlag/FlagPolicy CRDs), GitOps flag sync, OPA RBAC
• AST rewriter for dead-code cleanup after tombstoning
• Self-hosted via `make dev` — Docker Compose, zero-config for local dev
• MIT, Go + Python + TypeScript

v1.2.1 just shipped with 10 production-hardening patterns (distributed rate limiting,
DLQ for poison stream messages, SKIP LOCKED scheduler, adaptive load shedding,
idempotency keys on mutations).

GitHub: https://github.com/sairam0424/Tombstone
Curious if this maps to anything you're dealing with on the platform side.
```

---

## TLDR DevOps / TLDR Open Source — tldr.tech/submit

**Submission (keep to 2-3 sentences):**
```
Tombstone is a self-hosted feature flag platform with circuit-breaker auto-rollback —
when a flag causes >5% errors, it disables automatically without human intervention.
Includes blast-radius scoring, causal incident correlation ("What Changed?"), and
ML-driven rollout recommendations. MIT, Docker Compose. https://github.com/sairam0424/Tombstone
```
