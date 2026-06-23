# Tombstone v2.0.1 — Production Intelligence Layer for Feature Flags at Scale

> You have 5,000 active flags. Something just broke. Which flag did it?

---

## The 3am Call Problem

| Scenario | Without Tombstone | With Tombstone |
|----------|-----------------|---------------|
| P0 alert fires at 3am | Page engineer, grep logs, cross-reference deploys, manually bisect 5k flags | Blast radius view pinpoints the causal flag in seconds |
| Mean time to identify | ~25 minutes | ~90 seconds |
| Rollback action | Edit YAML, open PR, wait for CI, merge, wait for deploy | One button. Circuit breaker auto-triggers at p99 threshold |
| Post-incident question | "Which flag touched this?" | Already answered — audit log with Merkle-linked entries |
| Morning retrospective | Recreate timeline from scattered logs | What-Changed feed shows exact flag state at every minute |

Tombstone eliminates the 3am call by treating feature flags as a **live causal graph of production behavior** — not a configuration file.

---

## Quick Start

```bash
git clone https://github.com/your-org/tombstone.git
cd tombstone
cp .env.example .env
make dev
```

The dashboard opens at http://localhost:3000.

---

## Service Ports

| Service | Port | Description |
|---------|------|-------------|
| gateway | 8080 | SSE streaming to SDKs — Redis Streams consumer groups, sync.Map pooling, backpressure |
| flag-api | 8081 | REST CRUD, approval workflows, Merkle-linked audit log, tombstoning, kill switch, prerequisites, scheduled changes |
| evaluator | 8082 | Circuit breaker (5% error/100 req threshold), blast radius (BLOCKED/HIGH/MEDIUM/LOW), auto-rollback, SLO endpoint |
| intelligence | 8083 | 3-model ensemble anomaly detection, hybrid NLP search (pgvector + lexical), LinUCB bandit, mSPRT, CUPED, warehouse connectors |
| gitops-sync | 8084 | YAML-as-code flag sync — watches repo, applies changes, never deletes |
| ast-rewriter | 8085 | Dead-code scanner + jscodeshift rewrite engine for stale flag cleanup |
| marketplace | 8086 | Integration registry — Slack interactive app, Datadog bidirectional, PagerDuty, OpsGenie, Jira, Linear, OTel |
| tombstone-operator | — | Kubernetes operator (FeatureFlag/FlagPolicy CRDs, controller-runtime) |
| dashboard | 3000 | React 19 management UI |
| PostgreSQL | 5432 | Primary store + pgvector extension |
| Redis | 7 | Pub/sub, Streams, LinUCB matrix persistence |
| Kafka | 9092 | Event bus |

---

## Architecture

```
                         +------------------+
                         |  dashboard :3000 |
                         +--------+---------+
                                  |
          +-----------------------+-----------------------+
          |                       |                       |
+---------+--------+    +---------+--------+    +---------+--------+
|  flag-api :8081  |    |  gateway  :8080  |    | evaluator :8082  |
|  CRUD + approvals|    |  SSE stream      |    | circuit breaker  |
|  audit log       |    |  Redis Streams   |    | blast radius     |
|  tombstoning     |    |  backpressure    |    | auto-rollback    |
|  prerequisites   |    |  snapshot proxy  |    | SLO endpoint     |
+--------+---------+    +--------+---------+    +--------+---------+
         |                       |                       |
         +-----------+-----------+-----------+-----------+
                     |                       |
          +----------+---------+   +---------+----------+
          | intelligence :8083 |   |  gitops-sync :8084  |
          | 3-model ensemble   |   |  YAML -> flags      |
          | pgvector RRF search|   |  PR auto-generate   |
          | LinUCB / mSPRT     |   |  tombstone guard    |
          | warehouse connectors   +--------------------+
          | incident correlation|
          +----------+---------+
                     |
          +----------+----------+----------+
          |          |          |          |
  +-------+---+ +----+----+ +---+----+ +---+--------+
  |ast-rewriter| |marketplace| |  k8s  | |  OTel     |
  |  :8085    | |  :8086   | |operator| |  collector |
  +-----------+ +-----------+ +-------+ +------------+
                     |
         +-----------+-----------+
         |                       |
    +----+------+       +--------+-----+
    | PostgreSQL |       |    Redis 7   |
    | :5432      |       |    :6379     |
    | + pgvector |       | Streams/Sets |
    +----+-------+       +--------------+
         |
    +----+-------+
    |  ClickHouse |  (optional — production analytics)
    +-------------+
```

