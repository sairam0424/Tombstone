# Tombstone v1.2.1 Release Notes

**Released:** 2026-07-05
**Prior release:** v1.0.0 (2026-06-22)
**Validation status:** All E2E checks PASS (28/28 sub-checks across 4 independent validation passes)

---

## Executive Summary

Tombstone is a self-hosted production intelligence layer for feature flags. Every other flag platform — LaunchDarkly, Unleash, Flipt, Flagsmith — asks the same question: *how do I evaluate and deliver a flag value?* Tombstone asks a different question: *which of my 5,000 active flags is responsible for what's happening in production right now?* The distinction is architectural. Where others treat flags as configuration delivery, Tombstone treats them as causally-linked production actors: a flag that changes state leaves a forensic trail, participates in incident correlation, can trigger its own rollback when error rates spike, and leaves a Merkle-linked tamper-evident audit record that satisfies SOC 2 requirements without an external audit tool. The causal dependency graph is not an afterthought — it is the reason the system exists.

v1.0.0 shipped a compelling surface: OPA RBAC, Merkle-chained audit log, Knight Capital tombstoning, break-glass tokens, four-eyes approval, a Python intelligence service with anomaly detection, nine polyglot SDKs, a Terraform provider, and a Kubernetes operator. The gap was the production infrastructure underneath it. v1.0.0's HTTP clients had no retries. Its rate limiting was per-process (so three replicas tripled the allowed rate). Its gateway had no dead-letter queue, meaning a malformed message permanently blocked a stream partition. Its CI masked test failures with `|| true`. Its Datadog auto kill-switch silently returned HTTP 401 on every call. These were not edge cases — they were load-bearing failures that would surface within hours of a real incident.

v1.2.1 is the production hardening release. Over ten resilience phases plus an adversarially-validated pre-release regression cycle, every one of those gaps was closed: resilient HTTP clients across all inter-service calls, distributed rate limiting via Redis-backed Lua, adaptive load shedding, idempotency keys on mutation endpoints, a Redis Streams dead-letter queue with manual replay, dependency-aware `/readyz` probes across every service, and jittered reconnect backoff to prevent thundering-herd storms. The intelligence service was rebuilt from a single Z-score detector into a three-model ensemble with a LinUCB contextual bandit, Thompson Sampling, CUPED variance reduction, mSPRT sequential testing, and hybrid semantic search (BAAI/bge-m3 + BM25 + Reciprocal Rank Fusion). Security gained Rekor transparency log integration, mutual TLS with an internal PKI, SCIM 2.0 provisioning, and SOC 2 evidence endpoints.

If you are evaluating Tombstone for production adoption, v1.2.1 is the release to evaluate. If you are already running v1.0.0, upgrade is a single `make migrate` — there are no breaking changes.

---

## What's New: v1.0.0 → v1.2.1

### Resilience & Reliability

**Before v1.2.1:** The evaluator's rollback executor used a plain `*http.Client` with a 5s timeout and zero retries. The gateway reconnected to Redis with deterministic (non-jittered) backoff, so all replicas slammed Redis simultaneously after a shared outage. The scheduler used a bare `SELECT` with no transaction isolation, meaning two replicas could execute the same scheduled change concurrently. Rate limiting lived in `sync.Map` — scaling to three replicas tripled the effective quota. There was no load shedding, no readiness distinction from liveness, and no idempotency layer.

**What was added:**

- **Resilient HTTP client (retry + circuit breaker):** Every inter-service HTTP call — rollback, kill-switch confirmation, Datadog inbound, GitOps sync — now goes through a shared client with `MaxRetries=3`, exponential backoff with jitter, and a per-client circuit breaker that opens at a 5% error rate over a 10-second window. A transient Redis blip no longer drops a rollback silently.

