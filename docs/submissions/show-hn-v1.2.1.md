# Show HN Post — v1.2.1

## Title

Show HN: Tombstone v1.2.1 – self-hosted feature flags with circuit-breaker auto-rollback and 10 resilience phases

---

## Body (paste directly into the HN submission text field)

I built a self-hosted feature flag platform that auto-rolls back flags when error rates spike — no human required. v1.2.1 is the production hardening release: 10 resilience phases, a full documentation suite, and adversarial validation by a 26-agent pre-release test suite.

Repo: https://github.com/[your-handle]/tombstone
Demo: `make dev` — API on :8081, dashboard on :3000

---

## First comment (post immediately after submitting)

The motivation is Knight Capital: they lost $440M in 45 minutes in 2012 because a flag that should have been deleted got accidentally reactivated. The standard industry answer is "use a kill switch" — but kill switches require a human to notice, diagnose, and act. By then it's too late.

Tombstone started with two ideas I couldn't find in any OSS flag system:

**1. Circuit-breaker auto-rollback.** When a flag causes >5% errors over 100 requests in a 10-second window, the evaluator service kills it automatically. MTTR goes from "however long it takes your on-call to wake up" to seconds. No PagerDuty alert required.

**2. Causal incident correlation.** Given an incident timestamp, `GET /api/v1/incidents/{id}/correlation` returns the flags that changed in the preceding window, ranked by exponential decay score (a flag changed 2 minutes before scores higher than one changed 28 minutes before), with one-click rollback URLs. "Was it a flag?" takes one API call instead of 20 minutes of log archaeology.

---

That was v1.0.0. v1.2.1 closed the gap between "this is an interesting demo" and "I would run this in production." The honest summary from the release notes: the v1.0.0 HTTP clients had no retries. Rate limiting was per-process (so three replicas tripled the allowed rate). The gateway had no dead-letter queue — a malformed message permanently blocked a stream partition. The Datadog auto kill-switch silently returned HTTP 401 on every call. CI masked test failures with `|| true`.

**The 10 resilience phases that closed those gaps:**

1. **Retry + jitter.** Every inter-service HTTP call (rollback executor, Datadog inbound, GitOps sync, Slack kill-switch) now goes through a shared client: MaxRetries=3, exponential backoff with jitter, per-client circuit breaker. A transient Redis blip no longer silently drops a rollback.

2. **Circuit breaker on inter-service calls.** The evaluator's rollback executor previously used a plain `*http.Client` with a 5s timeout and zero retries. Now it shares the same circuit-breaker-wrapped client as the rest of the service mesh.

3. **Distributed rate limiting via Redis Lua.** The `sync.Map` token buckets are replaced by an atomic Lua leaky-bucket script. Quota is now global across all replicas — three pods enforce the intended rate, not three times it.

4. **Adaptive load shedding (TCP Vegas concurrency limiter).** Under sustained load that exhausts the Postgres connection pool, requests are immediately rejected with a retryable 503 rather than queuing indefinitely.

5. **Idempotency keys on mutation endpoints.** `POST /flags`, `PATCH /flags/{key}/environments/{env}`, and `POST /flags/{key}/kill` accept and enforce `Idempotency-Key` headers. A retried kill-switch call is always a no-op — no duplicate audit log row, no second Redis broadcast.

6. **Redis Streams DLQ with manual replay.** The gateway previously ACKed every message immediately, including unmarshal failures — poison messages were silently dropped. Now malformed messages go through a three-attempt retry before landing in a dead-letter queue inspectable via `GET /api/v1/dlq` and replayable via `POST /api/v1/dlq/{id}/replay`.

7. **Scheduler `FOR UPDATE SKIP LOCKED`.** The scheduler's `SELECT` is wrapped in a transaction with `FOR UPDATE SKIP LOCKED`. Two flag-api replicas cannot claim the same scheduled change row simultaneously.

8. **Webhook deduplication.** Outbound webhooks to PagerDuty, Jira, and custom integrations include an `Idempotency-Key` derived from the event ID. Retry loops on the receiving side cannot create duplicate incidents.

9. **Asyncio hardening.** A lock guards concurrent ML retraining jobs and graph rebuilds in the Python intelligence service. Warehouse queries run in a bounded `ThreadPoolExecutor` with a 30-second timeout per query — a hung BigQuery connection cannot stall the event loop.

10. **Snapshot reconciliation loop.** A background loop runs every 5 minutes, compares live flag state in Postgres against the Redis cache, and publishes correction events for any delta. Long-lived SSE connections receive state corrections within 5 minutes of a missed event, rather than staying stale until reconnect.

---

**The documentation suite** (because production-readiness is also about operability):

