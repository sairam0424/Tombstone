# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> **Double-Hook Protocol:** Before starting any task, read `AGENTS_LEARNING.md`. After completing any task, update `AGENTS_LEARNING.md` with new learnings.

---

## Commands

### Quick start (Docker Compose)
```bash
make dev          # build all images, run migrations, seed sample flags
make down         # stop all services
make migrate      # re-apply schema.sql (idempotent)
make seed         # re-insert sample flags
```

### Per-service development
```bash
# Go services
cd services/flag-api && go build -o bin/flag-api ./cmd/main.go
cd services/flag-api && go test ./...
cd services/flag-api && go test -run TestFlagCreate ./...     # single test

cd services/gateway && go test ./...
cd services/evaluator && go test ./...
# same pattern for: gitops-sync, ast-rewriter, marketplace

# Python intelligence
cd services/intelligence
uv sync --all-packages
uv run pytest tests/
uv run pytest tests/test_anomaly.py::test_zscore   # single test

# TypeScript SDK (@tombstone/core)
cd packages/sdks/@flagmind/core
npm run build
npm run test          # mocha dist/tests/**/*.test.js
npm run test -- --grep "MurmurHash"   # single test by name

# Dashboard (React 19 + Vite)
cd workspace-dashboard
npm run dev           # Vite dev server :3000
npm run test          # Vitest
npm run build
```

### Full workspace
```bash
make test         # Go (flag-api + gateway) + TypeScript SDK + dashboard Vitest
make build        # all Go binaries + TypeScript packages
make lint         # golangci-lint (Go) + ruff (Python) + eslint (dashboard)
make gen-proto    # regenerate Go stubs from .proto files (requires protoc)
```

### End-to-end
```bash
bash scripts/e2e-test.sh    # 15-test suite against live Docker stack (waits 90s for intelligence)
```

---

## Service Ports (local dev)

| Service | Port | Notes |
|---------|------|-------|
| gateway (SSE) | 8080 | Real-time flag stream to SDKs |
| flag-api | 8081 | REST CRUD, approvals, audit log |
| evaluator | 8082 | Circuit breaker, blast radius, rollback |
| intelligence | 8083 | ML anomaly, NLP search, Thompson sampling |
| gitops-sync | 8084 | YAML → flags reconciliation |
| ast-rewriter | 8085 | Dead-code scanner + AST rewrite |
| marketplace | 8086 | Integration registry + webhook dispatcher |
| dashboard | 3000 | React management UI |
| PostgreSQL | **5433** | Host-side port (5432 inside container) |
| Redis | **6380** | Host-side port (6379 inside container) |
| Kafka | 9092 | Event streaming (Zookeeper on 2181 internal) |

ClickHouse is opt-in: `docker compose --profile clickhouse up` (adds port 9000/8123).

---

## Architecture

### Polyglot Monorepo

```
services/         Go microservices (go.work multi-module workspace)
packages/sdks/    SDKs: TypeScript (@flagmind/*), Python, Ruby, Java, .NET
workspace-cli/    @tombstone/cli — Commander.js CLI
workspace-dashboard/  React 19 + Vite + Tailwind v4 management UI
workspace-mcp/    MCP server (stdio) for AI coding assistant integration
workspace-vscode-ext/ VS Code extension (CodeLens + TreeView)
proto/v1/         Protobuf source of truth (flags, admin, intelligence)
infra/            Docker Compose + Helm + Terraform provider
```

### Cross-Service Communication

The event bus is **Redis pub/sub** — no Kafka in the Go hot path (Kafka feeds intelligence telemetry only).

| From | To | Protocol | Channel / Endpoint |
|------|----|----------|--------------------|
| flag-api | Redis | pub/sub publish | `stream:{environment}:updates` |
| flag-api | marketplace | HTTP POST | `/api/v1/marketplace/events` |
| gateway | Redis | PSUBSCRIBE | `stream:*:updates` |
| gateway | flag-api | HTTP GET | `/api/v1/environments/snapshot` (60s Redis cache) |
| evaluator | flag-api | HTTP POST | `/api/v1/flags/{key}/kill` (on circuit trip) |
| evaluator | Redis | pub/sub publish | `stream:{environment}:updates` (belt-and-suspenders) |
| evaluator | Redis | GET/SET | `circuit:{flagKey}:state` (TTL 10 min) |
| gitops-sync | flag-api | HTTP POST/PATCH | `/api/v1/flags`, `/api/v1/flags/{key}/environments/{env}` |
| intelligence | PostgreSQL | asyncpg | audit_log queries (blast radius, correlation, stale) |
| intelligence | Kafka | aiokafka consumer | telemetry events → AnomalyDetector |
| SDKs | gateway | EventSource (SSE) | `/api/v1/stream?environment={env}` |
| SDKs | evaluator | HTTP POST | `/api/v1/telemetry` (batch SDK error events) |

