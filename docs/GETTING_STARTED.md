# Getting Started with Tombstone

**Time to first flag: ~10 minutes**

You ran `make dev`. Here is what is running and how to use it.

---

## 1. What You Just Launched

`make dev` starts 8 services via Docker Compose plus supporting infrastructure (PostgreSQL, Redis, Kafka). Each service has a dedicated role:

| Service | Port | What it does |
|---------|------|-------------|
| **flag-api** | 8081 | REST CRUD for flags, environments, rollouts, approvals, audit log, tombstoning, kill switches, prerequisites, variations, and scheduled changes |
| **gateway** | 8080 | Streams real-time flag updates to SDKs via Server-Sent Events (SSE); backed by Redis Streams consumer groups |
| **evaluator** | 8082 | Computes blast radius (BLOCKED/HIGH/MEDIUM/LOW), enforces circuit breakers (auto-rollback at 5% error rate over 100 requests), and exposes SLO endpoint |
| **intelligence** | 8083 | Python anomaly detection (3-model ensemble), rollout recommendations (LinUCB contextual bandit), experiment analysis (CUPED + mSPRT), incident correlation, and hybrid NLP flag search |
| **gitops-sync** | 8084 | Syncs YAML-as-code flag definitions from a git repository into flag-api |
| **ast-rewriter** | 8085 | Scans codebases for dead flag references and rewrites them via jscodeshift |
| **marketplace** | 8086 | Integration registry for Slack, Datadog, PagerDuty, OpsGenie, Jira, Linear, and OpenTelemetry |
| **dashboard** | 3000 | React 19 management UI — the primary human interface for everything above |

PostgreSQL (5432), Redis (6379), and Kafka (9092) are also running as shared infrastructure.

---

## 2. Your First Feature Flag

### Open the dashboard

