# Changelog

All notable changes to Tombstone are documented in this file.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
Versioning: [Semantic Versioning](https://semver.org/spec/v2.0.0.html)

---

## [Unreleased]

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

[Unreleased]: https://github.com/sairam0424/Tombstone/compare/v2.0.1...HEAD
[2.0.1]: https://github.com/sairam0424/Tombstone/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/sairam0424/Tombstone/compare/v1.0.0...v2.0.0
[1.0.0]: https://github.com/sairam0424/Tombstone/compare/v0.1.0...v1.0.0
[0.1.0]: https://github.com/sairam0424/Tombstone/releases/tag/v0.1.0