**Redis channel naming is strict:** `stream:{environment}:updates` — broadcaster.go extracts environment by splitting on `:`. Violating this breaks SSE fan-out to all SDKs.

### Rollback Flow (end-to-end)

1. SDK detects high error rate → `POST /api/v1/telemetry` to evaluator
2. Evaluator aggregates in 10s window → `ShouldTrip()` triggers at >5% error + ≥100 requests
3. Evaluator calls flag-api `/flags/{key}/kill` AND publishes kill event directly to Redis
4. flag-api disables flag, writes Merkle-linked audit entry, publishes to Redis
5. gateway broadcaster receives from Redis → hub fans out SSE `kill_switch` event to all SDK clients
6. marketplace (if installed) dispatches webhooks to Slack/PagerDuty/etc.

---

## Key Services in Depth

### flag-api — Control Plane

The authoritative state store. All flag mutations flow through here.

**Routes:**
- `GET/POST /api/v1/flags` — list, create
- `GET/PATCH/DELETE /api/v1/flags/{key}` — read, update, archive (archive → tombstone)
- `PATCH /api/v1/flags/{key}/environments/{env}` — enable/disable, rollout %
- `POST /api/v1/flags/{key}/kill` — kill switch (sets `enabled=false`, records reason)
- `GET /api/v1/environments/snapshot` — full flag state snapshot for an environment
- `GET /api/v1/audit` — paginated Merkle-linked audit log
- `GET /api/v1/compliance/{evidence,controls,export}` — SOC 2 evidence export
- `SCIM /scim/v2/Users*` — IdP user provisioning

Every flag mutation does three things atomically: updates state, publishes to Redis, writes audit log. Missing any one is a bug.

### gateway — SSE Streaming Hub

Stateless relay. Multiple instances can run in parallel (circuit breaker state is in Redis, not in-process).

- `GET /api/v1/stream?environment={env}` — SSE long-lived connection (30s heartbeat)
- `GET /api/v1/snapshot?environment={env}` — snapshot with `X-Cache: HIT/MISS` header

The broadcaster goroutine holds a `PSUBSCRIBE stream:*:updates` subscription. On message, it extracts the environment from the channel name and calls `hub.Broadcast()`. Slow SSE clients get dropped after 64-item buffer fills.

### evaluator — Safety Layer

- `POST /api/v1/telemetry` — batch SDK telemetry ingest
- `GET /api/v1/blast-radius?flag_key=&environment=&rollout_pct=` — pre-change risk assessment
- `GET /api/v1/circuit/{flagKey}` — circuit state (CLOSED/OPEN/HALF_OPEN)
- `POST /api/v1/rollback` — manual rollback

**Circuit breaker thresholds:** 5% error rate, minimum 100 requests per 10-second window. State stored in Redis `circuit:{flagKey}:state` (TTL 10 min). Half-open recovery uses 5-minute observation window.

**Blast radius scoring matrix:**

| Score | Condition | Gate |
|-------|-----------|------|
| BLOCKED | ≥50% traffic AND >5% historical error delta | Requires 10-char typed justification + second approver |
| HIGH | ≥25% traffic OR >5 dependent flags | Requires acknowledgment |
| MEDIUM | ≥10% traffic OR >2 dependent flags | — |
| LOW | otherwise | — |

Dependent flags = flags that changed within the same environment within 30 days (queried from audit_log).

### intelligence — ML/AI Layer (Python)

Five independent engines, each stateless (no DB writes):

| Engine | Algorithm | Key Detail |
|--------|-----------|------------|
| Anomaly detection | Z-score, 7-day rolling window (672 × 10s buckets) | Threshold: 2.5 std deviations; real-time via Kafka consumer |
| NLP search | Hybrid lexical + BAAI/bge-m3 dense embeddings + RRF | Graceful degrade to PostgreSQL full-text if model download fails |
| Autonomous rollout | Thompson Sampling Beta(1,1) prior | Min 50 observations; schedule `[1,5,10,25,50,75,100]%`; posteriors **in-memory only** (lost on restart) |
| Incident correlation | Exponential recency decay `exp(-0.1 * minutes_before)` | 30-min pre-incident window; returns top 3 candidates |
| Dependency graph | Co-change coupling within 300s windows | O(n²) audit_log scan; top 50 edges retained |

