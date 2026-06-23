---
name: dev-local
description: Start, stop, and inspect the Tombstone local dev stack. Triggers on "dev-local up", "start the stack", "bring up Tombstone locally", "start all services".
user_invocable: true
---

# Tombstone dev-local launcher

One-command local dev stack for Tombstone. All services run via Docker Compose.

## Service / Port Map

| Service | Port | Notes |
|---------|------|-------|
| gateway (SSE) | 8080 | Real-time flag stream to SDKs |
| flag-api | 8081 | REST CRUD, approvals, audit log |
| evaluator | 8082 | Circuit breaker, blast radius, SLO |
| intelligence | 8083 | ML anomaly, search, Thompson/LinUCB |
| gitops-sync | 8084 | YAML → flags sync |
| ast-rewriter | 8085 | Dead-code scanner + rewriter |
| marketplace | 8086 | Integration registry + webhooks |
| dashboard | 3000 | React management UI |
| PostgreSQL | 5433 | (5432 inside container) |
| Redis | 6380 | (6379 inside container) |

## Prerequisites
- Docker Desktop running
- `infra/.env` exists (copy from `infra/.env.example` if missing)

## Commands

```bash
scripts/dev-local.sh up              # build + start all services + migrate + seed
scripts/dev-local.sh status          # compose ps + port check
scripts/dev-local.sh logs flag-api   # tail service logs
scripts/dev-local.sh restart intelligence  # restart one service
scripts/dev-local.sh down            # stop stack (keep volumes)
scripts/dev-local.sh down --all      # stop stack + wipe volumes
```

## Troubleshooting

- **Port already in use:** `scripts/dev-local.sh status` to see which service is already running.
- **Schema errors:** `scripts/dev-local.sh down --all` then `up` for a clean state.
- **Intelligence slow to start:** It downloads the BAAI/bge-m3 model on first run (~1.5GB). Wait 90s.