- `docs/EVALUATION_MODEL.md` — the complete 5-step evaluation pipeline with hash bucketing, flag state semantics, and worked examples. All logic is in-process; evaluation never makes a network call.
- `docs/runbooks/CIRCUIT_BREAKER.md` — quick-reference table, state machine diagram, Redis inspection commands, reset procedure.
- `docs/runbooks/DLQ_REDIS_STREAMS.md` — PEL inspection, replay procedure, how to identify structurally unreplayable messages.
- `docs/runbooks/AUTO_ROLLBACK.md` — trigger conditions, what the audit log records, how to override if the CB trips on a false positive.
- `docs/runbooks/RATE_LIMITING.md` — Lua script walkthrough, how to tune per-endpoint limits.
- `docs/DEPLOYMENT_KUBERNETES.md` — Helm chart, multi-region values files, CRD install, readiness probe configuration.
- `docs/SDK_INTEGRATION_GUIDE.md` — all nine SDKs (TypeScript/Node, React, Edge/Cloudflare Workers, Browser, WASM, Python, Java, .NET, Ruby), OpenFeature provider setup.
- `docs/INTELLIGENCE_MODEL.md` — the 3-model ensemble anomaly detector (Z-score + Isolation Forest + EWMA), LinUCB contextual bandit, Thompson Sampling, CUPED variance reduction, mSPRT sequential testing.

---

**Adversarial validation.** Before tagging v1.2.1, a 26-agent pre-release suite ran four independent passes: E2E validation (7 sub-checks), full resilience/idempotency/Merkle/DLQ validation (9 sub-checks), deployment readiness (6 sub-checks), and v1.1.0 feature regression check (6 sub-checks). All 28 sub-checks returned PASS. The validation manifest with specific line-number evidence is in the release notes.

---

**Stack:** Go 1.22 (flag-api, gateway, evaluator, gitops-sync, ast-rewriter, marketplace, tombstone-operator) + Python 3.12 (intelligence/ML) + TypeScript (9 SDKs + React dashboard + CLI + MCP server). PostgreSQL 16 + pgvector, Redis, Kafka. Everything starts with `make dev`. MIT licensed, no telemetry.

Happy to answer questions about the circuit breaker implementation, the Thompson Sampling rollout engine, or why I chose Lua leaky-bucket over a token bucket in Redis.

---

## Optimal posting time

- Tuesday–Thursday, 8–11am US Eastern
- Do NOT post Friday–Sunday
- Monitor comments for first 2 hours and respond to every technical question within 30 minutes — HN ranking is sensitive to early engagement velocity

---

## Expected HN questions to prepare for

**1. "How is this different from Unleash/Flagsmith/Flipt?"**
The short answer is the circuit-breaker auto-rollback and causal incident correlation — neither exists in any OSS flag platform at any tier. The longer answer is that those tools model flags as configuration delivery problems. Tombstone models them as causal production actors: each flag change is Merkle-linked in an audit chain, participates in incident scoring, and can trigger its own rollback. That is a different architectural assumption, not a feature addition.

**2. "Why not just use LaunchDarkly?"**
Per-seat pricing at scale ($50k+/yr for 50 engineers), data sovereignty (flags often contain business logic you don't want leaving your VPC), and LaunchDarkly does not have automated rollback. Their circuit breaker is a kill switch — it still requires a human to flip it.

**3. "How does the circuit breaker know which flag caused the error?"**
SDKs report evaluation events (flag key + evaluation outcome + context) to the evaluator service. The circuit breaker tracks a per-flag rolling window in Redis: TotalCount and ErrorCount over a 10-second window. The breaker won't trip below 100 total requests (minimum volume guard) — a single-user flag can't accidentally trip on one bad request.

**4. "What's the operational overhead of running 8 services?"**
Honest answer: non-trivial. For small teams, `make dev` (Docker Compose) is the full stack. The minimal footprint for meaningful value is flag-api + gateway + evaluator — the circuit breaker and blast radius scoring live in evaluator. The intelligence service (ML/anomaly) is optional; the system degrades gracefully without it.

**5. "Is the ML rollout actually useful or is it ML for marketing?"**
The Thompson Sampling Beta posteriors are ~50 lines of Python. The LinUCB matrices are persisted in Redis and survive service restarts. The concrete value is that neither requires you to manually decide when to advance a rollout percentage — the system holds back during bad windows (high error rate, off-hours traffic patterns) and advances when conditions look safe. Whether that's worth the operational cost depends on your rollout volume.

**6. "What's the blast radius scoring based on?"**
Four inputs: percentage of traffic the flag is currently enabled for (higher = larger blast radius), number of flags that declare this flag as a prerequisite (dependent flags fail if this one rolls back), number of flags in the same experiment collision group, and historical incident count for this flag key. The result is BLOCKED (>80% traffic and prior incidents), HIGH, MEDIUM, or LOW. The evaluator returns this score before any flag change — it is a gate, not a post-hoc label.

**7. "Why Kafka? Isn't that overkill?"**
Kafka is in the compose file but is used only for the intelligence service's event ingestion pipeline. The flag delivery path (flag-api → Redis Streams → gateway → SDK) does not touch Kafka. If you are running a small deployment, you can disable the intelligence service and not run Kafka at all.