pgvector extension is loaded but **not yet used** — prepared for Phase 6 dense search indices.

### gitops-sync — Flag-as-Code

Runs in two modes: **HTTP server** (awaits CI/CD webhook `POST /api/v1/sync`) or **one-shot CLI** (set `FLAGS_DIR` env var → syncs and exits).

Sync is create-or-update only. **Never deletes flags** — archiving is always an explicit API call.

YAML schema (`apiVersion: tombstone.io/v1`, `kind: FeatureFlag`):
```yaml
spec:
  key: payments.checkout.checkout-v2
  type: BOOLEAN          # BOOLEAN | STRING | INTEGER | FLOAT | JSON
  safeDefault: "false"
  environments:
    production:
      enabled: false
      rolloutPct: 0
```

### ast-rewriter — Dead-Code Cleanup

Activated after a flag variant wins and the losing branch should be removed from source.

- `POST /api/v1/scan` — find all call-site occurrences of a flag key across a repo
- `POST /api/v1/rewrite` — generate unified diff or apply AST rewrite (jscodeshift for JS/TS, textual for others)

Supports TypeScript, JavaScript, Python, Go, Java, Ruby. Excludes `node_modules`, `vendor`, `dist`, `.next`, `.venv`. Invokes jscodeshift as a subprocess with 30s timeout; falls back to textual diff if jscodeshift unavailable.

### marketplace — Integration Registry

In-memory registry (no DB). Ephemeral state — restarts clear all installed integrations.

First-party integrations: Slack, Datadog, PagerDuty, OpsGenie, Jira, Linear, OpenTelemetry.

Event types dispatched: `flag.created`, `flag.enabled`, `flag.disabled`, `flag.kill_switch`, `flag.rollback`, `flag.stale_detected`, `flag.archived`.

Webhook delivery is fire-and-forget (goroutine per webhook, 10s timeout, no retries).

---

## Database Schema

Schema file: `services/flag-api/internal/db/schema.sql` (authoritative).

**Core tables and their invariants:**

| Table | Key Constraint |
|-------|---------------|
| `flag_tombstones` | `key TEXT PRIMARY KEY` — permanent; never pruned |
| `flags` | `UNIQUE(project_id, key)` + BEFORE INSERT trigger blocks tombstoned keys |
| `flag_environments` | `PRIMARY KEY (flag_id, environment)` — one row per flag per env |
| `audit_log` | DB rules block ALL UPDATE and DELETE — append-only enforced at DB level |
| `change_requests` | `approved_by TEXT[]` tracks multiple approvers; `PENDING → APPROVED → APPLIED` or `REJECTED` |
| `break_glass_tokens` | `token TEXT UNIQUE`; `used BOOLEAN` + partial index on `WHERE used = false` |
| `inventory_limits` | Default 500 flags per project; middleware returns HTTP 429 when `current_count >= max_flags` |
| `user_roles` | `PRIMARY KEY (user_id, project_id)`; role enum: VIEWER → OPERATOR → OWNER → ADMIN |
| `scheduled_changes` | Partial index on `(scheduled_for) WHERE status = 'PENDING'` for scheduler queries |

**Tombstone enforcement (two-layer):**
1. DB trigger `enforce_tombstone` fires BEFORE INSERT on `flags` — raises exception if key exists in `flag_tombstones`
2. Service layer checks tombstone before accepting CreateFlag and returns HTTP 409 with a human-readable error

**Merkle-linked audit log:** Each entry stores `prev_hash = sha256(previous_entry.id || previous_entry.created_at)`. Computation in `writeAudit()` in `services/flag-api/internal/api/v1/flags.go`.

**sqlc:** All Go DB queries are generated. After any schema change run `sqlc generate` inside `services/flag-api` before editing query files.

**RBAC defaults:** Service tokens (prefixed `sdk:`) always resolve to OPERATOR. Unknown actors default to VIEWER (least privilege).

---

## SDK Architecture

All six language SDKs implement the same evaluation contract:

1. Fetch full snapshot from `GET /api/v1/environments/snapshot` on init
2. Subscribe to SSE stream at `GET /api/v1/stream?environment={env}` for incremental updates
3. Apply updates **immutably** — always spread into new objects, never mutate in-place
4. Evaluate: targeting rules (priority-sorted) → MurmurHash3 rollout → safe default

