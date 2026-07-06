# API Reference

## REST API

**Base URL**: `http://localhost:8081` (flag-api)  
**Authentication**: `Authorization: Bearer {FLAG_API_TOKEN}`  
**Content-Type**: `application/json`  
**Protocol**: Proto-first — all contracts are defined in `proto/v1/`. REST endpoints are generated via grpc-gateway.

### Endpoint Groups

| Group | Path prefix | Description |
|-------|------------|-------------|
| Flags | `/api/v1/flags` | Flag CRUD, targeting rules, variations, prerequisites |
| Environments | `/api/v1/environments` | Per-environment flag state, snapshot |
| Audit | `/api/v1/audit` | Merkle-linked audit log |
| Compliance | `/api/v1/compliance` | SOC2 evidence, export |
| Change Requests | `/api/v1/change-requests` | Four-eyes approval workflow |
| Break-Glass | `/api/v1/break-glass` | Emergency override tokens |
| Scheduled | `/api/v1/flags/{key}/scheduled-changes` | Scheduled flag changes |
| Blast Radius | `/api/v1/blast-radius` | Pre-change risk scoring |
| Kill Switch | `/api/v1/flags/{key}/kill-switch` | Immediate disable |
| Marketplace | Handled by marketplace service (:8086) | Slack, Datadog, PagerDuty integrations |

### Key Endpoints

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| `GET` | `/api/v1/flags` | List all flags for a project | Required |
| `POST` | `/api/v1/flags` | Create a new flag | Required |
| `GET` | `/api/v1/flags/{key}` | Get flag by key | Required |
| `PUT` | `/api/v1/flags/{key}` | Update flag metadata | Required |
| `POST` | `/api/v1/flags/{key}/kill-switch` | Immediately disable flag | Required |
| `POST` | `/api/v1/flags/{key}/environments/{env}` | Update flag for environment | Required |
| `GET` | `/api/v1/environments/snapshot` | Full flag snapshot for SDK cache | Required |
| `GET` | `/api/v1/audit` | Query audit log (paginated) | Required |
| `GET` | `/api/v1/blast-radius` | Compute blast radius for a change | Required |
| `POST` | `/api/v1/flags/{key}/prerequisites` | Add prerequisite flag | Required |
| `POST` | `/api/v1/flags/{key}/scheduled-changes` | Schedule a future change | Required |
| `GET` | `/api/v1/change-requests` | List pending approvals | Required |
| `POST` | `/api/v1/change-requests/{id}/approve` | Approve a change request | Required |
| `POST` | `/api/v1/break-glass/tokens` | Create emergency override token | Required |
| `GET` | `/readyz` | Readiness probe (checks DB + Redis) | None |
| `GET` | `/health` | Liveness probe | None |

### SSE Stream (gateway, port 8080)

```
GET http://localhost:8080/api/v1/stream?environment=production
Authorization: Bearer {SDK_KEY}

# Streams FlagEvent objects as Server-Sent Events:
data: {"flag_key":"checkout-v2","enabled":true,"rollout_pct":50,"ts":1751808000,"environment":"production"}
```

---

## MCP Server

Tombstone ships an MCP (Model Context Protocol) server at `workspace-mcp/`. It exposes 8 tools for AI assistant integration via Streamable HTTP at `/api/mcp/mcp`.

To connect: add to your Claude Code config with `claude mcp add tombstone -- npx -y @tomb-stone/mcp`.

### MCP Tools

| Tool name | Description |
|-----------|-------------|
| `tombstone_get_flag` | Get the current state and metadata of a feature flag by dot-notation key |
| `tombstone_kill_switch` | Immediately disable a flag. Requires a reason of at least 10 characters |
| `tombstone_blast_radius` | Compute blast radius (risk score, affected traffic, dependent services) before flipping a flag |
| `tombstone_list_stale_flags` | List flags that haven't been updated recently and are candidates for cleanup |
| `tombstone_create_flag` | Create a new feature flag with dot-notation key |
| `tombstone_generate_cleanup_pr` | Generate a cleanup PR specification for a stale flag (branch name, PR title, checklist) |
| `tombstone_search_flags` | Natural-language search across all flags using the intelligence service |
| `tombstone_rollout_recommendation` | Get LinUCB bandit recommendation for whether to advance a flag's rollout |

---

## SDK Packages

| Runtime | Package | Registry |
|---------|---------|----------|
| TypeScript / Node.js | `@tomb-stone/core` | npm |
| React | `@tomb-stone/react` | npm |
| Edge / Cloudflare Workers | `@tomb-stone/edge` | npm |
| Browser (no bundler) | `@tomb-stone/browser` | npm |
| WASM / embedded | `@tomb-stone/eval` | npm |
| Python | `tombstone-sdk` | PyPI |
| Java | `tombstone-java-sdk` | Maven Central |
| Ruby | `tombstone-ruby` | RubyGems |
| .NET | `Tombstone.SDK` | NuGet |

---

## OpenAPI Reference

Full auto-generated OpenAPI documentation is served by flag-api at:
- **JSON spec:** `GET http://localhost:8081/api/v1/openapi.json`
- **Interactive explorer (Redoc):** `GET http://localhost:8081/api/v1/docs`

The JSON schema is generated from `proto/v1/` via grpc-gateway. Import into Postman, Insomnia, or any OpenAPI-compatible client.
The interactive explorer is embedded in the binary (no CDN dependency) via `go-redoc`.

---

## Proto Contracts

All API contracts are defined in `proto/v1/`:

| File | Defines |
|------|---------|
| `proto/v1/flags/flags.proto` | Flag evaluation, CRUD, environments, snapshot |
| `proto/v1/admin/admin.proto` | Approval workflows, audit log, compliance, governance |

To regenerate Go stubs after `.proto` changes: `make gen-proto`.

---

## Rate Limits

| Credential type | Sustained | Burst |
|----------------|-----------|-------|
| SDK token (Bearer) | 1,000 req/min | 50 req |
| IP (unauthenticated) | 200 req/min | 20 req |
| Evaluator telemetry | 5,000 req/min | 200 req |

On limit: HTTP 429 with `Retry-After` header. See `docs/runbooks/RATE_LIMITING.md` for details.
