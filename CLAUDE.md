# Tombstone

Tombstone is the **production intelligence layer** for feature flags at scale. It treats 5,000+ flags not as a configuration problem but as a live causal graph of production behavior — combining flag delivery, blast radius gating, circuit-breaker auto-rollback, and incident correlation into a single system.

**Core thesis:** Every competitor asks "how do I deliver a flag value?" — Tombstone asks "which of my 5,000 active flags is responsible for what's happening in production right now?"

**Current version: v2.0.1**

## Architecture

Polyglot monorepo (Go 1.22 + Python 3.12 + TypeScript) following the Graph-Forge workspace pattern.

```
Tombstone/
├── services/
│   ├── flag-api/        # Go — REST CRUD, approval workflows, audit log (Merkle), tombstoning,
│   │                    #        kill switch, prerequisites, variations, scheduled changes
│   ├── gateway/         # Go — SSE streaming (Redis Streams consumer groups), sync.Map pooling,
│   │                    #        backpressure, metrics endpoint
│   ├── evaluator/       # Go — circuit breaker (5% error/100 req), blast radius (BLOCKED/HIGH/
│   │                    #        MEDIUM/LOW), auto-rollback, SLO endpoint
│   ├── intelligence/    # Python — 3-model ensemble anomaly (ImDiffusion VLDB 2024), hybrid NLP
│   │                    #          search, Thompson Sampling + LinUCB, CUPED, mSPRT, collision
│   │                    #          detection, incident correlation, warehouse connectors
│   ├── gitops-sync/     # Go — YAML-as-code flag sync
│   ├── ast-rewriter/    # Go — dead-code scanner + jscodeshift rewrite
│   ├── marketplace/     # Go — integration registry (Slack, Datadog, PagerDuty, OpsGenie,
│   │                    #        Jira, Linear, OTel)
│   └── tombstone-operator/ # Go — Kubernetes operator (FeatureFlag/FlagPolicy CRDs)
├── packages/sdks/
│   ├── @tombstone/core/  # TypeScript — Node.js SDK, 5-step eval pipeline, 108 tests
│   ├── @tombstone/react/ # TypeScript — React SDK (hooks + TombstoneProvider)
│   ├── @tombstone/edge/  # TypeScript — Cloudflare Workers KV-backed snapshot + Cron Trigger
│   └── @tombstone/browser/ # TypeScript — browser bundle (no Node deps)
├── packages/sdk-wasm/   # @tombstone/eval — zero-dependency WASM-ready engine, 41 tests
├── workspace-cli/       # @tombstone/cli — Commander CLI
├── workspace-dashboard/ # React 19 + Vite + Tailwind v4 — management UI
├── workspace-mcp/       # MCP server — 8 tools, Streamable HTTP at /api/mcp/mcp
├── proto/v1/            # Protobuf contracts (source of truth for all APIs)
└── infra/               # Docker Compose + Helm (multi-region) + Terraform tombstone_region
```

## Build & Test Commands

### Quick start (Docker Compose)
```bash
make dev          # Spin up all services + seed sample flags
make down         # Stop all services
```

### Per-service development

**flag-api (Go):**
```bash
cd services/flag-api
go build -o bin/flag-api ./cmd/main.go
go test ./...
```

**gateway (Go):**
```bash
cd services/gateway
go build -o bin/gateway ./cmd/main.go
go test ./...
```

**evaluator (Go):**
```bash
cd services/evaluator
go build -o bin/evaluator ./cmd/main.go
go test ./...
```

**ast-rewriter (Go):**
```bash
cd services/ast-rewriter
go build -o bin/ast-rewriter ./cmd/main.go
go test ./...
```

**marketplace (Go):**
```bash
cd services/marketplace
go build -o bin/marketplace ./cmd/main.go
go test ./...
```

**tombstone-operator (Go):**
```bash
cd services/tombstone-operator
go build -o bin/operator ./cmd/main.go
go test ./...
```