**MurmurHash3 cross-SDK invariant:** All SDKs must use unsigned 32-bit MurmurHash3 (seed=0) on `flagKey + userId` then `hash % 100 < rolloutPct`. Language-specific gotchas:
- Java: `Integer.remainderUnsigned(hash, 100)` — not `hash % 100` (signed)
- .NET: `BitConverter.ToUInt32` — not signed conversion
- Ruby: `MurmurHash3::V32.str_hash(flagKey + userId, 0)`
- TypeScript: `murmurhash.v3(flagKey + userId) >>> 0` then `% 100`
- Edge SDK (Cloudflare Workers): FNV-1a acceptable (no WASM murmurhash in Workers)

The `packages/sdks/test-contract/vectors.json` file contains 12 reference vectors. Every SDK must reproduce them exactly. This file is the parity gate — run it if you touch rollout logic in any SDK.

**SDK packages:**

| Package | Runtime | Notes |
|---------|---------|-------|
| `@tombstone/core` (`packages/sdks/@flagmind/core`) | Node.js | SSE streaming, three-tier immutable cache |
| `@tombstone/react` | React ≥18 | Hooks + TombstoneProvider; peer-depends on core |
| `@tombstone/edge` | Cloudflare Workers | KV-backed snapshot, no SSE |
| `flagmind-python` | Python async | httpx, mmh3 |
| `flagmind-ruby` | Ruby | murmurhash3 gem |
| `flagmind-java` | Java 21 / Gradle | |
| `flagmind-dotnet` | .NET / NuGet | |

---

## Workspace Tools

### workspace-mcp — MCP Server (stdio)

8 tools exposed to AI coding assistants:

| Tool | Purpose |
|------|---------|
| `tombstone_get_flag` | Fetch flag metadata by key |
| `tombstone_kill_switch` | Emergency disable (reason min 10 chars) |
| `tombstone_blast_radius` | Risk analysis before flip |
| `tombstone_list_stale_flags` | Cleanup candidates (default: 30-day window) |
| `tombstone_create_flag` | Create new flag |
| `tombstone_search_flags` | NLP semantic search |
| `tombstone_generate_cleanup_pr` | Generate PR spec for dead-code removal |
| `tombstone_openfeature_setup` | Setup instructions for TypeScript or Python |

Config in `.claude/settings.json`:
```json
{
  "mcpServers": {
    "tombstone": {
      "command": "node",
      "args": ["/path/to/tombstone/workspace-mcp/dist/index.js"],
      "env": { "TOMBSTONE_API_URL": "http://localhost:8081", "TOMBSTONE_API_KEY": "..." }
    }
  }
}
```

### workspace-cli — CLI

```bash
flags list --project <id> --env <env>
flags get <key>
flags enable <key> --env <env>
flags disable <key> --env <env>
flags flip <key> --env <env> --pct <n> --dry-run
```

Auth via `TOMBSTONE_API_URL` + `TOMBSTONE_TOKEN` env vars.

### workspace-vscode-ext — VS Code Extension

- **CodeLens:** Inline flag state (enabled %, owner, stale warning) above every `evaluate()` / `isEnabled()` / `is_enabled()` call. 30s cache TTL.
- **TreeView:** Sidebar flag list — killed flags first (red), then enabled/disabled, then alphabetical.
- **Commands:** `killSwitch` (reason min 10 chars), `searchFlags` (NLP), `generateCleanupPR`, `openFlagInDashboard`.
- **Settings:** `tombstone.apiUrl`, `tombstone.apiToken` (stored in VS Code secrets), `tombstone.environment`, `tombstone.intelligenceApiUrl`.

### workspace-dashboard — React UI

Views: FlagList, FlagDetail, IncidentTimeline ("What Changed?"), GovernanceDash (stale + autonomous rollout recommendations), ApprovalQueue, BreakGlass, DependencyGraph (D3 force-directed), Experiments, Marketplace.

`useFlags(environment, projectId?)` hook computes staleness: flag at 100% rollout for 30+ days with no updates = cleanup candidate.

---

## Conventions