- **Dependency-aware `/readyz`:** Every Go service and the Kubernetes operator gained a `/readyz` endpoint that verifies actual dependency connectivity before returning 200. `/health` remains the liveness probe (process alive). Kubernetes readiness probes should point at `/readyz`. Rate-limiting and load-shedding middleware explicitly exempt the `/readyz` path so a pod under load can still report its readiness truthfully.

- **Distributed rate limiting (Redis-backed Lua leaky bucket):** The `sync.Map` token buckets are replaced with an atomic Lua script executed against Redis. Quota is now global across all replicas. A three-pod flag-api deployment enforces the intended rate, not three times it.

- **Adaptive load shedding (TCP-Vegas concurrency limiter):** Under sustained load that exhausts the Postgres connection pool (5 connections on the Neon free tier), requests are immediately rejected with a retryable `503` rather than queuing indefinitely. The concurrency window tracks actual inflight requests against an adaptive limit derived from observed latency, matching the TCP Vegas congestion-control model.

- **Idempotency keys on mutation endpoints:** `POST /flags`, `PATCH /flags/{key}/environments/{env}`, and `POST /flags/{key}/kill` all accept and enforce `Idempotency-Key` headers. A retried kill-switch confirmation is always a no-op — it cannot create a duplicate `audit_log` row or trigger a second Redis broadcast. Keys are scoped to the calling actor (SEC-001 fix) so a shared key string cannot cause one caller's cached kill-switch response to be served to a different caller.

- **Redis Streams dead-letter queue with manual replay:** The gateway's `RunStreamConsumer` previously ACKed every message immediately, including unmarshal failures — poison messages were silently dropped. The new path leaves malformed messages un-ACKed (they remain in the PEL), moves them to the DLQ after three failed processing attempts, and exposes `GET /api/v1/dlq` and `POST /api/v1/dlq/{id}/replay` for inspection and manual replay. The replay endpoint requires `FLAG_API_TOKEN` bearer authentication (SEC-002 fix). Structurally malformed messages — those missing the `payload` key entirely — are ACKed immediately on a distinct code path since they cannot be replayed regardless.

- **Reconnect jitter:** Both `Run()` (pub/sub) and `RunStreamConsumer()` (Streams) in `broadcaster.go` now add random jitter to their reconnect backoff. After a shared Redis outage, N gateway pods spread their reconnect attempts across the full jitter window rather than all arriving at the same instant.

- **Scheduler retry/backoff with `FOR UPDATE SKIP LOCKED`:** The scheduler's `SELECT` is now wrapped in a transaction with `FOR UPDATE SKIP LOCKED`. Concurrent flag-api replicas cannot claim the same scheduled change row simultaneously. Transient DB errors during `applyChange` result in retry with exponential backoff rather than immediate `FAILED` status.

- **Snapshot reconciliation loop:** A background loop runs every 5 minutes, compares live flag state in Postgres against the Redis cache, and publishes correction events for any delta. Long-lived SSE connections receive state corrections within 5 minutes of a missed event, rather than remaining stale until disconnect/reconnect. This closes the dual-write gap: Postgres commit + Redis publish are two separate operations, and the reconciliation loop is the belt-and-suspenders that makes the gap bounded.

---

### Production Intelligence (ML & Analytics)

**Before v1.2.1:** A single Z-score anomaly detector with a 24-hour sliding window, basic PostgreSQL full-text search, and a simple incident correlation query with no scoring.

**What was added:**

- **3-model ensemble anomaly detector:** Z-score, Isolation Forest, and Autoencoder vote on each observation. The per-model and per-granularity vote breakdown is returned in the API response, so operators know which detector(s) triggered rather than receiving an opaque `anomaly: true`. False-positive rate is substantially lower than any single model.

- **LinUCB contextual bandit for rollout advancement:** Instead of percentage-based steps, the bandit learns which context conditions (time-of-day, current error rate, traffic volume) predict safe advancement. It maintains per-arm upper confidence bounds updated on each observation, meaning it actively explores conditions that have uncertainty rather than committing to a fixed schedule.

