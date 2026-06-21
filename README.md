# Tombstone — Production Intelligence Layer for Feature Flags at Scale

> You have 5,000 active flags. Something just broke. Which flag did it?

---

## The 3am Call Problem

| Scenario | Without Tombstone | With Tombstone |
|----------|-----------------|---------------|
| P0 alert fires at 3am | Page engineer, grep logs, cross-reference deploys, manually bisect 5k flags | Blast radius view pinpoints the causal flag in seconds |
| Mean time to identify | ~25 minutes | ~90 seconds |
| Rollback action | Edit YAML, open PR, wait for CI, merge, wait for deploy | One button. Circuit breaker auto-triggers at p99 threshold |
| Post-incident question | "Which flag touched this?" | Already answered — audit log with Merkle-linked entries |
| Morning retrospective | Recreate timeline from scattered logs | What-Changed feed shows exact flag state at every minute |

Tombstone eliminates the 3am call by treating feature flags as a **live causal graph of production behavior** — not a configuration file.

---

## Quick Start

```bash
git clone https://github.com/your-org/tombstone.git
cd tombstone
cp .env.example .env
make dev
```

The dashboard opens at http://localhost:3000.

---

## Service Ports

| Service | Port | Description |
|---------|------|-------------|
| gateway (SSE) | 8080 | Real-time flag stream to SDKs |
| flag-api | 8081 | REST CRUD, approval workflows, audit log |
| evaluator | 8082 | Circuit breaker, blast radius, rollback API |
| intelligence | 8083 | Anomaly detection, incident correlation, NLP search |
| gitops-sync | 8084 | GitOps sync agent (YAML -> flags) |
| dashboard | 3000 | React management UI |

---

## Architecture

```
                        +------------------+
                        |   dashboard :3000|
                        +--------+---------+
                                 |
              +------------------+------------------+
              |                  |                  |
    +---------+--------+ +-------+------+ +---------+---------+
    |   flag-api :8081  | | gateway:8080 | | evaluator  :8082  |
    |  CRUD + approvals | | SSE stream   | | circuit breaker   |
    |  audit log        | | Redis hub    | | blast radius      |
    |  tombstoning      | | snapshot     | | auto-rollback     |
    +---------+---------+ +-------+------+ +---------+---------+
              |                  |                  |
              +------------------+------------------+
                                 |
                    +------------+------------+
                    |                         |
          +---------+---------+    +----------+---------+
          | intelligence:8083 |    |  gitops-sync :8084  |
          | anomaly detect    |    |  YAML -> flag sync  |
          | incident corr.    |    |  PR auto-generate   |
          | NLP search        |    |  tombstone guard    |
          +-------------------+    +--------------------+
                    |
         +----------+----------+
         |                     |
    +----+------+    +---------+----+
    | PostgreSQL|    |    Redis     |
    | :5432     |    |    :6379     |
    +-----------+    +--------------+
```

---

## SDKs

| SDK | Package | Description |
|-----|---------|-------------|
| Node.js | `@tombstone/core` | In-process evaluation, three-tier cache, SSE sync |
| React | `@tombstone/react` | Hooks + provider, automatic re-render on flag changes |
| Python | `tombstone-python` | Async client, SSE listener, evaluation engine |
| MCP Server | `workspace-mcp` | AI coding assistant integration via stdio transport |

---

## MCP Integration

Add Tombstone to your AI assistant by adding this to `.claude/settings.json`:

```json
{
  "mcpServers": {
    "tombstone": {
      "command": "node",
      "args": ["/path/to/tombstone/workspace-mcp/dist/index.js"],
      "env": {
        "TOMBSTONE_API_URL": "http://localhost:8081",
        "TOMBSTONE_API_KEY": "your-api-key"
      }
    }
  }
}
```

Your AI assistant can then query flag state, check blast radius, and trigger rollbacks directly from a chat session.

---

## Key Features

- **Kill switch with p99 guard** — Flags auto-disable when your error rate or latency p99 crosses a configured threshold. No human in the loop required at 3am.
- **Circuit breaker** — Evaluator tracks per-flag error rates in a sliding window. Trips automatically, resets with exponential back-off.
- **Blast radius analysis** — Before you touch a flag, see exactly which user segments, services, and revenue flows it affects. BLOCKED status requires a 10-character minimum justification.
- **What-Changed feed** — Every flag state transition is Merkle-linked in an append-only audit log. Replay any minute in history to answer "what was different when the incident started?"
- **Autonomous rollout** — Thompson Sampling experiments advance automatically once statistical significance is reached (minimum 50 observations). Prevents premature rollouts on low-traffic flags.
- **GitOps sync** — Store flag definitions in YAML. The sync agent watches your repo and applies changes — but never deletes flags. Archiving is always an explicit API call.
- **Warehouse experiments** — Pull experiment results directly from your data warehouse (BigQuery, Snowflake, Redshift) without moving raw user rows. Zero-copy privacy guarantee.
- **Tombstoning** — Archived flag keys are permanently reserved. The DB constraint and service layer both block reuse, preventing the silent resurrection bug that causes production incidents months after a flag is "cleaned up."
- **Inventory limits** — Hard cap on active flags per environment. Forces regular cleanup, prevents the 5,000-flag sprawl problem from silently growing to 10,000.

---

## License

Apache-2.0
