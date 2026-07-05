# Twitter / X Launch Thread — Tombstone v1.2.1

8 tweets. Post as a thread in sequence. Reply to your own tweets immediately.
Optimal time: Tuesday–Thursday, 9–11am US Eastern.

---

## Tweet 1 (hook — anchor tweet)

Knight Capital lost $440M in 45 minutes in 2012 because of a feature flag.

Not a bug. Not a breach. A flag.

The standard answer is "use a kill switch."

The problem: kill switches require a human to notice, diagnose, and flip.

By then it's already 3am and the damage is done.

I built the alternative. 🧵

---

## Tweet 2 (the core idea)

Tombstone is a self-hosted feature flag platform with circuit-breaker auto-rollback.

When a flag causes >5% errors over 100 requests in a 10-second window, the evaluator kills it automatically.

No PagerDuty alert. No on-call. MTTR goes from minutes to seconds.

---

## Tweet 3 (causal correlation)

The harder problem: "which of my 5,000 active flags caused this incident?"

Tombstone's answer:

GET /api/v1/incidents/{id}/correlation

Returns flags that changed in the preceding window, ranked by exponential decay score, with one-click rollback URLs.

"Was it a flag?" is now one API call.

---

## Tweet 4 (blast radius gating)

Before any flag change, Tombstone computes a blast radius score:

- BLOCKED: >80% traffic + prior incidents → hard stop
- HIGH / MEDIUM / LOW: graduated warnings

Inputs: traffic %, dependent flags, experiment collision groups, historical incident count.

A BLOCKED flag cannot be changed without explicit override.

---

## Tweet 5 (v1.2.1 resilience phases)

v1.2.1 closed the gap between "interesting demo" and "I'd run this in production."

10 resilience phases:
- Retry + jitter on all inter-service calls
- Distributed rate limiting via Redis Lua (global across replicas)
- Redis Streams DLQ with manual replay
- Adaptive load shedding (TCP Vegas)
- Snapshot reconciliation loop

All 28 pre-release checks: PASS.

---

## Tweet 6 (stack + architecture)

Stack:
- Go 1.22 — flag-api, gateway, evaluator, gitops-sync, ast-rewriter, marketplace, k8s operator
- Python 3.12 — ML intelligence (Z-score + Isolation Forest + EWMA ensemble, LinUCB, Thompson Sampling)
- TypeScript — 9 SDKs + React dashboard + CLI + MCP server

PostgreSQL 16 + pgvector, Redis Streams, Kafka.

OpenFeature-compatible. MIT licensed. No telemetry.

---

## Tweet 7 (self-hosted angle)

Why self-host feature flags?

- LaunchDarkly: ~$50k+/yr for 50 engineers
- Data sovereignty: flags often contain business logic you don't want leaving your VPC
- Audit chain: every flag change is Merkle-linked — tamper-evident, append-only

`make dev` → full stack (API :8081, dashboard :3000) in one command.

---

## Tweet 8 (CTA)

Tombstone v1.2.1 is live.

9 SDKs (TypeScript, React, Edge/CF Workers, Browser, WASM, Python, Java, .NET, Ruby), OpenFeature provider, Kubernetes operator with CRDs, Helm chart.

GitHub: https://github.com/sairam0424/tombstone

If you've ever been paged at 3am for a flag incident — this is for you.

---

## Hashtags (add to tweet 8 or distribute across thread)

#featureflags #devops #platformengineering #golang #opensource #kubernetes #selfhosted

---

## Engagement notes

- Reply to every reply within the first 2 hours
- Quote-tweet any interesting responses
- Pin the thread anchor tweet to your profile for the launch week
- Cross-post the thread opener to LinkedIn as a text post (not a link — LinkedIn deprioritizes external links)