- **Thompson Sampling for Bayesian rollout staging:** For each rollout stage boundary, Thompson Sampling maintains a Beta distribution over the probability of a safe advancement, updated from live error-rate observations. The API returns a calibrated confidence value (0–1) rather than a binary pass/fail, and advancement can be configured to require a threshold (default: 0.95) before proceeding.

- **Argos LLM rule generation (3-agent pipeline):** A proposer agent analyzes a flag's traffic pattern and drafts an anomaly detection rule in Rego; a critic agent stress-tests it against historical data; a final agent produces the human-review artifact. Rules are not activated without manual approval. The pipeline is accessible via `POST /api/v1/flags/{key}/generate-rule`.

- **Hybrid semantic search (BAAI/bge-m3 + BM25 + RRF):** Flag search now fuses dense embeddings from `BAAI/bge-m3` with BM25 term-frequency scores using Reciprocal Rank Fusion. Searching `payment retry logic` surfaces relevant flags even when none of them have `payment` in their key. The embedding model runs in-process; no external embedding service is required.

- **CUPED variance reduction:** Experiment results use pre-experiment covariate data to reduce variance by 20–40%, allowing the same statistical power with fewer users or shorter run times. The implementation includes full Welch confidence intervals and is exposed as a standalone module callable independently of the experiment pipeline.

- **mSPRT sequential testing:** Analysts can monitor running experiments continuously without the alpha-inflation penalty of repeated significance testing. Ville's inequality is used for the always-valid confidence interval. A futility check allows stopping experiments that have no realistic chance of detecting an effect, freeing traffic.

- **Experiment collision detection:** Before launching a new experiment, `POST /api/v1/experiments/check-collision` evaluates whether the proposed user population overlaps with any running experiment, returns the overlap fraction and the conflicting experiment IDs, and can be configured to block launch above a threshold.

- **Warehouse connectors (BigQuery, Snowflake, Databricks):** Metric data from cloud warehouses can be pulled directly into the statistical analysis pipeline. Each connector runs in a bounded `ThreadPoolExecutor` with a 30-second timeout per query, preventing a hung warehouse connection from stalling the intelligence service event loop.

- **Incident correlation scoring:** The `GET /api/v1/incidents/{id}/correlation` endpoint now returns flags ranked by exponential decay score (flags changed 2 minutes before the incident score higher than flags changed 28 minutes before), with confidence labels and one-click rollback URLs. This endpoint is also called automatically when the Datadog inbound webhook fires.

- **Asyncio hardening (v1.2.0):** A lock guards concurrent ML retraining jobs and graph rebuilds; warehouse queries run in a bounded executor and cannot starve the embedding model's thread pool or block the event loop indefinitely.

---

### Security & Compliance

Features present since v1.0.0: OPA policy-as-code RBAC with hot-reload (fsnotify watcher on Rego files, four-tier model mapping to SOC 2 CC6.1), Merkle-linked append-only audit log with `prev_hash` chaining and DDL-level `no_audit_update`/`no_audit_delete` rules, Knight Capital tombstoning (permanent archive preventing flag key reuse, enforced at the database trigger layer), break-glass emergency tokens with full audit trail, and four-eyes approval workflow (`change_requests` table with `PENDING_APPROVAL` state).

**Added post-v1.0.0:**

- **Rekor transparency log integration:** After each audit event is written and Merkle-chained internally, the event hash is submitted to the public Rekor transparency log. The returned `rekor_log_id` and `rekor_log_index` are stored in `audit_log`. A full database compromise cannot retroactively erase audit entries that have already been anchored in Rekor.

- **Mutual TLS with internal PKI:** `flag-api/tlsutil` ships `certgen.go` (CA generation) and `loader.go` (cert/key loading with hot-reload). TLS 1.3 is enforced. Inter-service calls from evaluator, gateway, and gitops-sync present client certificates; requests without a valid certificate signed by the internal CA are rejected at the transport layer.