- **Go services:** `go.work` multi-module workspace; each service has its own `go.mod`; **no cross-service Go imports** (use REST/gRPC). `GOWORK=off` required in all Dockerfiles.
- **TypeScript:** ESM-only (`"type": "module"`), strict mode, NodeNext resolution. Import paths must use `.js` extensions.
- **Python:** asyncio + FastAPI + `uv` for package management.
- **Proto-first:** All REST endpoints generated via grpc-gateway from `.proto` files. Modifying a `.proto` requires `make gen-proto` to regenerate Go stubs.
- **sqlc:** Type-safe Go queries generated from SQL. Schema changes require `sqlc generate` in `services/flag-api`.
- **Immutability:** All TypeScript cache updates MUST create new objects (spread), never mutate in-place. Enforced in `@tombstone/core` cache.ts.
- **Audit log:** Append-only. `prev_hash` Merkle-links entries. **No UPDATE/DELETE on `audit_log` ever** — DB rules enforce this at the PostgreSQL level.
- **Tombstoning:** Archived flag keys are permanent in `flag_tombstones`. Reuse is blocked by DB trigger AND service layer.
- **Redis channels:** `stream:{environment}:updates` — this naming is load-bearing; gateway broadcaster extracts environment by string split.
- **Conventional Commits:** `type(scope)` — scopes: `flag-api`, `gateway`, `evaluator`, `intelligence`, `sdk`, `dashboard`, `cli`, `proto`, `infra`

---

## Branch Model

- `main` — production releases (never commit directly)
- `develop` — integration branch (all PRs merge here)
- Feature branches: `feature/<description>` → PR to `develop`

---

## CI Pipeline

`.github/workflows/ci.yml` runs 8 parallel jobs on push to `main`/`develop` and PRs to `develop`:

- **Go services** (6 jobs, parallel): `go vet` → `go build` → `go test` (60s timeout). `GOWORK=off` per job.
- **Python intelligence**: ruff lint → `uv pip install -e .` → no tests in CI (e2e script covers this)
- **TypeScript SDK**: Node 22 → `npm install` → `npm run build`
- **Python SDK**: pytest (uses mmh3)
- **Ruby SDK**: bundler + rspec
- **Java SDK**: Gradle test (Java 21)
- **Terraform provider**: `go build + go vet` in `infra/terraform/provider/`

---

## Environment Variables

Minimum for local dev (see `infra/.env.example` for full list):

```bash
# PostgreSQL
DB_URL=postgres://tombstone:change-me@localhost:5433/tombstone?sslmode=disable

# Redis
REDIS_URL=redis://localhost:6380

# flag-api
JWT_SECRET=change-me-at-least-32-chars-long

# evaluator → flag-api auth
FLAG_API_TOKEN=sdk-dev-token-change-in-prod

# dashboard (Vite client-side)
VITE_EVAL_URL=http://localhost:8082
VITE_INTEL_URL=http://localhost:8083
VITE_SDK_TOKEN=sdk-dev-token-change-in-prod
```

Optional: `PAGERDUTY_TOKEN` (incident correlation), `CLICKHOUSE_HOST` (high-volume telemetry), `SCIM_TOKEN` (IdP sync — endpoint returns 503 without it), `CLOUDFLARE_*` (edge SDK KV sync).

---

## Key File Locations

| File | Purpose |
|------|---------|
| `services/flag-api/internal/db/schema.sql` | PostgreSQL schema (authoritative) |
| `services/flag-api/internal/api/v1/flags.go` | CRUD handlers, audit writing, Redis publishing |
| `services/evaluator/internal/circuit/breaker.go` | Circuit breaker state machine |
| `services/evaluator/internal/blast/blast_radius.go` | Blast radius scoring |
| `services/evaluator/internal/rollback/rollback.go` | Auto-rollback executor |
| `services/gateway/internal/hub/broadcaster.go` | Redis → SSE fan-out |
| `services/gateway/internal/hub/hub.go` | In-memory SSE connection registry |
| `services/intelligence/app/anomaly/detector.py` | Z-score anomaly detection |
| `services/intelligence/app/rollout/thompson.py` | Thompson Sampling autonomous rollout |
| `services/intelligence/app/search/retriever.py` | Hybrid NLP search (lexical + dense) |
| `services/intelligence/app/correlation/correlator.py` | Incident correlation engine |
| `packages/sdks/@flagmind/core/src/cache.ts` | Immutable SDK flag cache |
| `packages/sdks/@flagmind/core/src/evaluation.ts` | In-process evaluation engine |
| `packages/sdks/test-contract/vectors.json` | MurmurHash3 cross-SDK parity vectors |
| `proto/v1/flags/flags.proto` | Flag evaluation + CRUD contracts |
| `proto/v1/admin/admin.proto` | Approval, audit, blast radius, break-glass contracts |
| `flags/examples/checkout-v2.yaml` | GitOps YAML flag definition example |
| `scripts/e2e-test.sh` | 15-test end-to-end suite |
