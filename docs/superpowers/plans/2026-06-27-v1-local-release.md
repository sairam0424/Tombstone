# Tombstone v1 Local Self-Hosted Release Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship Tombstone v1 as a fully self-hosted product — users run `make dev` and get the complete platform locally, with a README that tells them exactly how.

**Architecture:** Three tasks: (1) fix `docker-compose.yml` so all dashboard views work locally out of the box; (2) rewrite `README.md` as a self-hosted setup guide replacing the current marketing-focused feature showcase; (3) cut the `v1.0.0-local` release tag. No cloud dependencies. No Northflank. No Vercel. Pure Docker Compose.

**Tech Stack:** Docker Compose, Make, YAML

## Global Constraints

- Repo root: `/Users/sairamugge/Desktop/Not-Humans-World/Tombstone`
- Branch: work directly on `main` (these are docs + config, not code)
- No Go, Python, or Node changes — only YAML, Markdown, shell
- The `.env.example` file purpose: local-first, not cloud-first
- Keep existing docker-compose service definitions intact — only ADD to dashboard environment block
- README must be honest: prerequisites are Docker + Make only
- No marketing fluff — just what a user needs to run it

---

## Task 1: Fix docker-compose.yml — enable all views for local dev

**Files:**
- Modify: `infra/docker-compose.yml` (lines 178–180, dashboard environment block)

**Context:** The dashboard service environment currently only has `VITE_API_URL` and `VITE_GATEWAY_URL`. Missing 7 vars means all intelligence/evaluator/marketplace views stay hidden even when those services are running locally. A user who runs `make dev` locally would see GovernanceDash, SLOView, and Marketplace as "offline" — despite those services running on ports 8082/8083/8086.

**Interfaces:**
- Produces: Dashboard sees all services. `VITE_ENABLE_INTELLIGENCE=true` etc. bake into the Vite dev server at startup so GovernanceDash, SLOView, Experiments, and Marketplace all load data from the locally running services.

- [ ] **Step 1: Read the current dashboard block**

Read `infra/docker-compose.yml` lines 166–181 to confirm the exact current environment block.

- [ ] **Step 2: Replace the environment block**

Find this exact block in `infra/docker-compose.yml`:
```yaml
    environment:
      VITE_API_URL: http://localhost:8081
      VITE_GATEWAY_URL: http://localhost:8080
```

Replace with:
```yaml
    environment:
      VITE_API_URL: http://localhost:8081
      VITE_GATEWAY_URL: http://localhost:8080
      VITE_EVAL_URL: http://localhost:8082
      VITE_INTEL_URL: http://localhost:8083
      VITE_MARKETPLACE_URL: http://localhost:8086
      VITE_SDK_TOKEN: sdk-dev-token-change-in-prod
      VITE_ENABLE_INTELLIGENCE: "true"
      VITE_ENABLE_EVALUATOR: "true"
      VITE_ENABLE_MARKETPLACE: "true"
```

- [ ] **Step 3: Verify YAML is valid**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
python3 -c "import yaml; yaml.safe_load(open('infra/docker-compose.yml'))" && echo "YAML valid"
```

Expected: `YAML valid`

- [ ] **Step 4: Commit**

```bash
git add infra/docker-compose.yml
git commit -m "fix(infra): enable all dashboard views in local docker-compose — add VITE_ENABLE_* and service URLs"
```

---

## Task 2: Rewrite README.md for self-hosted v1

**Files:**
- Modify: `README.md` (full rewrite — 371 lines → ~250 lines, focused on setup)
- Modify: `infra/.env.example` (fix header comment — remove cloud-deployment focus, make local-first)

**Context:** Current README.md opens with "The 3am Call Problem" and marketing tables. It has no prerequisites section, no step-by-step setup, and no troubleshooting. A new user has no idea how to actually run it. The `.env.example` header comment says "PRODUCTION DEPLOYMENT (Northflank + Oracle + Cloudflare Pages)" which is wrong for v1 self-hosted.

- [ ] **Step 1: Rewrite README.md**

Write the complete new README.md:

```markdown
# Tombstone

Production intelligence layer for feature flags. Treats 5,000+ flags as a live causal graph of production behavior — combining flag delivery, blast-radius gating, circuit-breaker auto-rollback, and incident correlation in one self-hosted system.

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
| **Kafka** | localhost:9092 | Event bus |

---

## Configuration

Copy and edit the environment file:

```bash
cp infra/.env.example infra/.env
```

**Required changes** (edit `infra/.env`):

