# Tombstone v2.2.0 Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Deploy Tombstone v2.2.0 to production at $0/month using Northflank (gateway + intelligence, always-on), Oracle Cloud Always Free (5 Go services, self-managed VM), and Cloudflare Pages (dashboard, static CDN).

**Architecture:** Three-platform hybrid. Northflank hosts the two services that must never sleep (SSE gateway, Redis Streams background worker). Oracle Cloud ARM VM (4 OCPUs, 24GB RAM — free forever) hosts the five stateless Go services via docker-compose. Cloudflare Pages hosts the React dashboard as a static SPA. All three connect to the same managed backends: Neon PostgreSQL, Upstash Redis, AWS Bedrock.

**Tech Stack:** Docker (all Go/Python services), docker-compose (Oracle), Northflank CLI/dashboard (gateway + intelligence), Cloudflare Pages (dashboard), GitHub Actions (CI/CD + loop workflows), nginx + Let's Encrypt (Oracle TLS).

## Global Constraints

- All deployments must be $0/month — no paid tiers
- Northflank Sandbox: strictly 2 services max — gateway and intelligence only
- Oracle Always Free: ARM (aarch64) architecture — all Docker images must be multi-arch or ARM-specific
- Never commit secrets — all credentials go into platform secret managers
- Gateway service: MUST have `auto_stop_machines = 'off'` equivalent on every platform (no sleep ever)
- Intelligence service: CONSUMER_BACKEND=redis, EMBEDDING_BACKEND=bedrock
- Dashboard: VITE env vars injected at build time — not runtime
- JWT_SECRET and FLAG_API_TOKEN in production must be rotated from dev values
- All services use Neon DB_URL and Upstash REDIS_URL — same values everywhere
- `GOWORK=off` in all Docker builds

---

## Task 1: Version Cut — Tag v2.2.0 and PR develop → main

**Files:**
- No code changes — git operations only

**Interfaces:**
- Produces: `v2.2.0` git tag on main, develop synced to main

- [ ] **Step 1: Verify develop is clean and ahead of main**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git fetch origin
git log --oneline origin/main..origin/develop
```

Expected: `275c51a chore(release): prep v2.2.0 ...` (the CHANGELOG/env.example commit)

- [ ] **Step 2: Push develop and open PR to main**

```bash
git push origin develop
```

Then create PR:
```bash
/opt/homebrew/bin/gh pr create \
  --repo sairam0424/Tombstone \
  --base main --head develop \
  --title "release(v2.2.0): Bedrock embeddings + Redis Streams + Neon pool tuning + deployment prep" \
  --body "Release prep for v2.2.0. Adds CHANGELOG entry, documents all deployment env vars in .env.example."
```

- [ ] **Step 3: Wait for CI green, merge**

```bash
# Wait for CI then:
/opt/homebrew/bin/gh pr merge <PR_NUMBER> --repo sairam0424/Tombstone --merge
```

- [ ] **Step 4: Tag v2.2.0 on main**

```bash
git checkout main && git pull origin main
git tag -a v2.2.0 -m "Tombstone v2.2.0 — Fly.io free-tier deployment ready
- Bedrock Titan V2 embeddings (EMBEDDING_BACKEND=bedrock)
- Redis Streams consumer (CONSUMER_BACKEND=redis)
- Neon connection pool tuning (all services)
- .env.example fully documented
- Dashboard build verified"
git push origin v2.2.0
```

- [ ] **Step 5: Verify tag exists on remote**

```bash
/opt/homebrew/bin/gh release list --repo sairam0424/Tombstone | head -3
# OR:
git ls-remote --tags origin | grep "v2.2.0"
```

Expected: `v2.2.0` tag visible. GitHub Actions `release.yml` will auto-trigger SBOM generation.

---

## Task 2: Oracle Cloud Always Free — VM Setup

**Files:**
- Create: `infra/oracle/cloud-init.yml` — VM bootstrap script
- Create: `infra/oracle/docker-compose.prod.yml` — production compose (5 Go services only)
- Create: `infra/oracle/nginx.conf` — reverse proxy config
- Create: `infra/oracle/setup.sh` — one-shot server setup script

**Interfaces:**
- Produces: running Oracle ARM VM with Docker + nginx + SSL
- Produces: `docker-compose.prod.yml` that starts flag-api, evaluator, marketplace, gitops-sync, ast-rewriter

- [ ] **Step 1: Create `infra/oracle/cloud-init.yml`**

```yaml
#cloud-config
# Oracle Cloud Always Free — ARM (aarch64) Ubuntu 22.04
# Run once on first boot via Oracle Cloud console "Cloud-Init Script" field

