# Show HN — v1.2.1 (Updated)

## Title

Show HN: Tombstone v1.2.1 – self-hosted feature flags with circuit-breaker auto-rollback and causal incident correlation

---

## Body (paste directly into the HN submission text field)

I built a self-hosted feature flag platform that automatically rolls back flags when error rates spike — no human required. v1.2.1 is the production hardening release: 10 resilience phases, a complete documentation suite, and adversarial validation by a 26-agent pre-release test suite.

The core thesis: every competitor asks "how do I deliver a flag value?" — Tombstone asks "which of my 5,000 active flags is responsible for what's happening in production right now?"

Repo: https://github.com/sairam0424/tombstone
Demo: `make dev` — full stack (API :8081, dashboard :3000) via Docker Compose

---

## First comment (post immediately after submitting)

**The Knight Capital problem.** In 2012, a feature flag that should have been deleted was accidentally reactivated during a deploy. $440M gone in 45 minutes. The standard industry answer is "use a kill switch" — but kill switches require a human to notice, diagnose, and act. By then it's too late.

Tombstone started with two ideas I couldn't find in any OSS flag system:

**1. Circuit-breaker auto-rollback.**
When a flag causes >5% errors over 100 requests in a 10-second window, the evaluator service kills it automatically. MTTR goes from "however long it takes your on-call to wake up" to seconds. No PagerDuty alert required.

**2. Causal incident correlation.**
Given an incident timestamp, `GET /api/v1/incidents/{id}/correlation` returns the flags that changed in the preceding window, ranked by exponential decay score (a flag changed 2 min before scores higher than one changed 28 min before), with one-click rollback URLs. "Was it a flag?" takes one API call instead of 20 minutes of log archaeology.

---

v1.0.0 was the concept. v1.2.1 closed the gap between "interesting demo" and "I would run this in production."

**10 resilience phases shipped in v1.2.1:**

1. **Retry + jitter.** Every inter-service HTTP call goes through a shared client: MaxRetries=3, exponential backoff with jitter, per-client circuit breaker.
2. **Circuit breaker on inter-service calls.** The evaluator's rollback executor now shares the same circuit-breaker-wrapped client as the rest of the service mesh.
3. **Distributed rate limiting via Redis Lua.** `sync.Map` token buckets replaced by an atomic Lua leaky-bucket script — quota is now global across all replicas.
4. **Adaptive load shedding (TCP Vegas).** Under sustained load that exhausts the Postgres connection pool, requests are immediately rejected with a retryable 503.
5. **Idempotency keys on mutation endpoints.** `POST /flags`, `PATCH /flags/{key}/environments/{env}`, and `POST /flags/{key}/kill` accept and enforce `Idempotency-Key` headers.
6. **Redis Streams DLQ with manual replay.** Malformed messages go through a 3-attempt retry before landing in a dead-letter queue inspectable via `GET /api/v1/dlq` and replayable via `POST /api/v1/dlq/{id}/replay`.
7. **Scheduler `FOR UPDATE SKIP LOCKED`.** Two flag-api replicas cannot claim the same scheduled change row simultaneously.
8. **Webhook deduplication.** Outbound webhooks include an `Idempotency-Key` derived from the event ID — retry loops on the receiving side cannot create duplicate incidents.
9. **Asyncio hardening.** A lock guards concurrent ML retraining jobs; warehouse queries run in a bounded `ThreadPoolExecutor` with a 30-second timeout.
10. **Snapshot reconciliation loop.** A background loop runs every 5 minutes and publishes correction events for any delta between Postgres state and the Redis cache.

---

**Documentation suite added in v1.2.1:**

- `docs/EVALUATION_MODEL.md` — complete 5-step evaluation pipeline with hash bucketing and worked examples
- `docs/INTELLIGENCE_MODEL.md` — 3-model ensemble anomaly detector, LinUCB contextual bandit, Thompson Sampling, CUPED, mSPRT
- `docs/DEPLOYMENT_KUBERNETES.md` — Helm chart, multi-region values files, CRD install, readiness probe configuration
- `docs/SDK_INTEGRATION_GUIDE.md` — all 9 SDKs (TypeScript/Node, React, Edge/CF Workers, Browser, WASM, Python, Java, .NET, Ruby)
- `docs/runbooks/` — CIRCUIT_BREAKER, DLQ_REDIS_STREAMS, AUTO_ROLLBACK, RATE_LIMITING, BLAST_RADIUS

---

**Adversarial validation.** Before tagging v1.2.1, a 26-agent pre-release suite ran 4 independent passes (E2E, resilience/idempotency/Merkle/DLQ, deployment readiness, v1.1.0 regression). All 28 sub-checks returned PASS.

**Stack:** Go 1.22 + Python 3.12 + TypeScript (9 SDKs + React dashboard + CLI + MCP server). PostgreSQL 16 + pgvector, Redis, Kafka. MIT licensed, no telemetry.

Happy to answer questions about the circuit breaker, the Thompson Sampling rollout engine, or the Lua leaky-bucket rate limiting.

---

## Optimal posting time

- Tuesday–Thursday, 8–11am US Eastern
- Do NOT post Friday–Sunday
- Monitor comments for first 2 hours and respond to every technical question within 30 minutes — HN ranking is sensitive to early engagement velocity

---

## Expected HN questions to prepare for

**1. "How is this different from Unleash/Flagsmith/Flipt?"**
Circuit-breaker auto-rollback and causal incident correlation exist in none of them at any tier. Those tools model flags as a configuration delivery problem. Tombstone models them as causal production actors: each flag change is Merkle-linked in an audit chain, participates in incident scoring, and can trigger its own rollback.

**2. "Why not just use LaunchDarkly?"**
Per-seat pricing at scale ($50k+/yr for 50 engineers), data sovereignty (flags often contain business logic you don't want leaving your VPC), and LaunchDarkly does not have automated rollback. Their circuit breaker is a kill switch — it still requires a human to flip it.

**3. "How does the circuit breaker know which flag caused the error?"**
SDKs report evaluation events (flag key + evaluation outcome + context) to the evaluator service. The circuit breaker tracks a per-flag rolling window in Redis: TotalCount and ErrorCount over a 10-second window, with a 100-request minimum volume guard.

**4. "What's the operational overhead of running 8 services?"**
Honest answer: non-trivial. For small teams, `make dev` (Docker Compose) is the full stack. The minimal footprint is flag-api + gateway + evaluator — the circuit breaker and blast radius scoring live in evaluator. The intelligence service (ML/anomaly) is optional and the system degrades gracefully without it.

**5. "Is the ML rollout actually useful or is it ML for marketing?"**
The Thompson Sampling Beta posteriors are ~50 lines of Python. The LinUCB matrices are persisted in Redis and survive service restarts. The concrete value: neither requires you to manually advance rollout percentages — the system holds back during bad windows and advances when conditions look safe.

**6. "What's the blast radius scoring based on?"**
Four inputs: traffic percentage the flag is enabled for, number of flags that declare this flag as a prerequisite, number of flags in the same experiment collision group, and historical incident count for this flag key. Result: BLOCKED (>80% traffic + prior incidents), HIGH, MEDIUM, or LOW.

**7. "Why Kafka? Isn't that overkill?"**
Kafka is only in the intelligence service's event ingestion pipeline. The flag delivery path (flag-api → Redis Streams → gateway → SDK) does not touch Kafka. Small deployments can skip the intelligence service entirely.