---

## Key Features

### Evaluation Engine (v2)

The evaluation pipeline is a **5-step sequential chain**, verified against the LaunchDarkly specification:

1. **Preliminary** — flag existence, kill-switch, global kill switch
2. **Prerequisites** — gate flags must resolve to the required variation before continuing (GrowthBook gate pattern)
3. **Individual Targeting** — exact-match rules per user/context key
4. **Rule Match** — ordered targeting rules with extended operator set
5. **Fallthrough** — weighted percentage rollout via hash bucketing

**Hash algorithms:**
- **Hash v1** — MurmurHash3 (legacy, preserved for backward compatibility)
- **Hash v2** — Double-FNV32a, fixes the parallel-experiment bias problem where flags sharing a bucket boundary gave correlated assignments across experiments

**Operator set:**

| Category | Operators |
|----------|-----------|
| Equality | `IN`, `NOT_IN`, `EQ`, `NEQ` |
| Numeric | `LT`, `LTE`, `GT`, `GTE` |
| String | `CONTAINS`, `PREFIX`, `SUFFIX`, `REGEX` |
| Version | `SEMVER` (semver range comparisons) |
| Location | `GEO` (lat/lon radius targeting) |
| Time | `DATE` (before/after/between) |

**Flag types:** Boolean, string, integer, JSON — all support multivariate variations with weighted distribution.

---

### Blast Radius and Circuit Breaker

Before any flag change goes live, the evaluator computes blast radius across four tiers:

| Level | Criteria | Required action |
|-------|----------|----------------|
| BLOCKED | Crosses critical-path SLO boundary | 10-character minimum justification + approval |
| HIGH | >10% of active users affected | Manual confirmation |
| MEDIUM | 1–10% of active users affected | Informational |
| LOW | <1% of active users affected | No gate |

The **circuit breaker** runs a sliding window over per-flag error rates. Threshold: 5% error rate sustained over 100 requests. When tripped it auto-disables the flag and publishes a rollback event to the SSE stream — no human in the loop required.

---

### Anomaly Detection (3-Model Ensemble)

Intelligence service runs three detectors in parallel and combines results via multi-scale voting — the **ImDiffusion** approach from VLDB 2024:

| Model | What it catches |
|-------|----------------|
| Z-score | Sharp, statistically significant deviations from rolling mean |
| Isolation Forest | Unusual patterns in multi-dimensional metric space |
| EWMA | Gradual drift that single-point detectors miss |

Results are fused into a single confidence score. Flags whose evaluation timeline correlates with anomaly onset are surfaced in the What-Changed feed.

---

### Experimentation and Statistics

| Feature | Description |
|---------|-------------|
| **mSPRT sequential testing** | Always-valid p-values — peek at results any time without inflating false positive rate |
| **CUPED variance reduction** | Pre-experiment covariate adjustment reduces variance 20–40%, letting experiments reach significance faster |
| **LinUCB contextual bandit** | Adapts rollout percentages based on contextual features; Redis-persisted weight matrices survive restarts |
| **Thompson Sampling** | Bayesian A/B with automatic winner promotion once minimum observations (50) reached |
| **Experiment collision detection** | Jaccard overlap check flags experiments that share user segments and could confound each other |

---

### Semantic Search (3-Way RRF)

Flag search combines three retrieval signals via Reciprocal Rank Fusion:

1. **Dense vector** — BAAI/bge-m3 embeddings in pgvector (`ivfflat` index)
2. **Lexical** — Full-text `tsvector` search over flag key, name, description
3. **Pattern** — `ILIKE` substring match for exact partial-key lookup

The fused ranking surfaces the most relevant flag whether you know the exact key or only a vague description of what it does.

---

### Audit Log and Tombstoning

Every flag state transition writes an append-only audit log entry with a Merkle-linked hash:

```
sha256(id | event_type | actor | prev_state | new_state | timestamp)
```

The `prev_hash` field chain-links every entry to the one before it. Tampering with any historical record breaks the chain and is immediately detectable.

**Tombstoning** is permanent. When a flag is archived:
- Its key is written to `flag_tombstones` with the reason and actor
- A DB constraint blocks any future `INSERT` reusing that key
- The service layer enforces this independently of the DB constraint
- The flag key is shown as reserved in the UI

This prevents the "silent resurrection" bug where a flag key is reused months later, picking up stale SDK caches and old targeting rules.

---

### Observability

| Layer | Implementation |
|-------|---------------|
| Distributed tracing | OpenTelemetry across all 6 Go services; flag key and variation included in span attributes |
| Metrics | Prometheus exposition format; per-flag SLO dashboard with burn rate and circuit trip history |
| Streaming analytics | ClickHouse (optional) with batched writes, retry queue, and dead-letter queue |
| Structured logs | JSON over stdout; log level configurable per service at runtime |

---

### Security and Compliance

| Feature | Detail |
|---------|--------|
| **OPA policy-as-code RBAC** | Rego files define who can read, write, approve, or kill-switch flags; hot-reload without restart |
| **Kill switch + break-glass** | Both gated by `RequirePermission` middleware; break-glass events are always audit-logged |
| **SLSA Level 2** | syft-generated SBOM on every release; cosign keyless signing via GitHub OIDC |
| **Rekor transparency log** | Every release artifact is logged to Sigstore Rekor; verification is fail-open and async |
| **Merkle audit chain** | Full-row hash: `sha256(id|event_type|actor|prev_state|new_state|ts)` — tamper-evident |

---

### GitOps and Platform

| Feature | Detail |
|---------|--------|
| **GitOps sync** | YAML flag definitions in your repo; sync agent applies changes on push; tombstone guard blocks key reuse |
| **AST rewriter** | Scans codebase for dead flag references, generates jscodeshift codemods to remove them |
| **Kubernetes operator** | `FeatureFlag` and `FlagPolicy` CRDs managed by controller-runtime; multi-region Helm values (primary + secondary) |
| **Terraform provider** | `tombstone_region` resource for infrastructure-as-code flag management |
| **Scheduled changes** | Flags can be scheduled to change variation at a specific UTC timestamp |
| **Approval workflows** | Required reviewers, minimum approvals, time-lock windows |

---

### Integrations (Marketplace :8086)

| Integration | Direction | Capabilities |
|-------------|-----------|-------------|
| **Slack** | Bidirectional (interactive app) | Flag change notifications, kill-switch buttons in alert messages, rollback confirmations |
| **Datadog** | Bidirectional | Flag state as Datadog events; Datadog alert webhooks trigger circuit breaker |
| **PagerDuty** | Outbound | Auto-create incidents on blast radius BLOCKED events |
| **OpsGenie** | Outbound | Alert routing for on-call flag changes |
| **Jira** | Bidirectional | Flag change requests as Jira tickets; ticket resolution unblocks rollout |
| **Linear** | Bidirectional | Flag lifecycle linked to Linear issues |
| **OpenTelemetry** | Outbound | Flag evaluation events as OTel spans |

---

### Warehouse Connectors

Pull experiment results directly from your analytics warehouse — zero raw user rows cross the boundary:

| Warehouse | Transport |
|-----------|-----------|
| BigQuery | Service account + BigQuery Storage Read API |
| Snowflake | Key-pair auth + Snowflake Python Connector |
| Databricks | DBAPI2 via Databricks SQL Connector |

---

### Developer Tooling

| Tool | Description |
|------|-------------|
| **JetBrains plugin** | InlayHints show current flag variation in the IDE; ToolWindow lists active flags; kill-switch button directly in IntelliJ/Rider |
| **GitHub Actions** | PR annotations show blast radius for any flag touched in a diff — reviewers see impact before merge |
| **@tombstone/cli** | Flag CRUD, rollout management, stale-flag reports from the terminal |

---

## SDKs