- **SCIM 2.0 user provisioning and orphan detection:** `scim.go` implements the SCIM 2.0 User resource. When a user is deprovisioned via your IdP, their flags are automatically surfaced in the approval queue for reassignment or archival. The `sso.go` middleware bridges SSO identity to the RBAC layer.

- **SOC 2 evidence endpoints:** `GET /api/v1/compliance/evidence` returns an HMAC-signed NDJSON bundle containing audit log entries, MFA log (`user_mfa_log`), and compliance snapshots. The bundle is suitable for attachment to SOC 2 audit packages without manual data extraction. The schema is defined in migration `003_compliance.sql`.

- **Idempotency actor scoping (SEC-001):** Migration `012` adds an actor column to the idempotency key store. A key is only reused when the key string *and* the calling actor match, preventing a shared key from returning a cached response to a different caller.

- **DLQ replay auth (SEC-002):** The `POST /api/v1/dlq/{id}/replay` endpoint requires a `FLAG_API_TOKEN` bearer token. Without this guard, any process with network access to the gateway port could inject arbitrary messages into the flag event stream.

---

### Integrations & Ecosystem

- **Slack integration (hardened):** The `executeKillSwitch` function now sends the `FLAG_API_TOKEN` authorization header to flag-api and includes `Environment` and `Reason` fields in the kill body. The v1.0.0 implementation sent no authorization header, causing every slash command kill to silently fail with HTTP 401.

- **Datadog inbound webhook (fixed):** `postKillSwitch` in `inbound.go:293` now sends the `Authorization` header. At v1.0.0, the Datadog auto kill-switch feature was effectively non-functional in any deployment with RBAC enabled — every call returned 401 and was swallowed.

- **Webhook dispatcher (dedup):** Outbound webhooks to PagerDuty, Jira, and custom integrations now include an `Idempotency-Key` header derived from the event ID. Retry loops on the receiving side cannot create duplicate incidents or tickets.

- **MCP server (8 tools):** `tombstone_get_flag`, `tombstone_kill_switch`, `tombstone_blast_radius`, `tombstone_list_stale_flags`, `tombstone_create_flag`, `tombstone_search_flags`, `tombstone_generate_cleanup_pr`, `tombstone_openfeature_setup`. Unchanged from v1.0.0 surface; underlying endpoints are now hardened.

- **VS Code extension (5 commands):** Live flag state (enabled/disabled, rollout %) inline above evaluation call sites via CodeLens. Kill-switch and cleanup PR generation without leaving the editor. Unchanged surface from v1.0.0.

- **GitOps sync service:** Gained a resilient HTTP client (retry + circuit breaker) and a `/readyz` endpoint. The v1.0.0 syncer used a plain `http.Client` field with no retries and no readiness reporting.

- **Multi-language SDKs:** TypeScript/Node, React, Edge/Cloudflare Workers, Browser bundle, WASM engine, Python, Java, .NET, Ruby — all present from v1.0.0, all using MurmurHash3 for deterministic bucket assignment and OpenFeature-compatible provider interfaces.

- **Terraform provider:** `tombstone_flag`, `tombstone_flag_environment`, `tombstone_region` resources and `tombstone_flags` data source — unchanged from v1.0.0.

---

### Platform & Deployment

- **Migration infrastructure:** `make migrate` now applies migrations `001` through `012` in sequence using a loop that checks each file's application status before running it. At v1.0.0, `make migrate` applied only `schema.sql`; migrations `002`–`005` had to be applied manually. Fresh deployments on v1.0.0 crashed with `relation scheduled_changes does not exist`.