```bash
POSTGRES_PASSWORD=your-secure-password        # Change from default
JWT_SECRET=your-32-char-minimum-secret-key    # Min 32 chars
FLAG_API_TOKEN=your-dev-sdk-token             # Token for SDK auth
```

Everything else has working defaults for local development.

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
npm install @tombstone/core
```

```typescript
import { TombstoneClient } from '@tombstone/core';

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
pip install tombstone
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
npm install @tombstone/react
```

```tsx
import { TombstoneProvider, useFlag } from '@tombstone/react';

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

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Dashboard (React 19)                       │
│                    http://localhost:3000                       │
└──────────────────────────┬──────────────────────────────────┘
                           │
          ┌────────────────┼────────────────┐
          ▼                ▼                ▼
    ┌──────────┐    ┌──────────┐    ┌────────────┐
    │ flag-api │    │ gateway  │    │ evaluator  │
    │  :8081   │    │  :8080   │    │   :8082    │
    └──────────┘    └──────────┘    └────────────┘
          │                                │
          ▼                                ▼
    ┌──────────┐                   ┌────────────┐
    │ postgres │                   │intelligence│
    │  :5433   │                   │   :8083    │
    └──────────┘                   └────────────┘
          │
    ┌──────────┐
    │  redis   │
    │  :6380   │
    └──────────┘
```

**flag-api** — The control plane. All flag CRUD, approval workflows, four-eyes sign-off, Merkle-linked audit trail, tombstoning (Knight Capital pattern), OPA RBAC, break-glass emergency tokens.

**gateway** — The data plane. SDK connections via SSE, Redis Streams fan-out, real-time flag updates to all connected clients.

**evaluator** — The safety layer. Blast-radius scoring per flag, circuit-breaker auto-rollback at error thresholds, SLO tracking.

**intelligence** — The ML layer. 3-model ensemble anomaly detection (Z-score + Isolation Forest + EWMA), stale flag detection, LinUCB rollout recommendations, CUPED variance reduction, warehouse connectors (BigQuery, Snowflake, Databricks).

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

**Intelligence service slow to start**
The intelligence service downloads the BAAI/bge-m3 embedding model on first run (~400MB). This is a one-time download cached in the Docker image. Subsequent starts are fast.

---

## Development

Want to modify and contribute? See [CLAUDE.md](CLAUDE.md) for the full developer guide including per-service build commands, test commands, and architecture conventions.

```bash
# Per-service development (without Docker):
cd services/flag-api && go build ./... && go test ./...
cd services/intelligence && uv sync && uv run pytest tests/
cd workspace-dashboard && npm run dev
```

**Go 1.25** | **Python 3.12** | **Node 22** | **TypeScript 6**

---

## License

MIT
```

- [ ] **Step 2: Fix infra/.env.example header comment**

Read the current `infra/.env.example`. Replace the header section (lines 1–12) which currently references Northflank/Oracle/Cloudflare with a local-first header:

```bash
# Tombstone environment configuration.
# Copy this file to infra/.env before running `make dev`.
# NEVER commit infra/.env to git (it's git-ignored).
#
# Required changes for local development:
#   POSTGRES_PASSWORD  → any secure password
#   JWT_SECRET         → any random string, minimum 32 characters
#   FLAG_API_TOKEN     → your local SDK token
#
# Everything else has working defaults for local dev.
# See README.md for the full setup guide.
```

- [ ] **Step 3: Verify README has no broken markdown**

```bash
python3 -c "
import re
with open('/Users/sairamugge/Desktop/Not-Humans-World/Tombstone/README.md') as f:
    content = f.read()
# Check no empty headings
empty_headings = re.findall(r'^#{1,6}\s*$', content, re.MULTILINE)
# Check no placeholder text
placeholders = ['TODO', 'TBD', 'FIXME', 'your-org']
found = [p for p in placeholders if p in content]
print('Empty headings:', empty_headings)
print('Placeholders found:', found)
print('Line count:', len(content.splitlines()))
print('PASS' if not empty_headings and not found else 'CHECK NEEDED')
"
```

Expected: `PASS`, line count ~200-260

- [ ] **Step 4: Commit**

```bash
git add README.md infra/.env.example
git commit -m "docs: rewrite README.md as self-hosted setup guide for v1 release

Replaces marketing-focused feature showcase with actionable setup guide:
- 3-command quick start (prerequisites: Docker + Make only)
- Complete service table with URLs
- SDK quick start (TypeScript, Python, React)
- Architecture diagram
- Troubleshooting section (port conflicts, docker daemon, migration, disk space)
- Development guide reference to CLAUDE.md

Also fixes infra/.env.example header to be local-first instead of
cloud-deployment focused."
```

