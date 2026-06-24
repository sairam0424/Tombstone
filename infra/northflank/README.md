# Northflank Deployment — gateway + flag-api

Northflank Sandbox (free, always-on) hosts the two critical services.

## Why these two?

- **gateway** — SSE streaming hub. SDK clients hold long-lived connections here.
- **flag-api** — Core control plane. All flag CRUD, JWT auth, audit log, tombstoning.

Together they form the minimum viable production Tombstone:
SDKs connect → gateway streams → flag-api serves flag state.

## De-prioritized (run locally when needed)

| Service | How to run locally |
|---------|-------------------|
| intelligence | `cd services/intelligence && uv run uvicorn app.main:app --port 8083` |
| evaluator | `cd services/evaluator && go run ./cmd/main.go` |
| marketplace, gitops-sync, ast-rewriter | `make dev` (full Docker stack) |

## Setup — 30 min, no credit card needed

### 1. Create Northflank account
Go to **northflank.com** → Sign Up (Sandbox plan, no credit card required)

### 2. Create project: `tombstone`

### 3. Add Secret Group `tombstone-secrets`

| Secret | Value source |
|--------|-------------|
| `SECRET_DB_URL` | Neon connection string |
| `SECRET_REDIS_URL` | Upstash Redis TLS URL (`rediss://default:...`) |
| `SECRET_JWT_SECRET` | Any 32+ char random string |
| `SECRET_FLAG_API_URL` | Set AFTER flag-api deploys (its Northflank URL) |
| `SECRET_BEDROCK_ACCESS_KEY_ID` | From infra/.env |
| `SECRET_BEDROCK_SECRET_ACCESS_KEY` | From infra/.env |
| `SECRET_UPSTASH_REDIS_REST_URL` | From infra/.env |
| `SECRET_UPSTASH_REDIS_REST_TOKEN` | From infra/.env |

### 4. Connect GitHub: Settings → Git Integrations → sairam0424/Tombstone

### 5. Deploy flag-api FIRST

- New Service → From GitHub
- Dockerfile: `services/flag-api/Dockerfile` | Context: `services/flag-api`
- Port: 8081 (public HTTPS) | Plan: Sandbox | Attach: tombstone-secrets
- Wait for green health check
- **Copy the service URL** → update `SECRET_FLAG_API_URL` in secret group

### 6. Deploy gateway

- New Service → From GitHub
- Dockerfile: `services/gateway/Dockerfile` | Context: `services/gateway`
- Port: 8080 (public HTTPS) | Plan: Sandbox | Attach: tombstone-secrets
- Wait for green health check

### 7. Set GitHub Actions variables

```
TOMBSTONE_API_URL     = https://tombstone-flag-api--<hash>.northflank.app
TOMBSTONE_GATEWAY_URL = https://tombstone-gateway--<hash>.northflank.app
```

## Verify

```bash
curl https://tombstone-flag-api--<hash>.northflank.app/health
# {"status":"ok"}

curl -N "https://tombstone-gateway--<hash>.northflank.app/api/v1/stream?environment=production" \
  -H "Authorization: Bearer <sdk-token>"
# Stays connected — SSE heartbeats every 30s
```

## JSON service specs

- `gateway.json` — gateway service definition
- `flag-api.json` — flag-api service definition (use this, not intelligence.json)
- `intelligence.json` — kept for future reference when adding 3rd service
