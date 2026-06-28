# Tombstone

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI](https://github.com/sairam0424/Tombstone/actions/workflows/ci.yml/badge.svg)](https://github.com/sairam0424/Tombstone/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/sairam0424/Tombstone/graph/badge.svg)](https://codecov.io/gh/sairam0424/Tombstone)
[![OpenFeature](https://img.shields.io/badge/OpenFeature-compatible-blueviolet)](https://openfeature.dev)
[![Go Report Card](https://goreportcard.com/badge/github.com/sairam0424/Tombstone)](https://goreportcard.com/report/github.com/sairam0424/Tombstone)

Production intelligence layer for feature flags. Treats 5,000+ flags as a live causal graph of production behavior — combining flag delivery, blast-radius gating, circuit-breaker auto-rollback, and incident correlation in one self-hosted system.

**Every competitor asks "how do I deliver a flag value?" Tombstone asks "which of my 5,000 active flags is responsible for what's happening in production right now?"**

The things Tombstone adds that no other open-source flag system has: [circuit-breaker auto-rollback](#evaluator), [causal dependency graph + incident correlation](#intelligence), [ML-driven rollout recommendations](#intelligence) (Thompson Sampling + LinUCB contextual bandit), [Merkle-linked audit trail + Rekor transparency](#flag-api), and [CUPED + mSPRT experimentation](#intelligence).

> **Evaluating Tombstone vs. alternatives?** See [docs/WHY_TOMBSTONE.md](docs/WHY_TOMBSTONE.md) — competitive comparison, use cases, honest caveats, and a detailed breakdown of every unique capability.

## Quick Start

**Prerequisites:** Docker 20.10+, Docker Compose v2+, Make

```bash
git clone https://github.com/sairam0424/Tombstone.git
cd Tombstone
cp infra/.env.example infra/.env
make dev
```

The dashboard opens at **http://localhost:3000**.

> **First time?** `make dev` builds all images (~3–5 min), starts the full stack, runs migrations, and seeds 3 sample flags. Grab a coffee.

---

## What Runs

`make dev` starts the complete platform:

| Service | URL | What it does |
|---------|-----|-------------|
| **Dashboard** | http://localhost:3000 | React management UI — flags, approvals, governance |
| **flag-api** | http://localhost:8081 | REST CRUD, approval workflows, audit log, kill switch |
| **gateway** | http://localhost:8080 | SSE streaming to SDKs, real-time flag updates |
| **evaluator** | http://localhost:8082 | Blast-radius scoring, circuit-breaker auto-rollback, SLO tracking |
| **intelligence** | http://localhost:8083 | Anomaly detection, stale flag cleanup, rollout recommendations |
| **gitops-sync** | http://localhost:8084 | YAML-as-code flag sync from Git |
| **ast-rewriter** | http://localhost:8085 | Dead-code scanner for stale flag cleanup |
| **marketplace** | http://localhost:8086 | Integrations: Slack, Datadog, PagerDuty, OpsGenie, Jira, Linear |
| **PostgreSQL** | localhost:5433 | Primary store + pgvector |
| **Redis** | localhost:6380 | Pub/sub, Streams |
| **Kafka** | localhost:9092 | Event bus (optional — only needed if `CONSUMER_BACKEND=kafka`) |

---

## Configuration

```bash
cp infra/.env.example infra/.env
```

**That's it for local development.** The defaults work out of the box — no changes needed to run `make dev`.

> **For production deployments**, edit `infra/.env` and change these three values:
>
> | Variable | What to set | How |
> |----------|-------------|-----|
> | `POSTGRES_PASSWORD` | Strong random password | Any password — also update `DB_URL` to match |
> | `JWT_SECRET` | 64-char random hex | `openssl rand -hex 32` |
> | `FLAG_API_TOKEN` | Your SDK token | Any secret string your apps will use |
>
> See [SECURITY.md](SECURITY.md) for the full self-hosted security checklist.

---

## Managing the Stack

```bash
make dev          # Start everything (build + migrate + seed)
make down         # Stop everything
make test         # Run all tests (Go + TypeScript + Python)
make build        # Build all binaries and packages
make lint         # Lint all code

# Or use the helper script:
bash scripts/dev-local.sh up          # Start
bash scripts/dev-local.sh down        # Stop
bash scripts/dev-local.sh status      # Check all ports
bash scripts/dev-local.sh logs <svc>  # Tail service logs (e.g. flag-api, gateway)
```

---

## SDK Quick Start

### TypeScript / Node.js

```bash
npm install @tomb-stone/core
```

```typescript
import { TombstoneClient } from '@tomb-stone/core';

const client = new TombstoneClient({
  apiUrl: 'http://localhost:8081',
  sdkKey: 'sdk-dev-token-change-in-prod',
  environment: 'development',
});

await client.initialize();

const enabled = await client.isEnabled('checkout-v2', {
  userId: 'user-123',
  email: 'user@example.com',
});
```

### Python

```bash
pip install tombstone-sdk
```

```python
from tombstone import TombstoneClient

client = TombstoneClient(
    api_url="http://localhost:8081",
    sdk_key="sdk-dev-token-change-in-prod",
    environment="development",
)
client.initialize()

enabled = client.is_enabled("checkout-v2", {"user_id": "user-123"})
```

### React

```bash
npm install @tomb-stone/react
```

```tsx
import { TombstoneProvider, useFlag } from '@tomb-stone/react';

function App() {
  return (
    <TombstoneProvider apiUrl="http://localhost:8081" sdkKey="sdk-dev-token-change-in-prod">
      <CheckoutButton />
    </TombstoneProvider>
  );
}

function CheckoutButton() {
  const enabled = useFlag('checkout-v2');
  return enabled ? <NewCheckout /> : <LegacyCheckout />;
}
```

---

## What Is in v1.0.0

This is the first stable self-hosted release of Tombstone. Everything runs locally with `make dev`.

| Feature | Status |
|---------|--------|
| Flag CRUD with rollout slider | Stable |
| Four-eyes approval | Stable |
| Break-glass tokens | Stable |
| Real-time SSE | Stable |
| Incident timeline | Stable |
| Causal dependency graph | Stable |
| Governance + stale detection | Stable |
| Circuit-breaker auto-rollback | Stable |
| TypeScript/Python/React SDKs | Stable |
| MCP server | Stable |
| Cmd+K command palette | Stable |

Cloud deployment (Northflank, Vercel, Kubernetes) is planned for v1.1. See infra/ for those guides.

---

## Documentation

| Guide | What it covers |
|-------|---------------|
| [Why Tombstone](docs/WHY_TOMBSTONE.md) | USPs, competitive comparison, use cases, honest caveats — start here to evaluate |
| [Discoverability Roadmap](docs/DISCOVERABILITY.md) | GitHub topics, OSS launch strategy, awesome-go, CNCF, OpenFeature — how to grow Tombstone |
| [Discoverability Log](docs/DISCOVERABILITY_LOG.md) | Every distribution action taken — packages published, articles live, PRs open, newsletters sent |
| [Submission Guide](docs/SUBMISSION_GUIDE.md) | Every platform where Tombstone can be listed — 77 platforms, prioritized by effort/impact with exact URLs and steps |
| [User Guide](docs/USER_GUIDE.md) | Complete guide — what feature flags are, how to use Tombstone, every workflow explained |
| [Getting Started](docs/GETTING_STARTED.md) | 10-minute walkthrough from `make dev` to first flag in production |
| [Glossary](docs/GLOSSARY.md) | Blast radius, tombstoning, kill switch, circuit breaker, and more |
| [Contributing](CONTRIBUTING.md) | How to set up dev environment and submit PRs |
| [Security](SECURITY.md) | Vulnerability reporting, self-hosted security checklist, JWT setup |
| [Support](SUPPORT.md) | Where to get help — Discussions, Issues, Security |

**Evaluating Tombstone?** Read [Why Tombstone](docs/WHY_TOMBSTONE.md) — competitive comparison and use cases.
**New to feature flags?** Start with the [User Guide](docs/USER_GUIDE.md).
**Just ran `make dev`?** Go to [Getting Started](docs/GETTING_STARTED.md).
**Something broken?** Check [Troubleshooting](#troubleshooting) below or open a [GitHub Issue](https://github.com/sairam0424/Tombstone/issues/new?template=bug_report.md).

---

## Architecture

```
+-------------------------------------------------------------+
|                    Dashboard (React 19)                      |
|                    http://localhost:3000                      |
+---------------------------+---------------------------------+
                            |
           +----------------+-----------------+
           v                v                 v
     +----------+    +----------+    +------------+
     | flag-api |    | gateway  |    | evaluator  |
     |  :8081   |    |  :8080   |    |   :8082    |
     +----------+    +----------+    +------------+
           |                                |
           v                                v
     +----------+                   +------------+
     | postgres |                   |intelligence|
     |  :5433   |                   |   :8083    |
     +----------+                   +------------+
           |
     +----------+
     |  redis   |
     |  :6380   |
     +----------+
```

<a name="flag-api"></a>**flag-api** — The control plane. All flag CRUD, approval workflows, four-eyes sign-off, Merkle-linked audit trail, tombstoning (Knight Capital pattern), OPA RBAC, break-glass emergency tokens.

**gateway** — The data plane. SDK connections via SSE, Redis Streams fan-out, real-time flag updates to all connected clients.

<a name="evaluator"></a>**evaluator** — The safety layer. Blast-radius scoring per flag, circuit-breaker auto-rollback at error thresholds (5% errors / 100 requests / 10s window), SLO tracking.

<a name="intelligence"></a>**intelligence** — The ML layer. 3-model ensemble anomaly detection (Z-score + Isolation Forest + EWMA), Thompson Sampling + LinUCB contextual bandit rollout recommendations, CUPED variance reduction, mSPRT sequential testing, experiment collision detection, causal dependency graph, incident correlation ("What Changed?"), NLP semantic flag search (BAAI/bge-m3 + BM25 + RRF fusion).

---

## Troubleshooting

**Port already in use**
```bash
# Check which process is using the port:
lsof -i :8081   # or :8080, :3000, :5433, :6380

# Kill it:
kill -9 <PID>

# Or change the host port in infra/docker-compose.yml (left side of "hostport:containerport")
```

**Docker daemon not running**
```bash
# macOS: open Docker Desktop
# Linux:
sudo systemctl start docker
```

**Schema migration fails**
```bash
# Check postgres is healthy:
docker compose -f infra/docker-compose.yml ps postgres

# Run migration manually:
make migrate

# Or check postgres logs:
bash scripts/dev-local.sh logs postgres
```

**Dashboard shows "Offline" for some services**
Services take 10–30 seconds to be healthy after `make dev`. Refresh after 30 seconds. If still offline:
```bash
bash scripts/dev-local.sh status      # Check all ports
bash scripts/dev-local.sh logs evaluator
bash scripts/dev-local.sh logs intelligence
```

**Out of disk space (Docker)**
```bash
docker system prune -a     # Remove unused images, containers, networks
make down                  # Stop the stack first
make dev                   # Rebuild
```

**First `make dev` takes longer than expected**
The intelligence service image bundles the BAAI/bge-m3 embedding model (~400MB). Docker downloads and caches it during the **first build only** — you'll see it in the build output. Every subsequent `make dev` uses the cached layer and starts in seconds.

```
Step N/N : RUN python3 -c "from sentence_transformers..."
 ---> Downloading BAAI/bge-m3...   ← this is normal, wait ~2-5min
```

---

## Development

Want to modify and contribute? See [CLAUDE.md](CLAUDE.md) for the full developer guide including per-service build commands, test commands, and architecture conventions.

```bash
# Per-service development (without Docker):
cd services/flag-api && go build ./... && go test ./...
cd services/intelligence && uv sync && uv run pytest tests/
cd workspace-dashboard && npm run dev
```

**Go 1.22** | **Python 3.12** | **Node 22** | **TypeScript 6**

---

## License

MIT