**intelligence (Python):**
```bash
cd services/intelligence
uv sync --all-packages
uv run pytest tests/
```

**@tombstone/core (TypeScript SDK):**
```bash
cd packages/sdks/@tombstone/core
npm run build
npm run test        # mocha dist/tests/**/*.test.js
```

**@tombstone/eval (WASM engine):**
```bash
cd packages/sdk-wasm
npm run build
npm run test        # 41 tests
```

**dashboard (React):**
```bash
cd workspace-dashboard
npm run dev         # Vite dev server on :3000
npm run test        # Vitest
npm run build       # Production build
```

### Full workspace
```bash
make test           # All Go + TypeScript + Python tests
make build          # All binaries + packages
make lint           # golangci-lint + ruff + eslint
make gen-proto      # Regenerate Go stubs from .proto files
```

## Key File Locations

| File | Purpose |
|------|---------|
| `services/flag-api/internal/db/schema.sql` | PostgreSQL schema (authoritative) |
| `proto/v1/flags/flags.proto` | Flag evaluation + CRUD contracts |
| `proto/v1/admin/admin.proto` | Approval, audit, governance contracts |
| `packages/sdks/@tombstone/core/src/evaluation.ts` | In-process evaluation engine (5-step pipeline) |
| `packages/sdks/@tombstone/core/src/cache.ts` | Three-tier immutable flag cache |
| `packages/sdks/@tombstone/core/src/provider.ts` | OpenFeature provider implementation |
| `packages/sdks/@tombstone/core/src/testing.ts` | TombstoneTestClient — deterministic test utilities |
| `packages/sdk-wasm/src/index.ts` | WASM-ready zero-dependency eval engine |
| `services/gateway/internal/hub/hub.go` | SSE connection hub |
| `services/gateway/internal/hub/broadcaster.go` | Redis Streams → SSE fan-out |
| `services/gateway/internal/api/v1/metrics.go` | Gateway metrics endpoint |
| `services/flag-api/internal/api/v1/prerequisites.go` | Flag prerequisites (GrowthBook gate pattern) |
| `services/flag-api/internal/api/v1/variations.go` | Multivariate variations + weighted distribution |
| `services/flag-api/internal/api/v1/scheduled.go` | Scheduled flag changes |
| `services/flag-api/internal/scheduler/scheduler.go` | Scheduled-change executor |
| `services/flag-api/internal/transparency/rekor.go` | Rekor transparency log integration (fail-open) |
| `services/flag-api/policies/flags.rego` | OPA RBAC policy (hot-reload) |
| `services/intelligence/app/anomaly/ensemble.py` | 3-model ensemble: Z-score + Isolation Forest + EWMA |
| `services/intelligence/app/rollout/linucb.py` | LinUCB contextual bandit (Redis-persisted matrices) |
| `services/intelligence/app/experiments/cuped.py` | CUPED variance reduction (20–40%) |
| `services/intelligence/app/experiments/collision.py` | Experiment collision detection (Jaccard overlap) |
| `services/intelligence/app/warehouse/base.py` | Warehouse connector base (BigQuery/Snowflake/Databricks) |
| `services/tombstone-operator/cmd/main.go` | Kubernetes operator entrypoint (controller-runtime) |

## Conventions