| SDK | Package | Description |
|-----|---------|-------------|
| Node.js | `@tombstone/core` | 5-step eval pipeline, Hash v1/v2, prerequisites, extended operators, OpenFeature compliant, 3-tier immutable cache, SSE sync — 108 tests |
| React | `@tombstone/react` | `useTombstone` hook + `TombstoneProvider`; automatic re-render on flag changes via SSE |
| Edge | `@tombstone/edge` | Cloudflare Workers — KV-backed snapshot + Cron Trigger for periodic refresh; no Node deps |
| WASM eval | `@tombstone/eval` | Zero-dependency WASM-ready evaluation engine; runs in browser, edge, and test environments — 41 tests |
| Test utilities | `TombstoneTestClient` | Deterministic test utilities for TypeScript and Python; override flags, assert evaluation paths, snapshot flag state |
| Python | `tombstone-python` | Async client, SSE listener, full evaluation engine |
| Ruby | `tombstone-ruby` | HTTP client + evaluation engine |
| Java | `tombstone-java` | Blocking + reactive (Project Reactor) client |
| .NET | `tombstone-dotnet` | C# client with .NET 8 + 9 targets |

### OpenFeature Compliance

`@tombstone/core` implements the OpenFeature TypeScript SDK provider interface. Drop it into any OpenFeature-compliant application:

```typescript
import { OpenFeature } from '@openfeature/server-sdk';
import { TombstoneProvider } from '@tombstone/core';

await OpenFeature.setProviderAndWait(
  new TombstoneProvider({ apiUrl: 'http://localhost:8081', apiKey: process.env.TOMBSTONE_API_KEY })
);

const client = OpenFeature.getClient();
const enabled = await client.getBooleanValue('new-checkout', false, { targetingKey: userId });
```

---

## MCP Integration

Tombstone exposes a **Streamable HTTP MCP server** at `/api/mcp/mcp` on the flag-api port.

Add it to `.claude/settings.json`:

```json
{
  "mcpServers": {
    "tombstone": {
      "command": "node",
      "args": ["/path/to/tombstone/workspace-mcp/dist/index.js"],
      "env": {
        "TOMBSTONE_API_URL": "http://localhost:8081",
        "TOMBSTONE_API_KEY": "your-api-key"
      }
    }
  }
}
```

**Available tools (8):**

| Tool | Description |
|------|-------------|
| `get_flag` | Fetch full flag definition including variations, rules, and prerequisites |
| `kill_switch` | Immediately disable a flag across all environments |
| `blast_radius` | Compute impact tier (BLOCKED/HIGH/MEDIUM/LOW) for a flag before changing it |
| `list_stale_flags` | List flags with no evaluations in the last N days — cleanup candidates |
| `create_flag` | Create a new flag with targeting rules and initial variations |
| `search_flags` | Hybrid semantic + lexical search across all flags |
| `generate_cleanup_pr` | Generate a GitHub PR with AST-rewriter codemods to remove a stale flag from the codebase |
| `openfeature_setup` | Generate OpenFeature provider configuration for a given SDK language |

Your AI assistant can query flag state, check blast radius, trigger rollbacks, and generate cleanup PRs directly from a chat session.

---

## Stack

| Layer | Technology |
|-------|-----------|
| Go services | Go 1.22+, `go.work` multi-module workspace, sqlc, grpc-gateway, OpenTelemetry |
| Python service | Python 3.12+, FastAPI, asyncio, uv, sentence-transformers (BAAI/bge-m3) |
| TypeScript | Node 22+, strict ESM, NodeNext resolution, Mocha+Chai (SDKs), Vitest (dashboard) |
| Frontend | React 19, Vite, Tailwind v4 |
| Database | PostgreSQL 16 + pgvector |
| Cache / streams | Redis 7 (pub/sub + Streams + sorted sets) |
| Event bus | Kafka |
| Analytics | ClickHouse (optional, production hardening) |
| Policy | OPA (Open Policy Agent) with Rego |
| Supply chain | syft SBOM + cosign keyless + Rekor |
| Containers | Docker Compose (local) + Helm (production) |
| Kubernetes | controller-runtime operator, FeatureFlag/FlagPolicy CRDs |
| IaC | Terraform `tombstone_region` resource |

---

## License

Apache-2.0
