# Tombstone

Tombstone is the **production intelligence layer** for feature flags at scale. It treats 5,000+ flags not as a configuration problem but as a live causal graph of production behavior — combining flag delivery, blast radius gating, circuit-breaker auto-rollback, and incident correlation into a single system.

**Core thesis:** Every competitor asks "how do I deliver a flag value?" — Tombstone asks "which of my 5,000 active flags is responsible for what's happening in production right now?"

## Architecture

Polyglot monorepo (Go 1.22 + Python 3.12 + TypeScript) following the Graph-Forge workspace pattern.

```
Tombstone/
├── services/
│   ├── flag-api/        # Go — REST CRUD, approval workflows, audit log, tombstoning
│   ├── gateway/         # Go — SSE streaming to SDKs, Redis hub, snapshot proxy
│   ├── evaluator/       # Go — circuit breaker, blast radius, rollback API
│   └── intelligence/    # Python — anomaly detection, incident correlation, NLP search
├── packages/sdks/
│   ├── @tombstone/core/  # TypeScript — Node.js SDK (in-process evaluation)
│   ├── @tombstone/react/ # TypeScript — React SDK (hooks + provider)
│   └── @tombstone/browser/ # TypeScript — browser bundle (no Node deps)
├── workspace-cli/       # @tombstone/cli — Commander CLI
├── workspace-dashboard/ # React 19 + Vite + Tailwind v4 — management UI
├── workspace-mcp/       # MCP server for AI coding assistant integration
├── proto/v1/            # Protobuf contracts (source of truth for all APIs)
└── infra/               # Docker Compose (Phase 1) + Helm charts (Phase 3+)
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
| `packages/sdks/@tombstone/core/src/evaluation.ts` | In-process evaluation engine |
| `packages/sdks/@tombstone/core/src/cache.ts` | Three-tier immutable flag cache |
| `services/gateway/internal/hub/hub.go` | SSE connection hub |
| `services/gateway/internal/hub/broadcaster.go` | Redis → SSE fan-out |

## Conventions

- **Go services:** `go.work` multi-module workspace; `GOWORK=off` in Docker builds
- **TypeScript:** ESM-only (`"type": "module"`), strict mode, NodeNext resolution, Mocha+Chai for SDK, Vitest for dashboard
- **Python:** asyncio + FastAPI; `uv` for package management; `pytest` for tests
- **API contracts:** Proto-first. All REST endpoints generated via grpc-gateway from `.proto` files.
- **Database:** `sqlc` for type-safe Go queries from SQL — no ORM.
- **Immutability:** All TypeScript cache updates MUST create new objects (spread), never mutate in-place.
- **Audit log:** Append-only. `prev_hash` Merkle-links entries. No UPDATE/DELETE on `audit_log` table ever.
- **Tombstoning:** Archived flag keys are permanent in `flag_tombstones`. `flags.key` reuse is blocked at DB constraint AND service layer.
- **Conventional Commits:** `type(scope)` — scopes: `flag-api`, `gateway`, `evaluator`, `intelligence`, `sdk`, `dashboard`, `cli`, `proto`, `infra`

## Double-Hook Protocol

Before starting any task: read `AGENTS_LEARNING.md`.
After completing any task: update `AGENTS_LEARNING.md` with new learnings.

## Service Ports (local dev)

| Service | Port |
|---------|------|
| flag-api | 8081 |
| gateway (SSE) | 8080 |
| evaluator | 8082 |
| intelligence | 8083 |
| dashboard | 3000 |
| PostgreSQL | 5432 |
| Redis | 6379 |
| Kafka | 9092 |