Navigate to [http://localhost:3000](http://localhost:3000).

### Create a new flag

1. Click **New Flag** (top-right button or use `Cmd+K` and type "new flag").
2. Fill in the form:
   - **Key:** `my-first-flag` (lowercase, hyphens only — this is permanent)
   - **Name:** `My First Flag`
   - **Description:** `Testing Tombstone for the first time`
   - **Type:** `BOOLEAN`
3. Click **Create Flag**.

### Enable it in development

1. On the flag detail page, select the **development** environment tab.
2. Toggle the flag to **Enabled**.
3. Set **Rollout Percentage** to `100%`.
4. Click **Save Changes**.

The change is immediately visible in the audit log and streamed to any connected SDKs.

---

## 3. Using It in Code

All examples below use the local flag-api at `http://localhost:8081` with the development SDK token.

**Default dev token:** `sdk-dev-token-change-in-prod`

### TypeScript (Node.js)

```typescript
import { TombstoneClient } from "@tombstone/core";

const client = new TombstoneClient({
  apiUrl: "http://localhost:8081",
  sdkToken: "sdk-dev-token-change-in-prod",
  environment: "development",
});

await client.initialize();

const enabled = await client.getBooleanValue("my-first-flag", false);
console.log("my-first-flag:", enabled); // true
```

### Python

```python
import httpx

response = httpx.get(
    "http://localhost:8081/api/v1/flags/my-first-flag/evaluate",
    headers={"Authorization": "Bearer sdk-dev-token-change-in-prod"},
    params={"environment": "development"},
)
result = response.json()
print("my-first-flag:", result["value"])  # True
```

### React

```tsx
import { TombstoneProvider, useFlag } from "@tombstone/react";

function App() {
  return (
    <TombstoneProvider
      apiUrl="http://localhost:8081"
      sdkToken="sdk-dev-token-change-in-prod"
      environment="development"
    >
      <MyFeature />
    </TombstoneProvider>
  );
}

function MyFeature() {
  const enabled = useFlag("my-first-flag", false);
  return enabled ? <NewCheckout /> : <OldCheckout />;
}
```

---

## 4. Dashboard Tour

| View | Path | What it shows |
|------|------|--------------|
| **Flags** | `/flags` | Full flag list with search, filters by environment/type/status, and bulk actions |
| **Flag Detail** | `/flags/:key` | Per-environment rollout controls, variations, prerequisites, scheduled changes, and circuit breaker status |
| **Audit Log** | `/audit` | Append-only Merkle-linked history of every flag change — who changed what, when, and from which value |
| **Blast Radius** | `/blast-radius` | Real-time risk scores (BLOCKED/HIGH/MEDIUM/LOW) for all active flags, with dependency graph |
| **Experiments** | `/experiments` | A/B test results with CUPED variance reduction and mSPRT sequential testing |
| **Incidents** | `/incidents` | Causal incident correlation — shows which flags changed near production anomalies |
| **Approvals** | `/approvals` | Change requests pending four-eyes sign-off |
| **Tombstones** | `/tombstones` | Permanently archived flag keys (Knight Capital prevention) |
| **Marketplace** | `/marketplace` | Configure integrations: Slack alerts, Datadog metrics, PagerDuty incidents |
| **Settings** | `/settings` | Environments, SDK tokens, team members, OPA RBAC policies |

Use `Cmd+K` (or `Ctrl+K` on Windows/Linux) to open the command palette from any view.

---

## 5. Common Workflows

### Canary Release (0% -> 10% -> 50% -> 100%)

1. Create a flag (e.g., `checkout-v2`, type BOOLEAN).
2. Enable it in **production** with rollout at **0%** — flag is live but serving no users.
3. After deployment, raise rollout to **10%**. Monitor your dashboards for 30 minutes.
4. If metrics are clean, raise to **50%**. Wait another monitoring window.
5. Raise to **100%** when confident.
6. Once 100% has been stable for 7+ days, archive the flag via the **Tombstone** action to keep the codebase clean.

### Kill Switch (Enable, Monitor, Disable)

A kill switch is a flag you enable to disable a feature — the canonical "big red button."

1. Create a flag (e.g., `disable-new-payment-flow`, type BOOLEAN). Leave it **disabled** by default.
2. In an incident, navigate to the flag detail page and toggle **Enabled** in production.
3. Set rollout to **100%**. The SDK streams the update within milliseconds.
4. Monitor the incident. When resolved, toggle back to **Disabled**.

The evaluator's circuit breaker can also trigger this automatically: if error rate exceeds 5% over 100 requests, the flag is auto-rolled back and an incident signal is written.

### Four-Eyes Approval (Change Request + Approve)

For production changes that require a second human:

1. On a flag's detail page, click **Request Change** instead of saving directly.
2. Fill in the change description and expected impact.
3. A change request appears in **Approvals** (`/approvals`).
4. A second team member with the `approver` role opens the change request and clicks **Approve**.
5. The change is applied automatically after approval.

Approvals are enforced by OPA policy (`services/flag-api/policies/flags.rego`). The policy can be tuned without a service restart.

### Break-Glass (Create Emergency Token)

For on-call scenarios where the approval workflow would take too long:

1. Navigate to **Settings** > **SDK Tokens**.
2. Click **Create Break-Glass Token**.
3. Set scope (environment + flag keys or `*`), expiry (e.g., 4 hours), and an incident reference.
4. Copy the token — it is shown once.
5. Use the token in any SDK or direct API call to bypass normal approval gates.
6. The token and all actions taken with it are recorded in the audit log.

---

## 6. Useful Commands

### Stack management

```bash
# Check which services are up and their ports
scripts/dev-local.sh status

# Tail logs for a specific service
scripts/dev-local.sh logs flag-api
scripts/dev-local.sh logs gateway
scripts/dev-local.sh logs intelligence

# Stop the full stack
make down

# Restart from scratch (clears volumes)
make down && make dev
```

### Direct API calls (no SDK)

```bash
# List all flags
curl -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  http://localhost:8081/api/v1/flags

# Evaluate a flag
curl -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  "http://localhost:8081/api/v1/flags/my-first-flag/evaluate?environment=development"

# Check evaluator blast radius
curl http://localhost:8082/api/v1/blast-radius/my-first-flag
```

### Dashboard shortcuts

| Shortcut | Action |
|----------|--------|
| `Cmd+K` | Open command palette |
| `Cmd+K` then type flag key | Jump to flag detail |
| `Cmd+K` then "new flag" | Open create flag form |

---

## 7. Next Steps

- **[GLOSSARY.md](./GLOSSARY.md)** — definitions for every term used in Tombstone (Blast Radius, Circuit Breaker, Tombstoning, etc.)
- **Architecture deep-dive** — read `ARCHITECTURE.md` in the repo root for the full causal graph model and service interaction patterns
- **SDK reference** — `packages/sdks/@tombstone/core/README.md` for the full TypeScript SDK API including `TombstoneTestClient` for deterministic tests
- **Production setup** — `infra/` for Helm charts and Terraform modules when you are ready to deploy beyond local dev
