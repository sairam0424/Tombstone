# Tombstone MCP Server

**v2.0.1** — Use Tombstone feature-flag management from any MCP-compatible AI coding assistant (Claude Code, Cursor, VS Code Copilot, etc.).

## Setup

### 1. Build

```bash
cd workspace-mcp
npm install
npm run build
```

### 2. Configure your AI assistant

#### Claude Desktop (`~/Library/Application Support/Claude/claude_desktop_config.json`)

```json
{
  "mcpServers": {
    "tombstone": {
      "command": "node",
      "args": ["/absolute/path/to/Tombstone/workspace-mcp/dist/index.js"],
      "env": {
        "TOMBSTONE_API_URL": "http://localhost:8081",
        "TOMBSTONE_TOKEN": "your-api-token-here"
      }
    }
  }
}
```

#### Cursor (`.cursor/mcp.json` in project root or `~/.cursor/mcp.json` globally)

```json
{
  "mcpServers": {
    "tombstone": {
      "command": "node",
      "args": ["/absolute/path/to/Tombstone/workspace-mcp/dist/index.js"],
      "env": {
        "TOMBSTONE_API_URL": "http://localhost:8081",
        "TOMBSTONE_TOKEN": "your-api-token-here"
      }
    }
  }
}
```

#### Claude Code (`.claude/settings.json`)

```json
{
  "mcpServers": {
    "tombstone": {
      "command": "node",
      "args": ["/absolute/path/to/Tombstone/workspace-mcp/dist/index.js"],
      "env": {
        "TOMBSTONE_API_URL": "http://localhost:8081",
        "TOMBSTONE_TOKEN": "your-api-token-here"
      }
    }
  }
}
```

Or use `npx` after publishing to npm:

```json
{
  "mcpServers": {
    "tombstone": {
      "command": "npx",
      "args": ["-y", "@tomb-stone/mcp"],
      "env": {
        "TOMBSTONE_API_URL": "http://localhost:8081",
        "TOMBSTONE_TOKEN": "your-api-token-here"
      }
    }
  }
}
```

### Transport

v2.0.1 uses **Streamable HTTP** (the current MCP standard transport). The endpoint is:

```
POST /api/mcp/mcp
```

Legacy SSE transport is not supported. Ensure your MCP client is on a version that supports Streamable HTTP.

### Environment Variables

| Variable | Required | Description |
|---|---|---|
| `TOMBSTONE_API_URL` | Yes | Base URL of the Tombstone API (e.g. `http://localhost:8081`) |
| `TOMBSTONE_TOKEN` | Yes | Bearer token for authentication (also accepted as `TOMBSTONE_API_TOKEN`) |

## Available Tools

All 11 tools are available as of v2.1.0. Tools marked **v2** were added in v2.0.0; **v2.1** tools were added after v2.0.1.

| Tool | Description | Key Parameters |
|---|---|---|
| `tombstone_get_flag` | Fetch flag metadata by key | `key` (dot-notation) |
| `tombstone_kill_switch` | Emergency disable a flag immediately | `key`, `reason` (min 10 chars) |
| `tombstone_blast_radius` | Risk analysis before flipping a flag (returns BLOCKED / HIGH / MEDIUM / LOW) | `key`, `targetState` (bool), `environment` (optional) |
| `tombstone_list_stale_flags` | List cleanup candidates by inactivity window | `days` (default 30), `limit` (default 20) |
| `tombstone_create_flag` | Create a new feature flag | `key` (dot-notation), `description` |
| `tombstone_search_flags` | **v2** — NLP semantic search across all flags (pgvector-powered) | `q` (free-text query) |
| `tombstone_generate_cleanup_pr` | **v2** — Generate a PR spec for dead-code removal via ast-rewriter | `key`, `repo` (optional) |
| `tombstone_openfeature_setup` | **v2** — Setup instructions for the OpenFeature SDK | `language` (`typescript` or `python`) |
| `tombstone_get_dependency_graph` | **v2** — Dependency graph (nodes + weighted edges) for a flag, up to `depth` hops | `flag_key`, `depth` (default 1), `environment` (optional) |
| `tombstone_propose_change_request` | **v2.1** — Propose a governed change through the four-eyes approval queue instead of writing directly | `flag_key`, `environment`, `enabled`, `rollout_pct` |
| `tombstone_list_change_requests` | **v2.1** — List change requests in the approval queue | `status` (optional, default `PENDING`) |

### Tool Details

#### `tombstone_get_flag`
Returns current state, metadata, owner, rollout percentage, and recent audit entries for a flag.

#### `tombstone_kill_switch`
Immediately sets a flag to `false` and writes an audit log entry. The `reason` field must be at least 10 characters — enforced to prevent blank emergency actions from appearing in incident timelines.

#### `tombstone_blast_radius`
Computes the risk tier before you flip a flag, based on the flag's CURRENT rollout percentage for the given environment (not the requested new state — the risk being measured is how much traffic is currently exposed and would be affected by the change), real evaluation-error-rate telemetry, and the dependency graph. Per the evaluator's real thresholds:
- `BLOCKED` — currently affects ≥50% of traffic AND (historical error rate >5% OR confidence is LOW, i.e. too little real traffic to trust the error rate either way)
- `HIGH` — currently affects ≥25% of traffic, or has more than 5 dependent flags
- `MEDIUM` — currently affects ≥10% of traffic, or has more than 2 dependent flags
- `LOW` — below all of the above thresholds

`environment` defaults to `production` if omitted.

#### `tombstone_list_stale_flags`
Returns flags that have not been evaluated or modified within the configured window. Useful for scheduling cleanup sprints. The `days` parameter controls the inactivity threshold (default: 30).