package_update: true
package_upgrade: true

packages:
  - docker.io
  - docker-compose-v2
  - nginx
  - certbot
  - python3-certbot-nginx
  - git
  - curl
  - ufw

runcmd:
  # Enable Docker
  - systemctl enable docker
  - systemctl start docker
  - usermod -aG docker ubuntu

  # Firewall — allow SSH, HTTP, HTTPS, and Tombstone service ports
  - ufw allow 22/tcp
  - ufw allow 80/tcp
  - ufw allow 443/tcp
  - ufw allow 8081/tcp   # flag-api
  - ufw allow 8082/tcp   # evaluator
  - ufw allow 8084/tcp   # gitops-sync
  - ufw allow 8085/tcp   # ast-rewriter
  - ufw allow 8086/tcp   # marketplace
  - ufw --force enable

  # Clone repo
  - git clone https://github.com/sairam0424/Tombstone.git /opt/tombstone
  - chown -R ubuntu:ubuntu /opt/tombstone

  # Create .env from template (fill in secrets after setup)
  - cp /opt/tombstone/infra/.env.example /opt/tombstone/infra/.env
```

- [ ] **Step 2: Create `infra/oracle/docker-compose.prod.yml`**

```yaml
# Production docker-compose for Oracle Cloud Always Free ARM VM
# Runs only the 5 stateless Go services.
# gateway and intelligence run on Northflank (separate platform).
# PostgreSQL and Redis are managed externally (Neon + Upstash).

services:
  flag-api:
    build:
      context: ../../services/flag-api
      dockerfile: Dockerfile
    restart: unless-stopped
    ports:
      - "8081:8081"
    environment:
      - DB_URL=${DB_URL}
      - REDIS_URL=${REDIS_URL}
      - JWT_SECRET=${JWT_SECRET}
      - PORT=8081
      - EMBEDDING_BACKEND=${EMBEDDING_BACKEND:-bedrock}
      - BEDROCK_ACCESS_KEY_ID=${BEDROCK_ACCESS_KEY_ID}
      - BEDROCK_SECRET_ACCESS_KEY=${BEDROCK_SECRET_ACCESS_KEY}
      - BEDROCK_REGION=${BEDROCK_REGION:-us-east-1}
      - UPSTASH_REDIS_REST_URL=${UPSTASH_REDIS_REST_URL}
      - UPSTASH_REDIS_REST_TOKEN=${UPSTASH_REDIS_REST_TOKEN}
      - REKOR_ENABLED=${REKOR_ENABLED:-false}
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8081/health"]
      interval: 30s
      timeout: 5s
      retries: 3

  evaluator:
    build:
      context: ../../services/evaluator
      dockerfile: Dockerfile
    restart: unless-stopped
    ports:
      - "8082:8082"
    environment:
      - DB_URL=${DB_URL}
      - REDIS_URL=${REDIS_URL}
      - FLAG_API_URL=http://flag-api:8081
      - FLAG_API_TOKEN=${FLAG_API_TOKEN}
      - PORT=8082
    depends_on:
      flag-api:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8082/health"]
      interval: 30s
      timeout: 5s
      retries: 3

  marketplace:
    build:
      context: ../../services/marketplace
      dockerfile: Dockerfile
    restart: unless-stopped
    ports:
      - "8086:8086"
    environment:
      - REDIS_URL=${REDIS_URL}
      - FLAG_API_URL=http://flag-api:8081
      - FLAG_API_TOKEN=${FLAG_API_TOKEN}
      - DD_WEBHOOK_SECRET=${DD_WEBHOOK_SECRET}
      - EVALUATOR_URL=http://evaluator:8082
      - SLACK_BOT_TOKEN=${SLACK_BOT_TOKEN}
      - SLACK_SIGNING_SECRET=${SLACK_SIGNING_SECRET}
      - PORT=8086
    depends_on:
      - flag-api

  gitops-sync:
    build:
      context: ../../services/gitops-sync
      dockerfile: Dockerfile
    restart: unless-stopped
    ports:
      - "8084:8084"
    environment:
      - FLAG_API_URL=http://flag-api:8081
      - FLAG_API_TOKEN=${FLAG_API_TOKEN}
      - PORT=8084
    depends_on:
      flag-api:
        condition: service_healthy

  ast-rewriter:
    build:
      context: ../../services/ast-rewriter
      dockerfile: Dockerfile
    restart: unless-stopped
    ports:
      - "8085:8085"
    environment:
      - PORT=8085
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8085/health"]
      interval: 30s
      timeout: 5s
      retries: 3