---

## Task 3: Cut v1.0.0-local release tag

**Files:**
- Modify: `CLAUDE.md` (update version string)

**Context:** Current CLAUDE.md has `**Current version: v2.2.0 / Dashboard v1.0.0**`. We're tagging this as `v1.0.0-local` to mark the self-hosted release point.

- [ ] **Step 1: Update version in CLAUDE.md**

Read `CLAUDE.md`. Find:
```
**Current version: v2.2.0 / Dashboard v1.0.0**
```

Replace with:
```
**Current version: v2.2.0 / Dashboard v1.0.0 (self-hosted)**
```

- [ ] **Step 2: Commit version bump**

```bash
git add CLAUDE.md
git commit -m "chore: mark v1.0.0 self-hosted release in CLAUDE.md"
```

- [ ] **Step 3: Push all commits to main**

```bash
git push origin main
```

- [ ] **Step 4: Create the release tag**

```bash
git tag -a v1.0.0-local -m "Tombstone v1.0.0 — Self-Hosted Release

Full platform runs locally with a single command: make dev

What's included:
- flag-api: Flag CRUD, approval workflows, audit log, kill switch, break-glass
- gateway: SSE streaming, real-time flag updates to SDKs
- evaluator: Blast-radius, circuit-breaker auto-rollback, SLO tracking
- intelligence: Anomaly detection, stale flag cleanup, rollout recommendations
- gitops-sync, ast-rewriter, marketplace: GitOps, dead-code cleanup, integrations
- dashboard: Full React 19 management UI — all views functional
- PostgreSQL 16 + pgvector, Redis 7, Kafka 7.6

Prerequisites: Docker 20.10+, Docker Compose v2+, Make

Quick start:
  git clone https://github.com/sairam0424/Tombstone.git
  cd Tombstone && cp infra/.env.example infra/.env
  make dev
  # Dashboard at http://localhost:3000

Next version (v1.1): Cloud-hosted option (Northflank + Vercel)"

git push origin v1.0.0-local
```

- [ ] **Step 5: Create GitHub release**

```bash
export PATH="/opt/homebrew/bin:$PATH"
gh release create v1.0.0-local \
  --title "Tombstone v1.0.0 — Self-Hosted" \
  --repo sairam0424/Tombstone \
  --notes "$(cat <<'EOF'
## Tombstone v1.0.0 — Self-Hosted Release

Full production intelligence platform for feature flags. Runs locally with a single command.

### Quick Start

**Prerequisites:** Docker 20.10+, Docker Compose v2+, Make

\`\`\`bash
git clone https://github.com/sairam0424/Tombstone.git
cd Tombstone
cp infra/.env.example infra/.env
make dev
\`\`\`

Dashboard at **http://localhost:3000**

### What's Included

| Service | Port | Description |
|---------|------|-------------|
| Dashboard | 3000 | React 19 management UI |
| flag-api | 8081 | Flag CRUD, approvals, audit log, kill switch |
| gateway | 8080 | SSE streaming to SDKs |
| evaluator | 8082 | Blast-radius, circuit-breaker, SLO |
| intelligence | 8083 | Anomaly detection, recommendations |
| gitops-sync | 8084 | YAML-as-code sync |
| ast-rewriter | 8085 | Dead-code cleanup |
| marketplace | 8086 | Slack, Datadog, PagerDuty integrations |

### v1.1 Roadmap
- Cloud-hosted option (flag-api + gateway on Northflank, dashboard on Vercel)
- Kubernetes Helm charts
- Production hardening guide
EOF
)"
```

---

## Self-Review

**Spec coverage:**
- ✅ docker-compose.yml VITE_ENABLE_* vars → Task 1
- ✅ docker-compose.yml VITE_EVAL_URL, VITE_INTEL_URL, VITE_MARKETPLACE_URL, VITE_SDK_TOKEN → Task 1
- ✅ README rewrite (prerequisites, quick start, services, SDKs, troubleshooting) → Task 2
- ✅ .env.example header fix (local-first) → Task 2
- ✅ CLAUDE.md version bump → Task 3
- ✅ git tag v1.0.0-local → Task 3
- ✅ GitHub release created → Task 3

**Placeholder scan:** README uses `sairam0424/Tombstone` for the actual repo — correct. No TBD/TODO.

**Type consistency:** No code — N/A.
