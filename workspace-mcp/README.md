# Tombstone MCP Server

Use Tombstone feature-flag management from any MCP-compatible AI coding assistant (Claude Code, Cursor, VS Code Copilot, etc.).

## Setup

### 1. Build

```bash
cd workspace-mcp
npm install
npm run build
```

### 2. Add to `.claude/settings.json`

```json
{
  "mcpServers": {
    "tombstone": {
      "command": "node",
      "args": ["/absolute/path/to/Tombstone/workspace-mcp/dist/index.js"],
      "env": {
        "TOMBSTONE_API_URL": "http://localhost:8000",
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
      "args": ["-y", "@tombstone/mcp"],
      "env": {
        "TOMBSTONE_API_URL": "http://localhost:8000",
        "TOMBSTONE_TOKEN": "your-api-token-here"
      }
    }
  }
}
```

### Environment Variables

| Variable | Required | Description |
|---|---|---|
| `TOMBSTONE_API_URL` | Yes | Base URL of the Tombstone API (e.g. `http://localhost:8000`) |
| `TOMBSTONE_TOKEN` | Yes | Bearer token for authentication (also accepted as `TOMBSTONE_API_TOKEN`) |

## Available Tools

| Tool | Description | Key Parameters |
|---|---|---|
| `tombstone_get_flag` | Get current state and metadata of a flag | `key` (dot-notation) |
| `tombstone_kill_switch` | Immediately disable a flag | `key`, `reason` (min 10 chars) |
| `tombstone_blast_radius` | Compute risk score before flipping a flag | `key`, `targetState` (bool) |
| `tombstone_list_stale_flags` | List flags untouched beyond a threshold | `days` (default 30), `limit` (default 20) |
| `tombstone_create_flag` | Create a new feature flag | `key` (dot-notation), `description` |
| `tombstone_search_flags` | NLP search across all flags | `q` (free-text query) |

## Usage Examples

Ask your AI assistant:

- "Get the current state of the `payments.checkout.v2` flag"
- "Kill switch `auth.legacy-login` — reason: CVE-2024-1234 mitigation required"
- "What is the blast radius of enabling `billing.new-invoices.enabled`?"
- "List all stale flags untouched for more than 60 days"
- "Create a flag `search.semantic.v1` owned by the search team"
- "Search for all payment-related flags that were recently disabled"