networks:
  default:
    name: tombstone-prod
```

- [ ] **Step 3: Create `infra/oracle/nginx.conf`**

```nginx
# /etc/nginx/sites-available/tombstone
# Reverse proxy for all Tombstone services on Oracle Cloud VM
# Replace YOUR_DOMAIN with your actual domain or Oracle public IP

upstream flag_api    { server 127.0.0.1:8081; }
upstream evaluator   { server 127.0.0.1:8082; }
upstream marketplace { server 127.0.0.1:8086; }
upstream gitops_sync { server 127.0.0.1:8084; }
upstream ast_rewriter{ server 127.0.0.1:8085; }

server {
    listen 80;
    server_name YOUR_DOMAIN;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name YOUR_DOMAIN;

    # Certbot will add SSL lines here automatically
    # ssl_certificate /etc/letsencrypt/live/YOUR_DOMAIN/fullchain.pem;
    # ssl_certificate_key /etc/letsencrypt/live/YOUR_DOMAIN/privkey.pem;

    # flag-api — core CRUD, authentication
    location /api/v1/flags        { proxy_pass http://flag_api; proxy_set_header Host $host; proxy_set_header X-Real-IP $remote_addr; }
    location /api/v1/environments { proxy_pass http://flag_api; proxy_set_header Host $host; proxy_set_header X-Real-IP $remote_addr; }
    location /api/v1/audit        { proxy_pass http://flag_api; proxy_set_header Host $host; proxy_set_header X-Real-IP $remote_addr; }
    location /api/v1/compliance   { proxy_pass http://flag_api; proxy_set_header Host $host; proxy_set_header X-Real-IP $remote_addr; }
    location /api/v1/break-glass  { proxy_pass http://flag_api; proxy_set_header Host $host; proxy_set_header X-Real-IP $remote_addr; }
    location /auth/               { proxy_pass http://flag_api; proxy_set_header Host $host; proxy_set_header X-Real-IP $remote_addr; }
    location /health              { proxy_pass http://flag_api; }

    # evaluator — circuit breaker, blast radius, SLO
    location /api/v1/blast-radius { proxy_pass http://evaluator; proxy_set_header Host $host; }
    location /api/v1/circuit      { proxy_pass http://evaluator; proxy_set_header Host $host; }
    location /api/v1/telemetry    { proxy_pass http://evaluator; proxy_set_header Host $host; }
    location /api/v1/rollback     { proxy_pass http://evaluator; proxy_set_header Host $host; }

    # marketplace — integrations
    location /api/v1/marketplace  { proxy_pass http://marketplace; proxy_set_header Host $host; }

    # gitops-sync — YAML flag sync
    location /api/v1/sync         { proxy_pass http://gitops_sync; proxy_set_header Host $host; }

    # ast-rewriter — dead code scanner
    location /api/v1/scan         { proxy_pass http://ast_rewriter; proxy_set_header Host $host; }
    location /api/v1/rewrite      { proxy_pass http://ast_rewriter; proxy_set_header Host $host; }
}
```

- [ ] **Step 4: Create `infra/oracle/setup.sh`**

```bash
#!/usr/bin/env bash
# One-shot setup script for Oracle Cloud Always Free ARM VM
# Run as ubuntu user AFTER cloud-init completes (check: cloud-init status --wait)
set -euo pipefail

REPO_DIR="/opt/tombstone"
ENV_FILE="$REPO_DIR/infra/.env"

echo "=== Tombstone Oracle Setup ==="

# 1. Pull latest code
cd "$REPO_DIR"
git pull origin main

# 2. Check .env is filled in
if grep -q "change-me" "$ENV_FILE"; then
    echo "ERROR: $ENV_FILE still has placeholder values."
    echo "Edit $ENV_FILE and fill in DB_URL, REDIS_URL, JWT_SECRET, etc."
    exit 1
fi

# 3. Build ARM images (multi-arch or native)
cd "$REPO_DIR"
docker compose -f infra/oracle/docker-compose.prod.yml build \
    --build-arg GOARCH=arm64 \
    --build-arg GOOS=linux

# 4. Start services
docker compose -f infra/oracle/docker-compose.prod.yml up -d

# 5. Wait for flag-api health
echo "Waiting for flag-api..."
for i in $(seq 1 30); do
    curl -sf http://localhost:8081/health >/dev/null 2>&1 && echo "flag-api: UP" && break
    sleep 2
done

# 6. Configure nginx
cp "$REPO_DIR/infra/oracle/nginx.conf" /etc/nginx/sites-available/tombstone
ln -sf /etc/nginx/sites-available/tombstone /etc/nginx/sites-enabled/tombstone
nginx -t && systemctl reload nginx

echo ""
echo "=== Setup complete ==="
echo "Next: certbot --nginx -d YOUR_DOMAIN to enable HTTPS"
echo "Then update TOMBSTONE_API_URL in GitHub Actions vars to https://YOUR_DOMAIN"
```

```bash
chmod +x /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/infra/oracle/setup.sh
bash -n /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/infra/oracle/setup.sh && echo "syntax OK"
```

- [ ] **Step 5: Commit oracle infra files**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add infra/oracle/
git commit -m "feat(infra): Oracle Cloud Always Free deployment config — docker-compose, nginx, setup script, cloud-init"
```

---

## Task 3: Northflank Service Configs — gateway + intelligence

**Files:**
- Create: `infra/northflank/gateway.json` — Northflank service spec for gateway
- Create: `infra/northflank/intelligence.json` — Northflank service spec for intelligence
- Create: `infra/northflank/README.md` — setup instructions

**Interfaces:**
- Produces: Northflank service definitions deployable via Northflank CLI or dashboard

- [ ] **Step 1: Create `infra/northflank/gateway.json`**

```json
{
  "apiVersion": "v1",
  "spec": {
    "kind": "Service",
    "spec": {
      "name": "tombstone-gateway",
      "billing": {
        "deploymentPlan": "nf-compute-10"
      },
      "deployment": {
        "docker": {
          "configType": "default"
        },
        "buildpack": {
          "builder": "DOCKERFILE",
          "dockerFilePath": "/services/gateway/Dockerfile",
          "dockerWorkDir": "/services/gateway",
          "useCache": false
        },
        "instances": 1,
        "storage": {
          "ephemeralStorage": {
            "storageSize": 1024
          }
        }
      },
      "ports": [
        {
          "name": "http",
          "internalPort": 8080,
          "public": true,
          "protocol": "HTTP",
          "security": {
            "sslOnly": true
          },
          "disableNfDomain": false
        }
      ],
      "runtimeEnvironment": {
        "REDIS_URL": "${SECRET_REDIS_URL}",
        "FLAG_API_URL": "${SECRET_FLAG_API_URL}",
        "PORT": "8080",
        "CONSUMER_BACKEND": "redis",
        "TOMBSTONE_ENVIRONMENTS": "production"
      },
      "healthChecks": [
        {
          "protocol": "HTTP",
          "path": "/health",
          "port": 8080,
          "initialDelaySeconds": 10,
          "periodSeconds": 30,
          "timeoutSeconds": 5,
          "failureThreshold": 3
        }
      ]
    }
  }
}
```

- [ ] **Step 2: Create `infra/northflank/intelligence.json`**

```json
{
  "apiVersion": "v1",
  "spec": {
    "kind": "Service",
    "spec": {
      "name": "tombstone-intelligence",
      "billing": {
        "deploymentPlan": "nf-compute-10"
      },
      "deployment": {
        "docker": {
          "configType": "default"
        },
        "buildpack": {
          "builder": "DOCKERFILE",
          "dockerFilePath": "/services/intelligence/Dockerfile",
          "dockerWorkDir": "/services/intelligence",
          "useCache": false
        },
        "instances": 1
      },
      "ports": [
        {
          "name": "http",
          "internalPort": 8083,
          "public": true,
          "protocol": "HTTP",
          "security": {
            "sslOnly": true
          }
        }
      ],
      "runtimeEnvironment": {
        "DB_URL": "${SECRET_DB_URL}",
        "REDIS_URL": "${SECRET_REDIS_URL}",
        "EMBEDDING_BACKEND": "bedrock",
        "BEDROCK_ACCESS_KEY_ID": "${SECRET_BEDROCK_ACCESS_KEY_ID}",
        "BEDROCK_SECRET_ACCESS_KEY": "${SECRET_BEDROCK_SECRET_ACCESS_KEY}",
        "BEDROCK_REGION": "us-east-1",
        "CONSUMER_BACKEND": "redis",
        "TOMBSTONE_ENVIRONMENTS": "production",
        "PORT": "8083"
      },
      "healthChecks": [
        {
          "protocol": "HTTP",
          "path": "/health",
          "port": 8083,
          "initialDelaySeconds": 30,
          "periodSeconds": 30,
          "timeoutSeconds": 10,
          "failureThreshold": 3
        }
      ]
    }
  }
}
```

- [ ] **Step 3: Create `infra/northflank/README.md`**

```markdown
# Northflank Deployment — gateway + intelligence

Northflank Sandbox (free) hosts the two always-on services.

## Setup

1. Create account at northflank.com (no credit card required for Sandbox)
2. Create a new project: "tombstone"
3. Add secrets (Settings → Secret Groups):
   - SECRET_DB_URL = your Neon connection string
   - SECRET_REDIS_URL = your Upstash Redis URL
   - SECRET_FLAG_API_URL = https://your-oracle-domain/  (set after Oracle is live)
   - SECRET_BEDROCK_ACCESS_KEY_ID = (from infra/.env)
   - SECRET_BEDROCK_SECRET_ACCESS_KEY = (from infra/.env)

4. Connect GitHub repo: Settings → Git Integrations → sairam0424/Tombstone

5. Create two services using the JSON specs here:
   - gateway.json → tombstone-gateway (Sandbox plan)
   - intelligence.json → tombstone-intelligence (Sandbox plan)

6. Deploy both — they will build from Dockerfile automatically on push to main.

## After deploy

Note the Northflank service URLs (e.g. https://tombstone-gateway--xxx.northflank.app):
- Set TOMBSTONE_GATEWAY_URL in GitHub Actions repo variables
- Set TOMBSTONE_INTELLIGENCE_URL in GitHub Actions repo variables

SDK clients connect to: https://tombstone-gateway--xxx.northflank.app/api/v1/stream
```

- [ ] **Step 4: Commit northflank configs**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add infra/northflank/
git commit -m "feat(infra): Northflank service configs for gateway + intelligence (always-on free tier)"
```

---

## Task 4: Cloudflare Pages — Dashboard Deployment Config

**Files:**
- Create: `workspace-dashboard/public/_redirects` — SPA routing fix
- Create: `workspace-dashboard/public/_headers` — security headers
- Create: `.github/workflows/deploy-dashboard.yml` — CF Pages deploy workflow

**Interfaces:**
- Produces: dashboard auto-deployed to Cloudflare Pages on every push to main
- Consumes: `VITE_API_URL`, `VITE_GATEWAY_URL`, `VITE_INTEL_URL`, `VITE_SDK_TOKEN` — injected at build time

- [ ] **Step 1: Create `workspace-dashboard/public/_redirects`**

```
# Cloudflare Pages SPA routing — all paths serve index.html
/*    /index.html   200
```

- [ ] **Step 2: Create `workspace-dashboard/public/_headers`**

```
# Security headers for Tombstone dashboard
/*
  X-Frame-Options: DENY
  X-Content-Type-Options: nosniff
  Referrer-Policy: strict-origin-when-cross-origin
  Permissions-Policy: camera=(), microphone=(), geolocation=()
  Content-Security-Policy: default-src 'self'; connect-src 'self' https://*.northflank.app https://*.neon.tech wss://*.northflank.app; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'
```

- [ ] **Step 3: Create `.github/workflows/deploy-dashboard.yml`**

```yaml
name: Deploy Dashboard to Cloudflare Pages

on:
  push:
    branches: [main]
    paths:
      - 'workspace-dashboard/**'
      - '.github/workflows/deploy-dashboard.yml'
  workflow_dispatch:

jobs:
  deploy:
    runs-on: ubuntu-latest
    name: Deploy to Cloudflare Pages
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-node@v4
        with:
          node-version: '22'

      - name: Install dashboard deps
        working-directory: workspace-dashboard
        run: npm ci

      - name: Build dashboard
        working-directory: workspace-dashboard
        env:
          VITE_API_URL: ${{ vars.TOMBSTONE_API_URL }}
          VITE_GATEWAY_URL: ${{ vars.TOMBSTONE_GATEWAY_URL }}
          VITE_INTEL_URL: ${{ vars.TOMBSTONE_INTELLIGENCE_URL }}
          VITE_EVAL_URL: ${{ vars.TOMBSTONE_EVALUATOR_URL }}
          VITE_SDK_TOKEN: ${{ secrets.TOMBSTONE_SDK_TOKEN }}
        run: npm run build

      - name: Deploy to Cloudflare Pages
        uses: cloudflare/wrangler-action@v3
        with:
          apiToken: ${{ secrets.CLOUDFLARE_API_TOKEN }}
          accountId: ${{ secrets.CLOUDFLARE_ACCOUNT_ID }}
          command: pages deploy workspace-dashboard/dist --project-name=tombstone-dashboard --branch=main
```

- [ ] **Step 4: Commit dashboard configs**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add workspace-dashboard/public/_redirects workspace-dashboard/public/_headers .github/workflows/deploy-dashboard.yml
git commit -m "feat(infra): Cloudflare Pages deployment — SPA routing, security headers, GitHub Actions workflow"
```

---

## Task 5: Update CI/CD + GitHub Actions Variables Documentation

**Files:**
- Modify: `.github/workflows/ci.yml` — add `tombstone-operator` to Go matrix
- Create: `infra/github-actions-vars.md` — reference doc for all required secrets/vars

**Interfaces:**
- Produces: complete documentation of every GitHub Actions secret and variable needed

- [ ] **Step 1: Check if tombstone-operator is in ci.yml matrix**

```bash
grep "matrix\|service:" /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/.github/workflows/ci.yml | head -10
```

If `tombstone-operator` is missing from the matrix, add it.

- [ ] **Step 2: Create `infra/github-actions-vars.md`**

```markdown
# GitHub Actions Secrets + Variables — Tombstone

Configure these at: github.com/sairam0424/Tombstone/settings/secrets/actions

## Secrets (sensitive — never shown after save)

| Secret | Value | Used by |
|--------|-------|---------|
| `TOMBSTONE_SDK_TOKEN` | A strong random token (32+ chars) | deploy-dashboard.yml |
| `CLOUDFLARE_API_TOKEN` | CF API token with Pages:Edit permission | deploy-dashboard.yml |
| `CLOUDFLARE_ACCOUNT_ID` | From cloudflare.com/dashboard | deploy-dashboard.yml |

## Repository Variables (non-sensitive — visible in logs)

| Variable | Value | Used by |
|----------|-------|---------|
| `TOMBSTONE_API_URL` | https://your-oracle-domain | loop workflows, dashboard build |
| `TOMBSTONE_GATEWAY_URL` | https://tombstone-gateway--xxx.northflank.app | dashboard build |
| `TOMBSTONE_INTELLIGENCE_URL` | https://tombstone-intelligence--xxx.northflank.app | loop workflows |
| `TOMBSTONE_EVALUATOR_URL` | https://your-oracle-domain/api/v1 | loop-incident-response.yml |
| `TOMBSTONE_PROJECT_ID` | 00000000-0000-0000-0000-000000000001 | loop-flag-cleanup.yml |

## Notes

- TOMBSTONE_API_URL and TOMBSTONE_EVALUATOR_URL point to Oracle Cloud VM (flag-api + evaluator)
- TOMBSTONE_GATEWAY_URL and TOMBSTONE_INTELLIGENCE_URL point to Northflank services
- Loop workflows only activate when their required var is set (gated by `if: vars.TOMBSTONE_INTELLIGENCE_URL != ''`)
- Cloudflare secrets only needed after Cloudflare Pages project is created
```

- [ ] **Step 3: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add infra/github-actions-vars.md .github/workflows/ci.yml 2>/dev/null || git add infra/github-actions-vars.md
git commit -m "docs(infra): GitHub Actions secrets/variables reference + CI matrix check"
```

---

## Task 6: Push to develop → PR to main → Final v2.2.0 tag

**Files:** No new code — git operations only

- [ ] **Step 1: Push all infra commits to develop**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git push origin develop
```

- [ ] **Step 2: Open PR develop → main**

```bash
/opt/homebrew/bin/gh pr create \
  --repo sairam0424/Tombstone \
  --base main --head develop \
  --title "feat(infra): v2.2.0 deployment configs — Oracle, Northflank, Cloudflare Pages" \
  --body "Adds all deployment infrastructure:
- infra/oracle/ — docker-compose.prod.yml, nginx.conf, cloud-init.yml, setup.sh
- infra/northflank/ — gateway.json, intelligence.json, README.md
- workspace-dashboard/public/ — _redirects, _headers (Cloudflare Pages)
- .github/workflows/deploy-dashboard.yml — auto-deploy on push to main
- infra/github-actions-vars.md — complete secrets/vars reference"
```

- [ ] **Step 3: Wait for CI green, merge**

```bash
/opt/homebrew/bin/gh pr checks <PR_NUMBER> --repo sairam0424/Tombstone 2>&1 | grep -E "pass|fail" | sort -u
# When all pass:
/opt/homebrew/bin/gh pr merge <PR_NUMBER> --repo sairam0424/Tombstone --merge
```

- [ ] **Step 4: Tag v2.2.0**

```bash
git checkout main && git pull origin main
git tag -a v2.2.0 -m "Tombstone v2.2.0 production release"
git push origin v2.2.0
```

---

## Task 7: Live Deployment — Oracle Cloud VM

This task is **manual** — requires Oracle Cloud console access.

- [ ] **Step 1: Create Oracle Cloud Always Free account**
- Go to cloud.oracle.com → Create Account
- Choose "Always Free" resources (not paid)
- Select region: US East (Ashburn) — closest to Bedrock us-east-1

- [ ] **Step 2: Create ARM VM instance**
- Compute → Instances → Create Instance
- Shape: VM.Standard.A1.Flex (Always Free ARM)
- OCPUs: 4, Memory: 24GB
- Image: Canonical Ubuntu 22.04
- Paste `infra/oracle/cloud-init.yml` content into "Cloud-Init Script"
- Add your SSH public key
- Create instance → note the public IP

- [ ] **Step 3: Fill in production secrets on the VM**

```bash
ssh ubuntu@<oracle-public-ip>
# Wait for cloud-init to finish:
cloud-init status --wait

# Edit the .env file with production values:
nano /opt/tombstone/infra/.env
# Set: DB_URL (Neon), REDIS_URL (Upstash), JWT_SECRET (generate new strong value)
# Set: FLAG_API_TOKEN (generate new strong value)
# Set: BEDROCK_ACCESS_KEY_ID/SECRET/REGION
# Leave PAGERDUTY_TOKEN empty if not using PagerDuty
```

- [ ] **Step 4: Run setup script**

```bash
ssh ubuntu@<oracle-public-ip>
cd /opt/tombstone
bash infra/oracle/setup.sh
```

Expected: All 5 services start, nginx configured.

- [ ] **Step 5: Enable HTTPS with Let's Encrypt**

```bash
# Point a domain at the Oracle public IP first (DNS A record)
# Then:
sudo certbot --nginx -d your-domain.com
```

- [ ] **Step 6: Verify all services**

```bash
curl https://your-domain.com/health
# Expected: {"status":"ok"}

curl https://your-domain.com/api/v1/flags
# Expected: 401 (auth required — confirms flag-api is running)
```

---

## Task 8: Live Deployment — Northflank

This task is **manual** — requires Northflank account.

- [ ] **Step 1: Create Northflank account** (northflank.com, no credit card)

- [ ] **Step 2: Create project "tombstone"**

- [ ] **Step 3: Add secret group "tombstone-secrets" with these values:**
  - `SECRET_DB_URL` = Neon connection string
  - `SECRET_REDIS_URL` = Upstash Redis URL
  - `SECRET_FLAG_API_URL` = https://your-oracle-domain (from Task 7)
  - `SECRET_BEDROCK_ACCESS_KEY_ID` = (from infra/.env)
  - `SECRET_BEDROCK_SECRET_ACCESS_KEY` = (from infra/.env)

- [ ] **Step 4: Connect GitHub repo** (Settings → Git Integrations → sairam0424/Tombstone)

- [ ] **Step 5: Create gateway service**
- New Service → Deployment → From GitHub
- Repo: sairam0424/Tombstone, Branch: main
- Dockerfile path: `services/gateway/Dockerfile`
- Docker context: `services/gateway`
- Port: 8080 (public HTTPS)
- Attach secret group: tombstone-secrets
- Plan: Sandbox (free)
- Deploy

- [ ] **Step 6: Create intelligence service**
- Same process, Dockerfile: `services/intelligence/Dockerfile`
- Context: `services/intelligence`
- Port: 8083 (public HTTPS)
- Attach secret group
- Plan: Sandbox (free)
- Deploy

- [ ] **Step 7: Note Northflank URLs and set GitHub Actions variables**

```bash
# Set in GitHub: Settings → Variables → Actions:
TOMBSTONE_GATEWAY_URL = https://tombstone-gateway--<hash>.northflank.app
TOMBSTONE_INTELLIGENCE_URL = https://tombstone-intelligence--<hash>.northflank.app
```

---

## Task 9: Cloudflare Pages — Dashboard

This task is **manual** — requires Cloudflare account.

- [ ] **Step 1: Create Cloudflare account** (cloudflare.com, free)

- [ ] **Step 2: Create Pages project**
- Workers & Pages → Pages → Create → Connect to Git
- Select sairam0424/Tombstone
- Build command: `cd workspace-dashboard && npm ci && npm run build`
- Build output: `workspace-dashboard/dist`
- Root path: `/`

- [ ] **Step 3: Set environment variables in Cloudflare Pages**
- Settings → Environment Variables → Production:
  - `VITE_API_URL` = https://your-oracle-domain
  - `VITE_GATEWAY_URL` = https://tombstone-gateway--xxx.northflank.app
  - `VITE_INTEL_URL` = https://tombstone-intelligence--xxx.northflank.app
  - `VITE_EVAL_URL` = https://your-oracle-domain
  - `VITE_SDK_TOKEN` = (your SDK token)

- [ ] **Step 4: Trigger first deploy** — Pages will build and deploy automatically

- [ ] **Step 5: Add Cloudflare secrets to GitHub Actions**

```bash
# In GitHub: Settings → Secrets → Actions:
CLOUDFLARE_API_TOKEN = <CF API token with Pages:Edit>
CLOUDFLARE_ACCOUNT_ID = <from cloudflare.com/dashboard>
TOMBSTONE_SDK_TOKEN = <same value as VITE_SDK_TOKEN>
```

---

## Task 10: Set GitHub Actions Variables + Activate Loops

- [ ] **Step 1: Set all GitHub Actions repository variables**

In github.com/sairam0424/Tombstone/settings/variables/actions:

```
TOMBSTONE_API_URL          = https://your-oracle-domain
TOMBSTONE_GATEWAY_URL      = https://tombstone-gateway--xxx.northflank.app
TOMBSTONE_INTELLIGENCE_URL = https://tombstone-intelligence--xxx.northflank.app
TOMBSTONE_EVALUATOR_URL    = https://your-oracle-domain
TOMBSTONE_PROJECT_ID       = 00000000-0000-0000-0000-000000000001
```

- [ ] **Step 2: Test loop triggers manually**

```bash
# Trigger flag-cleanup loop manually to verify it reaches intelligence service:
/opt/homebrew/bin/gh workflow run loop-flag-cleanup.yml --repo sairam0424/Tombstone
# Check run:
/opt/homebrew/bin/gh run list --repo sairam0424/Tombstone --workflow=loop-flag-cleanup.yml --limit 1
```

- [ ] **Step 3: Verify full end-to-end**

```bash
# 1. SSE stream (Northflank gateway)
curl -N "https://tombstone-gateway--xxx.northflank.app/api/v1/stream?environment=production" \
  -H "Authorization: Bearer <SDK_TOKEN>" &
# Should stay connected and print heartbeats

# 2. Create a flag (Oracle flag-api)
curl -X POST "https://your-oracle-domain/api/v1/flags" \
  -H "Authorization: Bearer <JWT>" \
  -H "Content-Type: application/json" \
  -d '{"key":"deploy-test","name":"Deploy Test","flag_type":"BOOLEAN","safe_default":"false","owner_id":"test"}'

# 3. Intelligence semantic search (Northflank)
curl "https://tombstone-intelligence--xxx.northflank.app/api/v1/search?q=deploy+test"

# 4. Dashboard loads
open https://tombstone-dashboard.pages.dev
```

---

## Verification Summary

After all tasks complete:

```bash
# Go services (Oracle)
curl https://your-oracle-domain/health

# Gateway SSE (Northflank)  
curl -I https://tombstone-gateway--xxx.northflank.app/health

# Intelligence (Northflank)
curl https://tombstone-intelligence--xxx.northflank.app/health

# Dashboard (Cloudflare Pages)
curl -I https://tombstone-dashboard.pages.dev

# GitHub Actions loops active
/opt/homebrew/bin/gh workflow list --repo sairam0424/Tombstone
```

All should return 200. Loops should show as "active" in the workflow list.
