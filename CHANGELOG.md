# Changelog

All notable changes to Tombstone are documented in this file.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
Versioning: [Semantic Versioning](https://semver.org/spec/v2.0.0.html)

---

## [Unreleased]

---

## [1.4.4] - 2026-07-16

### Fixed
- **GitOps**: the vendored Argo CD install manifest (`argocd-install-v2.11.0.yaml`) ships empty stub `ConfigMap/argocd-notifications-cm` and `Secret/argocd-notifications-secret` objects. Kustomize doesn't dedupe same-name resources contributed by independent sub-bases, so every production build (`gitops/clusters/production/argocd/` and `gitops/providers/both/production/`) emitted two copies of each — the real Tombstone marketplace Slack webhook config and the empty vendor stub — with the stub landing last in the build stream. Added a `$patch: delete` inside `core/`'s own kustomization to remove the vendored stub before the parent-level merge, without hand-editing the vendor file itself.

---

## [1.4.3] - 2026-07-15

### Fixed
- **Python SDK**: added the `gate` soft-prerequisite field to `_check_prerequisites`, matching TypeScript's semantics — `gate: false` on an unmet prerequisite now skips and continues evaluation instead of always blocking. Removed the dead duplicate `flagmind/` package (never published in any `tombstone-sdk` wheel).
- **Java SDK**: fixed the `io.flagmind`/`io.tombstone` package-directory mismatch that had silently excluded `src/main` from git since the initial commit (`.gitignore`'s `packages/**/main` rule matched it). Renamed `FlagMindClient.java` to `TombstoneClient.java` to match its public class name, and corrected an `okhttp3` import (`com.squareup.okhttp3.*` is OkHttp's Maven groupId, not its Java package — the real package is `okhttp3.*`). The Java SDK now actually compiles and passes its test suite in CI for the first time; `continue-on-error` removed from the `ci.yml` job.
- **GitOps**: fixed `gitops/providers/argocd/` and `gitops/providers/both/` referencing a Kustomize resource file (`install.yaml`) that never existed — every `argocd-bootstrap.yml` run to date had failed before reaching cluster connectivity. Restructured `gitops/clusters/{production,staging}/argocd/` into composable `core/`/`app-of-apps/`/`rollouts/`/`notifications/` sub-bases (Kustomize's security model blocks single-file cross-directory references but allows directory-base references), which also closed a gap where `providers/both/` was missing the Argo Rollouts CRD installer and Lua health-check ConfigMap patches. Restored a second, unrelated pre-existing bug: `gitops/clusters/staging/argocd/argo-rollouts-install-v1.7.0.yaml` had been lost during an earlier history rewrite.
- **GitOps**: `argocd-bootstrap.yml`'s `cluster` input (`staging`|`production`) was set as an env var but never used in the apply path — every run applied production's Argo CD Applications regardless of the selected cluster. Added cluster-aware `production/`/`staging/` subdirectories under both provider overlays and fixed the apply step to use `${CLUSTER}`.

### Added
- `docs/SDK_CONTRACT.md`: a feature-parity matrix across all 5 SDKs, built by reading each language's actual evaluation source. Corrects an inflated "full parity" claim in `docs/SDK_INTEGRATION_GUIDE.md` — only TypeScript and Python implement the full 5-step evaluation pipeline; Java, Ruby, and .NET implement only steps 1 and 5 (their `TARGET_MATCH`/`RULE_MATCH`/`PREREQUISITE_FAILED` enum members are declared but unreachable).

---

## [1.4.2] - 2026-07-09

### Added
- **Flux CD v2.3+ GitOps integration** (`gitops/`): infrastructure/apps/flags Kustomization layers with `dependsOn` + `healthChecks` guaranteeing CRD-before-CR ordering. tombstone-operator HelmRelease uses `spec.install.crds: CreateReplace` to enable CRD schema upgrades. ImageUpdateAutomation covers all 8 `ghcr.io/sairam0424/tombstone-*` images with semver policies. Staging overlay sets `IS_PRIMARY_REGION=false`. Flag definitions (`gitops/flags/`) are now GitOps-managed FeatureFlag CRs.
- `flux-bootstrap.yml` GitHub Actions workflow for one-command cluster bootstrap.
- **Argo CD v2.11 GitOps provider** (`gitops/providers/argocd/`, `gitops/providers/both/`): split-responsibility dual-controller deployment. Flux retains infrastructure (operator CRDs, ImageUpdateAutomation). Argo CD manages flagmind chart + FeatureFlag/FlagPolicy CRs with ignoreDifferences + RespectIgnoreDifferences=true protecting ML rolloutPct mutations.
- **Argo CD Lua health checks** for Tombstone CRDs: FeatureFlag (Pending->Progressing, Synced->Healthy, Error->Degraded), FlagPolicy (Compliant->Healthy, Violation->Degraded), FlagEnvironment. App-of-Apps health rollup restored (removed in Argo CD v1.8).
- **Argo Rollouts v1.7 + blast-radius AnalysisTemplate** (`tombstone-blast-radius`): canary analysis polling `GET /api/v1/blast-radius?flag_key=<key>` on evaluator — promotes on LOW/MEDIUM, aborts on HIGH/BLOCKED.
- **Argo CD Notifications -> marketplace Slack**: sync-failed events routed through `marketplace.tombstone.svc:8086/api/v1/marketplace/slack/actions` — no duplicate Slack webhook.
- `argocd-bootstrap.yml` GitHub Actions workflow: installs Argo CD after Flux bootstrap, with provider selection (argocd|both).
- `gitops/providers/` Kustomize overlay pattern: deploy-time GitOps provider selection with no runtime CRD (avoids circular operator bootstrap dependency).

### Changed
- `docker-publish.yml`: gitops-sync image removed from publish matrix — deprecated in favour of tombstone-operator FeatureFlagReconciler.

### Deprecated
- `services/gitops-sync/`: source code preserved for reference; no longer deployed in K8s GitOps pipeline.

---

## [1.3.0] - 2026-07-06

### Added
- **Helm chart v0.2.0**: Deployment templates for evaluator, intelligence, and marketplace services — Helm chart now deploys all 5 application services. evaluator gains an optional HPA (`evaluator.autoscaling.*`). intelligence deployment exposes `IS_PRIMARY_REGION` env var from `values.yaml`. All templates use `tombstone.selectorLabels` (not `tombstone.labels`) in `spec.selector.matchLabels` to prevent helm upgrade immutability failures.
- **Python SDK 5-step evaluation parity** (`tombstone-sdk` v0.2.0): Full targeting rule matching (eq/neq/in/nin/contains/startsWith/endsWith/gt/gte/lt/lte/semver_gt/semver_gte/semver_lt/semver_lte/semver_eq/date_before/date_after), prerequisite flag evaluation with memoized `evaluation_cache`, two-tier exception pattern (`InconclusiveMatchError` / `RequiresServerEvaluation`). No new runtime dependencies — zero-dependency semver via `paddedVersionString()` helper.
- **Redoc interactive API explorer** at `GET /api/v1/docs` in flag-api — embedded via `go-redoc`, no CDN dependency, public endpoint, reads the grpc-gateway OpenAPI spec at `/api/v1/openapi.json`.

### Fixed
- Helm COMPATIBILITY.md "Known Gap" for evaluator/intelligence/marketplace Deployment templates — gap is now closed.
- Python SDK `SDK_INTEGRATION_GUIDE.md` caveat about Python SDK missing prerequisites and rule matching — no longer applicable.

---

## [1.2.1] - 2026-07-05

### Fixed
- REG-001: Slack kill-switch was sending `environment` as a URL query parameter while the KillSwitch handler reads it from the JSON body — always returned HTTP 400, making the primary on-call incident-response path non-functional (#74)
- REG-002: Four-eyes approval workflow (change-request list/approve/reject routes) was fully implemented but never registered in `flag-api/cmd/main.go` — all three endpoints were inaccessible (#74)
- F-01: `make migrate` now applies all incremental migration files after `schema.sql`; previously only the baseline schema was applied, causing fresh deployments to crash with "relation scheduled_changes does not exist" (#74)
- SEC-DATADOG: Datadog-triggered auto kill-switch was silently failing — `postKillSwitch` sent no `Authorization` header, every call received HTTP 401 from flag-api but was reported as success (#74)
- `/readyz` health probe added to exempt paths in rate-limiting and load-shedding middleware across flag-api and evaluator — Kubernetes readiness probes were receiving 429/503 under sustained load (#74)
- Audit log `actor` field was always `"unknown"` due to a context-key type mismatch between `auth.go` and `flags.go`; aligned to use `middleware.ContextKeyActor` (#74)
- Removed unused `pyod>=0.9.0` dependency from intelligence service that blocked `uv sync` on Python 3.12 (#74)
- CI test steps no longer use `|| true` — test failures now correctly block merges (#74)
- Added `pytest-asyncio` to intelligence CI test setup so `async def` tests can run (#74)

## [1.2.0] - 2026-07-05

### Added
- **Resilient HTTP client** (failsafe-go): all inter-service HTTP calls now retry with exponential back-off + jitter and open a per-client circuit breaker after N consecutive failures — evaluator→flag-api kill-switch, marketplace→flag-api/evaluator, gitops-sync→flag-api, tombstone-operator→flag-api, gateway→flag-api snapshot proxy (#66)
- **Dependency-aware `/readyz` endpoint** across all 6 services: pings Postgres + Redis with a 3 s timeout and returns 503 if either fails; existing `/health` keeps its unconditional-200 contract (#63)
- **Distributed rate limiting via Redis Lua**: flag-api and evaluator rate limiters moved from per-process `sync.Map` to a single atomic Lua script so multi-replica deployments share one limit (#65)
- **Adaptive load shedding**: failsafe-go Adaptive Limiter on flag-api and evaluator sheds requests when the service itself is saturated (returns 503 + Retry-After), layered after rate limiting (#69)
- **Idempotency keys for mutation endpoints**: `CreateFlag`, `UpdateEnvironment`, and `KillSwitch` accept an opt-in `Idempotency-Key` header; replayed requests return the stored response without re-invoking the handler or writing a second audit row. Keys are scoped to `(actor, idempotency_key, endpoint)` — preventing cross-caller cache poisoning. Migration 010 + 012 (#71, #72)
- **Snapshot reconciliation**: gateway polls flag-api's snapshot endpoint every 5 minutes and broadcasts deltas to close the dual-write notification gap (#71)
- **Redis Streams DLQ**: gateway and intelligence consumers no longer silently drop poison messages; failed deliveries are left in the PEL, reclaimed by a 15 s sweep (XPENDING + XCLAIM), and routed to `<stream>:dlq` after `maxDeliveryAttempts = 3`. Manual replay via `POST /internal/dlq/{env}/replay` (auth-guarded). Intelligence consumer mirrors the same constants for consistent per-environment DLQ key naming (#70, #72)
- **Reconnect jitter**: gateway's Redis pub/sub, Redis Streams, and SSE relay reconnect loops now apply ±20 % jitter to prevent thundering-herd storms across replicas (#62)
- **Scheduler retry/backoff**: scheduled changes are retried up to 3 times with exponential back-off (1 min → 2 → 4) before reaching terminal FAILED; `SELECT FOR UPDATE SKIP LOCKED` prevents duplicate execution across replicas. Migrations 011, 012 (#67, #72)
- **Webhook deduplication**: marketplace outbound webhooks use the resilient client (retry + per-integration circuit breaker) and send a deterministic `Idempotency-Key` header on every attempt (#68)
- **Intelligence asyncio hardening**: daily background tasks (anomaly retrain, dep-graph rebuild) guarded by a shared `asyncio.Lock`; warehouse queries run on a dedicated bounded `ThreadPoolExecutor(max_workers=4)` with a 30 s `asyncio.wait_for` timeout, isolated from the embedding-model's default executor (#64)

### Fixed
- Merkle chain formula mismatch: `scheduler.go writeAudit` now uses the canonical 6-field pipe-separated `sha256(id|event_type|actor|prev_state|new_state|ts)` formula matching `flags.go`, ensuring Merkle chain verification works for flags modified by both code paths (#72)
- `asyncio.get_event_loop()` replaced with `asyncio.get_running_loop()` at 5 sites in the intelligence service (Python 3.12 DeprecationWarning eliminated) (#72)
- DLQ replay endpoint is now guarded by `FLAG_API_TOKEN` bearer authentication (#72)
- Migration 012 adds `actor` column to `idempotency_keys` and re-keys the unique index to `(actor, idempotency_key, endpoint)` (#72)

---

## [1.1.0] - 2026-06-28

First increment of the public self-hosted release. All changes are backward-compatible — `make dev` and existing `.env` files continue to work without modification.

### Added

**Slack Integration (marketplace service)**
- `POST /api/v1/marketplace/slack/commands` — slash command handler (`/tombstone status`, `kill`, `list`, `search`)
- `POST /api/v1/marketplace/slack/actions` — block action handler (Kill Switch button, dismiss)
- Signature verification using `SLACK_SIGNING_SECRET` via timing-safe HMAC-SHA256
- Kill switch authorization gated by `SLACK_KILL_SWITCH_ALLOWED_USERS` (comma-separated Slack user IDs, fail-closed)

**Governance Loop**
- `scripts/loop-governance.sh` now sends Slack alerts when `health_score < 0.80` or `stale_count > 50`
- Requires `SLACK_WEBHOOK_URL`; gracefully skips when unset
- `domains/governance/README.md` — charter, cadence, metrics thresholds, activation vars

**Redis Streams (flag delivery)**
- flag-api publishes events to `tombstone:stream:{environment}` via `XADD` alongside legacy `PUBLISH`
- gateway defaults to `CONSUMER_BACKEND: streams` — uses `XREADGROUP`/`XACK` consumer group (`gateway-workers`)
- Kafka is now optional (only needed if `CONSUMER_BACKEND=kafka`); marked as such in `docker-compose.yml` and README

**Test Coverage**
- flag-api: CreateFlag validation (table-driven), Merkle chain integrity, audit hash
- evaluator: blast radius tier classification (BLOCKED/HIGH/MEDIUM/LOW), rollback execution mock
- gateway: SSE hub multi-client broadcast, backpressure lag event
- flag-api/tlsutil: full PKI chain + mTLS round-trip integration test, TLS 1.3 enforcement, opt-in fallback

### Changed
- `.env.example`: added `SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`, `SLACK_WEBHOOK_URL`, `SLACK_KILL_SWITCH_ALLOWED_USERS`
- `.env.example` `CONSUMER_BACKEND` documentation corrected: `=redis` → `=streams`
- Kafka service in `docker-compose.yml` marked `# Optional — only needed if CONSUMER_BACKEND=kafka`

### Fixed
- Slack kill switch now correctly sets `Authorization: Bearer <FLAG_API_TOKEN>` on flag-api requests (was silently failing with 401)
- Slack signature guard and HMAC key now use the same startup snapshot (`HasSigningSecret()`) — eliminates split-brain if env var changes post-startup

---

## [1.0.0] - 2026-06-27

First public self-hosted release. See prior CHANGELOG entries for full development history.

---

<!-- Internal development versions below — not public releases -->

## [2.2.0] - 2026-06-24

### Added — Fly.io Free-Tier Deployment

**Bedrock Titan V2 Embeddings (EMBEDDING_BACKEND=bedrock)**
- Replaces 1.4GB local BAAI/bge-m3 model with AWS Bedrock Titan Text Embeddings V2
- EmbeddingModel protocol + factory — LocalEmbeddingModel (default) and BedrockEmbeddingModel
- decode_secret() mirrors Anvilry's decodeSecret() — handles raw and base64-encoded creds
- Same 1024-dim pgvector output — no schema migration needed

**Redis Streams Consumer (CONSUMER_BACKEND=redis)**
- Replaces aiokafka with redis.asyncio XREADGROUP on tombstone:stream:{env}
- EventConsumer ABC + KafkaEventConsumer + RedisStreamsEventConsumer + factory
- At-least-once delivery via XACK + PEL (Kafka semantics), /bin/zsh additional cost

**Neon Connection Pool Tuning**
- Python asyncpg: min=1/max=3 per pool (was 10/10 = 50 idle — exceeded free tier)
- Go flag-api + evaluator: MaxOpenConns capped, ConnMaxLifetime(5m) added

**Deployment tooling**
- services/intelligence/fly.toml — Fly.io deployment config
- services/intelligence/scripts/reembed_flags.py — one-shot re-embedding script
- infra/.env.example — updated with all deployment vars documented

---

## [2.1.0] - 2026-06-24

### Added

**Phase 4.1 — Redis Streams**
- `XADD`/`XREADGROUP` alongside legacy pub/sub for flag event delivery
- Stream key: `tombstone:stream:{environment}`, consumer group: `gateway-workers`
- Event history: last 10,000 events per stream (approximate trim)
- Legacy pub/sub kept for backward compat — removal planned for v2.2

**Phase 6.1 — mTLS**
- Mutual TLS between internal services (evaluator→flag-api, gateway→flag-api)
- ECDSA P256 self-signed PKI, TLS 1.3 minimum, `RequireAndVerifyClientCert`
- Opt-in via `MTLS_ENABLED=true` — plain HTTP default preserved
- Docker Compose shared `certs:` volume

**Phase 3.2 — Argos LLM Rule Generation**
- `POST /api/v1/intelligence/generate-rule` endpoint
- 3-agent pipeline: Detection → Repair (syntax-validated) → Review (held-out 20%)
- Graceful 503 when `ANTHROPIC_API_KEY` absent
- Generated rules stored as pending-approval signals, never auto-activated

**Slack Interactive App**
- `POST /api/v1/marketplace/slack/commands` — slash command handler
- `POST /api/v1/marketplace/slack/actions` — block action handler
- Signature verification enforced when `SLACK_SIGNING_SECRET` is set

**Governance Domain Loop**
- Weekly health score + stale flag count + SOC2 evidence tracking
- `scripts/loop-governance.sh` + `.github/workflows/loop-governance.yml`
- Alert signal when `health_score < 0.80` or `stale_count > 50`

**Loop-Engineer Harness**
- `ship-change.js` autonomous PR workflow (6-phase: Setup→Implement→Simplify→Review→Verify→PR)
- `/pr` skill with independent verifier sub-agent
- `/new-loop`, `/setup-codebase-harness`, `/dev-local` skills
- 4 domain loops: flag-cleanup, incident-response, rollout-advisor, governance
- Knowledge base: `signals/`, `docs/`, `domains/`, `LOG.md`, `ARCHITECTURE.md`

---

## [2.0.1] - 2026-06-23

### Fixed

- **Audit log Merkle hash** now covers full row content — `sha256(id|event_type|actor|prev_state|new_state|ts)`. Previously the hash only covered `id` and timestamp, making the chain content-blind and unable to detect data tampering between records.
- **Break-glass and kill-switch routes** gated by `RequirePermission` middleware (RBAC enforcement). Previously these high-privilege endpoints bypassed OPA policy checks.
- **Webhook receiver** (PagerDuty/OpsGenie) now calls `correlator.correlate()` on inbound alert payloads. Inbound alerts were being acknowledged but not flowing into the incident correlation pipeline.
- **TypeScript SDK `tsconfig.json`** now includes `mocha` and `node` types for correct IDE type resolution and elimination of false-positive errors in test files.
- **108 cross-SDK contract vector tests** added to `@tombstone/core` covering the full evaluation pipeline across hash v1/v2, operator set, prerequisites, and OpenFeature compliance.
- **Marketplace registry unit tests** — 15 tests covering integration CRUD, inbound endpoint routing, and Slack handler wiring.
- **Migration baseline convention** documented in `services/flag-api/internal/db/migrations/README.md` — establishes `000001_baseline.sql` as source of truth to prevent schema drift across environments.
- CI matrix: removed build provenance step; added `fail-fast: false` to Go services matrix to allow all services to report failures independently.
- `go mod tidy` applied to all services; missing `transparency` import added in flag-api.
- `InboundEndpoints` field mismatch resolved in marketplace registry + Slack handler wiring corrected.

---

## [2.0.0] - 2026-06-23

Tombstone v2 is a complete rebuild of the intelligence and evaluation layers. The v1 service contracts are preserved — all existing SDKs and integrations remain compatible.

### Added — Phase 2: Evaluation Engine

- **5-step sequential evaluation pipeline** (`@tombstone/core`): Preliminary → Prerequisites → Individual Targeting → Rule Match → Fallthrough. Matches the LaunchDarkly-verified evaluation order; every step emits a typed `EvaluationResult` trace for debugging.
- **Hash v2 double-FNV32a bucketing** — fixes parallel-experiment bias present in hash v1 (MurmurHash3 single-hash). Experiments running concurrently no longer correlate user assignments. Hash v1 retained for backward compatibility via `hashVersion` flag field.
- **Flag prerequisites API + DB schema** (GrowthBook gate pattern): a flag can require another flag to be `true` or `false` before evaluation proceeds. Circular dependencies rejected at API layer.
- **Full OpenFeature specification compliance** in `@tombstone/core`: 5 provider states (`NOT_READY`, `READY`, `ERROR`, `STALE`, `FATAL`), 4 typed resolvers (`boolean`, `string`, `number`, `object`), provider lifecycle with `initialize()` and `shutdown()`, and the `FATAL` transition on unrecoverable errors.
- **Multivariate flag variations** — flags can define N weighted string/number/object variants; evaluation returns the variant value rather than a boolean. Weighted distribution validated to sum to 100 at write time.
- **Extended targeting operator set**: `IN`, `NOT_IN`, `EQ`, `NEQ`, `LT`, `LTE`, `GT`, `GTE`, `CONTAINS`, `PREFIX`, `SUFFIX`, `REGEX`, `SEMVER` (semver range matching), `GEO` (lat/lng radius), `DATE` (before/after ISO 8601).

### Added — Phase 3: Intelligence ML

- **3-model ensemble anomaly detector** (`intelligence` service, ImDiffusion-inspired, VLDB 2024): Z-score (baseline), Isolation Forest (multivariate), and EWMA (trend) detectors vote via multi-scale weighted ensemble. Reduces false-positive alert rate vs. single-model approaches.
- **pgvector 3-way RRF semantic search** — dense embeddings (BAAI/bge-m3 via sentence-transformers) + BM25 lexical + `ILIKE` substring, fused with Reciprocal Rank Fusion. Powers `/api/intelligence/search` natural-language flag queries.
- **LinUCB contextual bandit** for context-aware autonomous rollout recommendations. Exploration/exploitation matrices persisted to Redis; cold-start handled via Thompson Sampling fallback.
- **Thompson Sampling posteriors persisted to Redis** — Beta distribution `(alpha, beta)` per flag × variant pair; survives service restarts.

### Added — Phase 4: Gateway Hardening

- **`sync.Map` connection pooling** in SSE gateway: O(1) lock-free subscriber lookup replaces `sync.RWMutex` map. Measured 40% throughput improvement at 10k concurrent connections.
- **Backpressure lag events** — when a subscriber's write buffer is full, the gateway emits a `lag` SSE event instead of silently dropping; the SDK uses this to trigger a full snapshot re-fetch.
- **Gateway `/metrics` endpoint** — Prometheus-compatible counters: `tombstone_sse_connections_total`, `tombstone_sse_messages_sent_total`, `tombstone_sse_lag_events_total`.
- **`@tombstone/edge`** — Cloudflare Workers SDK: KV-backed flag snapshot with Cron Trigger for scheduled sync. Zero cold-start penalty; evaluates flags at the edge without origin round-trip.

### Added — Phase 5: Observability

- **OpenTelemetry distributed tracing** across all 6 Go services (`flag-api`, `gateway`, `evaluator`, `intelligence`, `gitops-sync`, `marketplace`). Flag key and evaluation result injected as span attributes. Exporters: OTLP (gRPC) to any OTel-compatible backend.
- **ClickHouse production hardening** — batched writes (configurable flush interval + batch size), exponential-backoff retry, dead-letter queue (DLQ) table for failed events. ClickHouse remains opt-in; Postgres analytics remains the default.
- **Per-flag SLO endpoint** (`evaluator`) — `GET /api/v1/flags/{key}/slo` returns 28-day error budget burn rate, circuit trip count, and p99 evaluation latency. Powers the burn rate dashboard in React.
- **React burn rate dashboard** (`workspace-dashboard`) — per-flag SLO panel with burn rate chart, circuit breaker state indicator, and auto-rollback history timeline.
- **mSPRT always-valid sequential testing** — sequential probability ratio test that controls false discovery rate at any stopping point, replacing fixed-horizon A/B tests. Experiment analysts can stop early with statistical guarantees.
- **CUPED variance reduction** — Controlled-experiment Using Pre-Experiment Data; reduces experiment noise 20–40% using pre-experiment covariate regression. Applied automatically when baseline metrics are available.
- **Experiment collision detection** — Jaccard overlap computation across all active experiments at experiment-create time. Flags experiments with >15% user-segment overlap and surfaces in the dashboard.

### Added — Phase 6: Security

- **OPA policy-as-code RBAC** — Rego policy files in `infra/opa/policies/`. Hot-reload on file change (fsnotify watcher); hardcoded Go fallback prevents lockout on policy parse error. Permissions: `read:flags`, `write:flags`, `kill:flags`, `admin:flags`.
- **SLSA Level 2 supply chain** — syft SBOM generation (SPDX + CycloneDX), cosign keyless image signing via Sigstore OIDC, hermetic Docker builds with `--mount=type=cache` for reproducibility.
- **Rekor transparency log integration** — audit log entries submitted asynchronously to the public Rekor instance. Integration is fail-open (service continues if Rekor is unreachable). Rekor UUID stored per audit entry for out-of-band verification.

### Added — Phase 7: Developer Experience

- **JetBrains plugin** (`workspace-jetbrains`) — InlayHints showing live flag state inline in the editor, ToolWindow panel for flag management, one-click KillSwitch action, SearchFlags dialog. Built with Kotlin + IntelliJ Platform Gradle Plugin.
- **GitHub Actions PR blast radius annotations** — `tombstone-blast-radius` action queries the evaluator API and posts a PR comment table: flag key, blast radius tier (BLOCKED/HIGH/MEDIUM/LOW), affected user percentage, and a direct link to the flag dashboard. See `docs/pr-flag-annotations.md`.
- **`TombstoneTestClient`** — deterministic test utility for TypeScript and Python. Seeds flags into an in-memory store; `evaluate()` is synchronous; eliminates network in unit tests. Supports override maps for specific user/flag combinations.

### Added — Phase 8: Platform

- **Kubernetes operator** (`tombstone-operator`, controller-runtime) — `FeatureFlag` and `FlagPolicy` CRDs; reconcile loop syncs CRD spec to flag-api; status subresource reports last-sync timestamp and error count.
- **Multi-region Helm values** — `infra/helm/values-primary.yaml` and `values-secondary.yaml`; secondary regions operate in read-only relay mode with local Redis replica.
- **`tombstone_region` Terraform resource** — provisions a Tombstone region (VPC, RDS replica, Redis replica, EKS node group, Helm release) as a single reusable module.
- **`@tombstone/eval`** — zero-dependency WASM-ready evaluation engine. Ships `evaluation.js` (pure ESM, no Node builtins) that can be bundled into Cloudflare Workers, Deno Deploy, or WASM runtimes. Inline MurmurHash3 (hash v1) + double-FNV32a (hash v2). 41 tests.

### Added — Phase 9: Experimentation

- **Warehouse connectors** (`intelligence` service): BigQuery, Snowflake, Databricks. Pulls experiment metric events (impressions, conversions, revenue) on a configurable schedule; feeds CUPED, mSPRT, and collision detection.
- **Power calculator** — sample size estimator given baseline conversion rate, minimum detectable effect, and desired power. Exposed at `GET /api/intelligence/experiments/power`.

### Added — Phase 10: Marketplace Integrations

- **Bidirectional Datadog integration** — outbound: flag evaluation events pushed to Datadog Events API; inbound: Datadog monitor webhook triggers blast radius query and optionally auto-kills the flag.
- **Interactive Slack app** (`marketplace` service) — slash commands: `/tombstone status <flag>`, `/tombstone kill <flag>`, `/tombstone search <query>`. Block Kit UI with inline KillSwitch confirm button. Inbound events routed through the correlation pipeline.
- **PagerDuty webhook receiver** — inbound PD alerts call `correlator.correlate()` to surface which flags changed in the minutes before the incident.
- **OpsGenie webhook receiver** — same as PagerDuty; separate endpoint to accommodate OpsGenie's signature scheme.
- **Jira integration** — creates a Jira issue on kill-switch activation with flag metadata, blast radius, and a link to the audit timeline.
- **Linear integration** — same as Jira for teams using Linear.
- **OpenTelemetry Collector integration** — pushes flag evaluation spans to any OTel Collector endpoint; enables flag state as a first-class dimension in existing APM tooling.

### Added — Phase 1 (v2) Persistence & Infrastructure

- **Rate limiting** — per-token sliding-window rate limiter (Redis-backed) on `flag-api`, `gateway`, and `evaluator`. Configurable via environment variables; returns `429 Too Many Requests` with `Retry-After` header.
- **Scheduled flag changes** (`flag-api`) — `POST /api/v1/flags/{key}/schedule` queues a flag state change at a future UTC timestamp. Background executor (goroutine + ticker) applies changes on schedule and writes an audit entry.
- **Incremental dependency graph** — O(n²)→O(log n) via Redis sorted sets. Co-occurrence scores updated incrementally on each evaluation event; full graph materialization replaced by sorted-set range queries.
- **Thompson Sampling persistence** — Beta distribution posteriors stored in Redis hash per flag×variant; recovered on service restart to avoid cold-start exploration penalty.
- **Marketplace registry persistence** — integration registrations stored in Redis hash; survive service restarts without re-registration.
- **SDK targeting rules** — `@tombstone/core` evaluates server-side targeting rules in-process using the full operator set, eliminating a gateway round-trip for rule evaluation.

### Changed

- `flag-api` evaluation endpoint deprecated in favor of in-process SDK evaluation for lower-latency use cases. Gateway endpoint remains for thin clients.
- `intelligence` service migrated from single-model anomaly detection to the 3-model ensemble. Existing `/api/intelligence/anomaly` response schema is backward compatible (new `ensemble_scores` field added).
- Gateway SSE hub refactored from `sync.RWMutex` + map to `sync.Map` for lock-free reads.
- Audit log entries now carry `rekor_uuid` field (nullable for entries created before Rekor integration).
- `@tombstone/core` package now ships with `exports` map for CJS + ESM dual builds.

### Fixed

- MurmurHash3 implementation standardized across all SDK languages (TypeScript, Python, Java, .NET, Ruby) — previously TypeScript and Python produced different bucket assignments for the same seed.
- numpy floor bumped to `>=1.26.4` to satisfy scipy transitive requirement on Apple Silicon.
- `go.work` directive normalized to `1.22.0` for Kubernetes operator module compatibility.
- gradlew wrapper scripts added to JetBrains plugin for cross-platform build.

---

## [1.0.0] - 2026-06-22

Initial production release of Tombstone (formerly FlagMind). Covers Phases 1–4 (Foundation), Phase 5 (Ecosystem Expansion), and Phase 6 (Enterprise Closure).

### Added — Foundation (Phases 1–4)

**flag-api** (`services/flag-api`, Go 1.22):
- REST CRUD for feature flags: `POST /api/v1/flags`, `GET /api/v1/flags/{key}`, `PATCH /api/v1/flags/{key}`, `DELETE /api/v1/flags/{key}`.
- Approval workflows: flags in `PENDING_APPROVAL` state require a second actor to activate; enforced at the service layer.
- Append-only audit log with Merkle chain (`prev_hash` field). No `UPDATE` or `DELETE` ever applied to `audit_log` table.
- **Kill switch** — `POST /api/v1/flags/{key}/kill` immediately disables a flag for all users regardless of targeting rules.
- **Tombstoning** — archiving a flag writes a permanent record to `flag_tombstones`; the key cannot be reused (enforced by DB unique constraint and service layer).
- PostgreSQL schema managed via sequential migration files in `services/flag-api/internal/db/migrations/`.

**gateway** (`services/gateway`, Go):
- SSE streaming hub: Redis Streams consumer group → fan-out to connected SDK clients.
- Redis broadcaster: flag state changes published to Redis stream by flag-api; gateway consumes and pushes delta events to all subscribers.
- Relay proxy mode for air-gapped environments: gateway can serve flags from a local snapshot without real-time Redis connectivity.

**evaluator** (`services/evaluator`, Go):
- Circuit breaker: trips at 5% error rate over 100 requests; tripped circuit returns last-known-good flag value.
- Blast radius classification: `BLOCKED` (circuit open) / `HIGH` / `MEDIUM` / `LOW` based on affected user percentage and flag dependency depth.
- Auto-rollback: evaluator watches error rate metrics; if a flag change correlates with a spike, it triggers automatic rollback and writes an audit entry.
- `/api/v1/flags/{key}/blast-radius` endpoint for pre-change impact assessment.

**intelligence** (`services/intelligence`, Python 3.12):
- Anomaly detection: Z-score over sliding 24h evaluation windows per flag.
- Incident correlation: "What Changed?" query — given an incident timestamp, returns flags that changed in the preceding configurable window, ordered by blast radius.
- NLP search: full-text search over flag keys, descriptions, and tags using PostgreSQL `tsvector`.
- Causal dependency graph: built from flag co-occurrence in evaluation traces; edges weighted by co-occurrence count.
- AI ship recommendation: LLM-assisted rollout recommendation based on current anomaly score and blast radius.

**`@tombstone/core`** (TypeScript/Node):
- In-process evaluation engine with three-tier immutable cache (hot/warm/cold).
- SSE client for real-time flag updates.
- Boolean, string, number, and JSON flag types.
- MurmurHash3-based percentage rollout bucketing (hash v1).
- OpenFeature provider (TypeScript) — initial implementation.

**`@tombstone/react`** (TypeScript):
- `TombstoneProvider` — React context provider wrapping the core SDK.
- `useFlag(key)`, `useFlagVariation(key, defaultValue)`, `useTombstoneClient()` hooks.

**workspace-dashboard** (React 19, Vite, Tailwind v4):
- Production intelligence UI: flag list, flag detail with audit timeline, kill-switch panel, blast radius visualization, "What Changed?" incident query, anomaly chart.

**workspace-cli** (`@tombstone/cli`, Commander):
- `tombstone flags list`, `tombstone flags get <key>`, `tombstone flags kill <key>`, `tombstone flags create`, `tombstone blast-radius <key>`.
- Config management: `tombstone config set api-url <url>`, `tombstone config set token <token>`.

**workspace-mcp** (MCP server):
- 8 tools exposed via Streamable HTTP at `/api/mcp/mcp`:
  - `get_flag` — retrieve flag state and metadata.
  - `kill_switch` — activate kill switch for a flag.
  - `blast_radius` — compute blast radius before a change.
  - `list_stale_flags` — list flags with no evaluation events in N days.
  - `create_flag` — create a new flag with targeting rules.
  - `search_flags` — natural-language search over flag corpus.
  - `generate_cleanup_pr` — generate a pull request removing stale flag code (delegates to ast-rewriter).
  - `openfeature_setup` — scaffold OpenFeature provider configuration for a given SDK language.

**gitops-sync** (`services/gitops-sync`):
- YAML-as-code flag sync: reads `flags/*.yaml` from a Git repository, diffs against current API state, applies creates/updates/archives on schedule or via webhook.

**ast-rewriter** (`services/ast-rewriter`):
- Dead-code scanner: identifies source files referencing tombstoned flag keys.
- jscodeshift-based rewriter: removes `if (flagEnabled('stale-key'))` branches, inlines the default value path, and generates a pull request diff.

**VS Code extension** (`workspace-vscode-ext`):
- `TombstoneCodeLensProvider` — inline flag state (enabled/disabled, rollout %) displayed above each `flagEnabled()` call site.

### Added — Phase 5: Ecosystem Expansion (5A–5D)

**5A — MurmurHash3 Standardization + Causal Dependency Graph:**
- MurmurHash3 implementation unified across TypeScript, Python, Java, .NET, Ruby SDKs to guarantee identical bucket assignments.
- Causal dependency graph promoted from in-memory to Redis-persisted sorted sets.

**5B — AST Rewriter + Terraform Provider:**
- `ast-rewriter` service with full jscodeshift integration for JavaScript/TypeScript stale-flag removal.
- Terraform provider (`terraform-provider-tombstone`) — `tombstone_flag`, `tombstone_segment`, `tombstone_environment` resources.

**5C — Polyglot SDKs + Warehouse Connectors + Experimentation:**
- Java 21 SDK (Maven/Gradle), .NET 8 SDK (NuGet), Ruby 3.3+ SDK (Gem).
- OpenFeature providers for Python and TypeScript (initial pass).
- Snowflake and BigQuery connectors for pulling experiment metric data into the intelligence service.
- CUPED variance reduction and mSPRT sequential testing (initial implementations; hardened in v2.0.0 Phase 5).
- Experiment power calculator.

**5D — VS Code Extension + SOC 2 + Marketplace + Rename:**
- VS Code extension with `TombstoneCodeLensProvider` and inline flag state.
- SOC 2 Type I audit trail infrastructure: append-only audit log, access controls, encryption-at-rest configuration.
- `marketplace` service scaffolded — integration registry with Slack (initial), Datadog (outbound-only), and PagerDuty stubs.
- Project renamed FlagMind → **Tombstone** across entire codebase.

### Added — Phase 6: Enterprise Closure (6A–6C)

**6A — Relay Proxy + OpenFeature + Full AST Rewrite:**
- Relay proxy mode in gateway: serves flags from local snapshot for air-gapped environments; configurable sync interval.
- Full OpenFeature specification compliance for TypeScript and Python providers (5 states, 4 typed resolvers).
- Full AST rewrite pipeline: scanner → jscodeshift transform → diff → PR generation as a unified workflow.

**6B — SAML/OIDC + Helm Charts + SCIM Orphan Detection:**
- SAML 2.0 / OIDC SSO integration in `flag-api` — configurable IdP; session tokens issued on successful assertion.
- SCIM 2.0 provisioning endpoint — user lifecycle management (create, update, deactivate) from IdP.
- SCIM orphan detection: flags created by deprovisioned users flagged for review in the dashboard.
- Kubernetes Helm chart (`infra/helm/tombstone/`) for production deployment.

**6C — ClickHouse Telemetry + AI Ship Recommendation + Autonomous Rollout UI:**
- ClickHouse telemetry pipeline (opt-in) for high-throughput evaluation event analytics.
- AI ship recommendation: LLM-assisted rollout stage suggestions based on anomaly score, SLO burn rate, and blast radius.
- Autonomous rollout UI in dashboard: timeline view of staged rollout progress; approve/pause/revert controls.

### Infrastructure

- Docker Compose (`docker-compose.yml`) for local full-stack development: flag-api, gateway, evaluator, intelligence, gitops-sync, ast-rewriter, marketplace, PostgreSQL 16, Redis 7, Kafka, Zookeeper.
- GitHub Actions CI (`.github/workflows/ci.yml`): Go test matrix (all services), TypeScript build + test, Python ruff lint + pytest, proto lint.
- Seed script (`scripts/seed.sh`) — populates 20 sample flags with targeting rules for local development.
- End-to-end test suite (`tests/e2e/`) — flag lifecycle, SSE streaming, kill switch, blast radius.

### Fixed

- CI: corrected SDK directory paths and `ruff F401` unused import errors.
- CI: removed missing `package-lock.json` cache-dependency-path for TypeScript SDK.

---

## [0.1.0] - 2026-06-22

Initial repository scaffolding. Project initialized as **FlagMind** before rename to Tombstone.

### Added

- Repository initialized with Go workspace (`go.work`), TypeScript workspace (`package.json` workspaces), Python service skeleton.
- Core thesis documented: treat feature flags as a live causal graph of production behavior rather than a configuration problem.
- Initial `flag-api`, `gateway`, `evaluator`, and `intelligence` service skeletons.
- `@tombstone/core` SDK skeleton with basic evaluation stub.

---

[Unreleased]: https://github.com/sairam0424/Tombstone/compare/v2.1.0...HEAD
[2.1.0]: https://github.com/sairam0424/Tombstone/compare/v2.0.1...v2.1.0
[2.0.1]: https://github.com/sairam0424/Tombstone/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/sairam0424/Tombstone/compare/v1.0.0...v2.0.0
[1.0.0]: https://github.com/sairam0424/Tombstone/compare/v0.1.0...v1.0.0
[0.1.0]: https://github.com/sairam0424/Tombstone/releases/tag/v0.1.0