- **Migration reference:** `services/flag-api/internal/db/migrations/README.md` contains both a descriptive table (each migration's purpose and dependencies) and a complete ordered apply sequence for all 12 migration files.

- **CI coverage and test gating:** `ci.yml` now collects `go test -coverprofile` output for all 7 Go services, runs `pytest` (without the `|| true` masking that was present in v1.0.0 and silently swallowed all test failures), and uploads coverage to Codecov. `pytest-asyncio` is installed as a CI dependency (lines 80–81 of `ci.yml`). Test failures now block merges.

- **Dependency-aware `/readyz` everywhere:** All Go services and the Kubernetes operator expose `/readyz` in addition to `/health`. Kubernetes readiness gates can be configured with confidence that a `200` from `/readyz` means dependencies are reachable, not just that the process started.

- **Governance loops (4 active loops, Slack alerting):** `loop-flag-cleanup.sh` (daily stale flag collector), `loop-incident-response.sh` (post-mortem generator), `loop-rollout-advisor.sh` (ML recommendation collector), `loop-governance.sh` (weekly health score collector). `loop-governance.sh` now sends a Slack notification when the governance health score drops below `0.80` or stale flag count exceeds `50`.

- **Deployment targets documented:** Oracle Cloud free tier (ARM, `cloud-init.yml` + `docker-compose.prod.yml` in `infra/oracle/`), Northflank Sandbox (three JSON service specs in `infra/northflank/`), Helm/Kubernetes (multi-region via `values-region-primary.yaml` / `values-region-secondary.yaml`), Terraform provider. The `infra/oracle/` README was absent at v1.0.0.

---

## What No Other OSS Flag Platform Has

These capabilities either do not exist in LaunchDarkly, Unleash, Flipt, or Flagsmith at any tier, or exist only in paid enterprise offerings:

1. **Automatic production rollback triggered by error rate.** Not UI-initiated, not human-in-the-loop. The evaluator's rollback executor monitors error rates against configurable thresholds and calls the kill-switch API automatically. The circuit breaker on the resilient HTTP client means the rollback attempt survives transient network partitions.

2. **Causal incident correlation ("What Changed?").** `GET /api/v1/incidents/{id}/correlation` returns flags that changed in the preceding 30-minute window, ranked by exponential decay score, with rollback URLs. Mean time to "which flag did this?" is measured in seconds, not war-room minutes.

3. **ML-driven rollout with contextual bandit.** LinUCB does not advance rollout on a fixed schedule. It learns which context conditions (time-of-day, error rate, traffic volume) predict safe advancement and holds back during bad windows. This is qualitatively different from percentage-based A/B rollout.

4. **Knight Capital tombstoning.** The `flag_tombstones` table, the `check_tombstone()` function, and the `enforce_tombstone` database trigger make it structurally impossible to reuse a flag key after it has been tombstoned. The 2012 Knight Capital incident ($440M in 45 minutes) was caused by exactly this class of accidental key reuse.

5. **Merkle-linked tamper-evident audit chain with Rekor.** The internal chain catches database-level tampering. The Rekor submission anchors each event in a public transparency log. Together they satisfy SOC 2 CC7.2 without a third-party audit tool.

6. **Redis Streams dead-letter queue for poison message resilience.** The PEL-based approach means malformed messages do not block a stream partition and do not silently disappear. Ops engineers can inspect, understand, and replay from a documented API endpoint.

7. **Adversarial pre-release validation.** v1.2.1 underwent four independent adversarial validation passes before release: PR #74 E2E validation (7 sub-checks), full resilience/idempotency/Merkle/DLQ validation (9 sub-checks), deployment readiness validation (6 sub-checks), and v1.1.0 feature regression check (6 sub-checks). All 28 sub-checks returned PASS. Release notes from other OSS projects do not typically include a validation manifest with specific line-number evidence.

---

## Upgrade Guide: v1.0.0 → v1.2.1

### Breaking changes

None. All API endpoints are backward-compatible. All SDK interfaces are backward-compatible. The Terraform provider resource schema is unchanged. Existing `docker-compose.yml` files work without modification.

### Migration steps

```bash
# From the repository root
git pull origin main
make migrate
```

`make migrate` applies migrations `002` through `012` in sequence. If you are starting fresh, `make dev` applies all migrations as part of the normal startup sequence. See `services/flag-api/internal/db/migrations/README.md` for the full ordered apply sequence and a description of what each migration introduces.

Key migrations and what they add:
- `002_enterprise.sql` — user_roles, break_glass_tokens, scim_users
- `003_compliance.sql` — user_mfa_log, compliance_snapshots
- `004_sso.sql` — SSO session tables
- `005_scheduled_changes.sql` — scheduled_changes (was missing from v1.0.0 make migrate, causing startup crashes)
- `009` — rekor_log_id, rekor_log_index columns on audit_log
- `010` — idempotency_keys table
- `011` — Redis Streams DLQ state table
- `012` — Actor column on idempotency_keys (SEC-001)

### New environment variables

| Variable | Required | Purpose |
|---|---|---|
| `FLAG_API_TOKEN` | Required for DLQ replay, Datadog inbound, Slack kill-switch | Shared bearer token for inter-service auth. Was present in v1.0.0 RBAC middleware but not enforced on the Datadog/DLQ paths. |
| `SLACK_WEBHOOK_URL` | Optional | Enables governance loop Slack notifications. Not required; loop runs silently without it. |
| `REKOR_SERVER_URL` | Optional | Defaults to `https://rekor.sigstore.dev`. Override for air-gapped deployments. |
| `INTERNAL_CA_CERT`, `INTERNAL_CLIENT_CERT`, `INTERNAL_CLIENT_KEY` | Required if mTLS enabled | PKI chain for mutual TLS inter-service calls. Generate via `tlsutil/certgen.go`. |

### Verification checklist

After `make migrate` and service restart:

- [ ] `GET /readyz` returns `200` on each service (not just `/health`)
- [ ] `POST /api/v1/flags/test-flag/kill` with a valid `Idempotency-Key` header returns `200`; retry with the same key returns the cached `200` without a second audit log entry
- [ ] `GET /api/v1/dlq` returns `200` with `FLAG_API_TOKEN` bearer; returns `401` without
- [ ] Slack `/tombstone kill <flag>` reaches flag-api without HTTP 401 (requires `FLAG_API_TOKEN` set in Slack service env)
- [ ] `make test` passes with no `|| true` masking — check that `ci.yml` test steps do not include it
- [ ] `GET /api/v1/compliance/evidence` returns a signed NDJSON bundle

---

## What's Next (v1.3.0)

Based on open backlog items visible in the current codebase:

- **DLQ auto-replay policy options.** The current DLQ requires manual replay via the API. The planned addition is a configurable policy (retry after N minutes, max M retries, backoff strategy) so that transient unmarshal failures from a rolling deployment recover automatically without operator intervention.

- **Warehouse circuit breaker.** The bounded `ThreadPoolExecutor` with a 30-second timeout prevents a hung warehouse query from blocking the event loop, but the caller still waits up to 30 seconds. A circuit breaker on the warehouse connector that opens after sustained timeout failures would allow the intelligence service to degrade gracefully (falling back to cached metrics) rather than applying the full timeout on every call during a warehouse outage.

- **Dashboard resilience indicators.** The React dashboard currently has no visibility into the state of the circuit breakers, the DLQ depth, or the reconnect backoff state of the gateway. A new resilience panel is planned that surfaces these operational signals without requiring access to the service logs.

- **Rollback failure alerting.** When the evaluator's rollback executor exhausts its three retries and the circuit breaker opens, the failure is currently logged only. A Slack (or PagerDuty) alert on rollback exhaustion is on the roadmap — if the automatic rollback mechanism itself fails during an incident, on-call engineers need to know immediately.

---

*Tombstone is self-hosted, MIT-licensed, and available at the repository linked in the release. The CHANGELOG for this release is at `CHANGELOG.md` under the `[1.2.1]` header, which lists 9 entries: 5 core regression fixes, pyod removal from the intelligence dependencies, audit log actor field fix, CI `|| true` removal, and pytest-asyncio addition.*