#### `tombstone_create_flag`
Creates a new flag in the disabled state. Keys must use dot-notation (`team.feature.variant`). Returns the new flag's full metadata.

#### `tombstone_search_flags` (v2)
Uses pgvector semantic embeddings (generated by the `intelligence` service) to find flags matching a natural-language query. More useful than a key prefix search when you don't know the exact flag name — e.g. "all payment-related flags that were disabled last month".

#### `tombstone_generate_cleanup_pr` (v2)
Given a tombstoned or stale flag key, generates a structured PR spec describing every code site that references the flag and the AST rewrites needed to remove the dead branch. Uses the `ast-rewriter` engine in the `intelligence` service. Returns a spec you can pipe into your PR workflow or hand to a code agent.

#### `tombstone_openfeature_setup` (v2)
Returns step-by-step setup instructions for wiring the Tombstone gateway into an [OpenFeature](https://openfeature.dev/) provider. Pass `language: "typescript"` or `language: "python"` to get language-specific code snippets.

#### `tombstone_get_dependency_graph` (v2)
Returns the dependency graph (nodes + weighted edges) for a flag, traversing up to `depth` hops (default 1, max 5) via the intelligence service. Use it to see flag coupling and identify high-risk dependencies before making a change. `environment` defaults to `production`.

#### `tombstone_propose_change_request` (v2.1)
Proposes a governed change to a flag's `enabled` state and/or `rollout_pct`, routed through Tombstone's four-eyes approval queue instead of writing directly. Pairs naturally with `tombstone_blast_radius` — compute the risk first, then propose a gated change instead of applying it unilaterally.

This tool deliberately omits approve/reject. That said: the real security boundary is the flag-api backend, not this omission — `ApproveChangeRequest` independently rejects self-approval (`requested_by == actor`) and requires OWNER/ADMIN-tier `approvals:approve` permission on every call, regardless of what tools any MCP server exposes. Not exposing approve/reject here is good least-privilege practice, but removing it would not, by itself, create a vulnerability, and adding it back would not, by itself, break self-approval protection.

**Known limitation, not introduced by this tool**: the self-approval check compares *credential* identity, not *human* identity. This MCP server authenticates every call with one static `TOMBSTONE_TOKEN`, resolved server-side to a fixed `sdk:<name>` actor — the same string for every human using this MCP server, distinct from any individual person's own dashboard login (`sub` claim). A person with BOTH agent/MCP access and their own dashboard login could propose a change via this tool (attributed to `sdk:<name>`) and then approve it themselves under their personal dashboard identity — the two identity strings differ, so the guard doesn't fire. This is an inherent property of any identity-string-based self-approval check, not something specific to this repo; be aware of it if `TOMBSTONE_TOKEN` is ever shared across a team rather than issued per-person.

#### `tombstone_list_change_requests` (v2.1)
Lists change requests in the four-eyes approval queue, optionally filtered by `status` (`PENDING`/`APPROVED`/`REJECTED`/`APPLIED`; defaults to `PENDING`).

## Usage Examples

Ask your AI assistant:

- "Get the current state of the `payments.checkout.v2` flag"
- "Kill switch `auth.legacy-login` — reason: CVE-2024-1234 mitigation required"
- "What is the blast radius of enabling `billing.new-invoices.enabled`?"
- "List all stale flags untouched for more than 60 days"
- "Create a flag `search.semantic.v1` owned by the search team"
- "Search for all payment-related flags that were recently disabled"
- "Generate a cleanup PR spec for the tombstoned `payments.old-checkout` flag"
- "Show me how to set up OpenFeature with Tombstone in TypeScript"
- "Show me the dependency graph for `payments.checkout.v2` up to 3 hops"
- "Propose disabling `billing.new-invoices.enabled` in production for review instead of killing it directly"
- "List all pending change requests"

## Changelog

### v2.1.0
- Added `tombstone_propose_change_request` — propose a governed change through the four-eyes approval queue
- Added `tombstone_list_change_requests` — list change requests in the approval queue
- Fix: `tombstone_blast_radius` previously sent its request to flag-api (which has no `/api/v1/blast-radius` route) with unrecognized parameter names (`key`/`targetState` instead of `flag_key`/`environment`/`rollout_pct`) — every call silently computed blast radius for an empty flag key against the evaluator's own default, ignoring the flag actually asked about. Now correctly targets the evaluator service with the flag's real current rollout percentage.
- Docs: corrected `tombstone_blast_radius`'s risk-tier thresholds (previously invented — the real gate is traffic-percentage- and dependent-flag-count-based, not "circuit breaker state" or ">10% of traffic"); added the previously-undocumented `tombstone_get_dependency_graph` tool.

### v2.0.1
- Fix: Streamable HTTP transport endpoint path corrected to `/api/mcp/mcp`
- Fix: `TOMBSTONE_API_URL` default updated to port `8081` (flag-api)

### v2.0.0
- Added `tombstone_search_flags` — pgvector-powered NLP semantic search
- Added `tombstone_generate_cleanup_pr` — ast-rewriter-based dead-code PR spec generation
- Added `tombstone_openfeature_setup` — OpenFeature provider setup for TypeScript and Python
- Added `tombstone_get_dependency_graph` — dependency graph traversal (undocumented in this changelog until v2.1.0, but shipped in v2.0.0's code)
- Migrated transport from legacy SSE to Streamable HTTP (`/api/mcp/mcp`)

### v1.0.0
- Initial release: `tombstone_get_flag`, `tombstone_kill_switch`, `tombstone_blast_radius`, `tombstone_list_stale_flags`, `tombstone_create_flag`, `tombstone_search_flags`