- **Go services:** `go.work` multi-module workspace; `GOWORK=off` in Docker builds
- **TypeScript:** ESM-only (`"type": "module"`), strict mode, NodeNext resolution, Mocha+Chai for SDK, Vitest for dashboard
- **Python:** asyncio + FastAPI; `uv` for package management; `pytest` for tests
- **API contracts:** Proto-first. All REST endpoints generated via grpc-gateway from `.proto` files.
- **Database:** `sqlc` for type-safe Go queries from SQL — no ORM.
- **Immutability:** All TypeScript cache updates MUST create new objects (spread), never mutate in-place.
- **Audit log:** Append-only. Merkle hash covers full row: `sha256(id|event_type|actor|prev_state|new_state|ts)`. `prev_hash` chains entries. No UPDATE/DELETE on `audit_log` table ever.
- **Tombstoning:** Archived flag keys are permanent in `flag_tombstones`. `flags.key` reuse is blocked at DB constraint AND service layer.
- **OPA:** Policy-as-code RBAC via Rego files in `services/flag-api/policies/`. Hot-reload on file change — no restart needed.
- **Rekor:** Transparency log writes are fail-open and async. Do not add synchronous Rekor blocking to hot paths.
- **Redis Streams:** Gateway uses consumer groups for SSE fan-out. Use `XACK` after successful delivery; failed messages stay in PEL for retry.
- **Contract vectors:** Evaluation correctness is pinned against LD-verified 5-step pipeline contract tests. Changes to `evaluation.ts` or `@tombstone/eval` must pass all 108 + 41 contract tests.
- **Kill switch / break-glass:** Gated by `RequirePermission`. Never bypass in hot paths — circuit breaker handles auto-rollback.
- **Conventional Commits:** `type(scope)` — scopes: `flag-api`, `gateway`, `evaluator`, `intelligence`, `sdk`, `dashboard`, `cli`, `proto`, `infra`, `operator`, `marketplace`, `ast-rewriter`

## Double-Hook Protocol

Before starting any task: read `AGENTS_LEARNING.md`.
After completing any task: update `AGENTS_LEARNING.md` with new learnings.

## Loop-Engineer Harness

This repo uses the [loop-engineer](https://github.com/AI-Builder-Club/loop-engineer-template) pattern.
Read `ARCHITECTURE.md` for the knowledge-base model.

### Ship any code change
Use the `ship-change` workflow — do NOT create ad-hoc worktrees manually:
```js
Workflow({ name: "ship-change", args: {
  task: "what to build",
  repo: "/Users/.../Tombstone",
  baseBranch: "develop"
}})
```

### Open a PR
Use `/pr` skill — it spawns an independent verifier sub-agent that drives the real app.
"The feature is the verdict — a green test suite with an unverified feature isn't done."

### Set up new loops / domains
Use `/new-loop` skill. A loop = a domain = a recurring workstream with its own charter.
Charter, backlog, and timeline live in `domains/<name>/README.md`.

### Knowledge base
- `signals/` — evidence: feedback, friction, observations. Create with frontmatter `kind: signal`.
- `docs/` — durable knowledge: analysis, decisions, learnings. Create with `kind: doc`.
- `domains/` — the loops: flag-cleanup (daily), incident-response (event), rollout-advisor (daily).
- `LOG.md` — global feed. Append one entry per ship. Never delete entries.

### Active loops
| Loop | Trigger | Script | What it does |
|------|---------|--------|-------------|
| flag-cleanup | daily 02:00 UTC | `scripts/loop-flag-cleanup.sh` | Detects stale flags, creates signals, metrics |
| incident-response | circuit trip event | `scripts/loop-incident-response.sh <key> <env>` | Post-mortem doc + repeat-offender signal |
| rollout-advisor | daily 08:00 UTC | `scripts/loop-rollout-advisor.sh` | ML recommendation review + blast radius check |

### Dev stack
```bash
scripts/dev-local.sh up        # start full stack
scripts/dev-local.sh status    # check ports
scripts/dev-local.sh logs <svc>
```

## Service Ports (local dev)

| Service | Port |
|---------|------|
| flag-api | 8081 |
| gateway (SSE) | 8080 |
| evaluator | 8082 |
| intelligence | 8083 |
| gitops-sync | 8084 |
| ast-rewriter | 8085 |
| marketplace | 8086 |
| dashboard | 3000 |
| PostgreSQL | 5432 |
| Redis | 6379 |
| Kafka | 9092 |
| tombstone-operator | (in-cluster only) |
