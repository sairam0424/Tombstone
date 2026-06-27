# @tombstone/cli

CLI for Tombstone feature flag management. Supports listing, inspecting, enabling, disabling, and rolling out flags across environments.

## Install

```bash
npm install -g @tombstone/cli
```

## Authentication

For local development, start the stack with `make dev` first (from the repo root), then set:

```bash
export TOMBSTONE_API_URL=http://localhost:8081
export TOMBSTONE_TOKEN=your-token-here
```

| Variable | Description |
|---|---|
| `TOMBSTONE_API_URL` | Base URL of the flag-api service (default: `http://localhost:8081`) |
| `TOMBSTONE_TOKEN` | Bearer token for authentication |

## Commands

### `tombstone flags list`

List all feature flags, optionally filtered by project and environment.

```bash
tombstone flags list [--project <id>] [--env <env>]
```

**Options:**

| Option | Description |
|---|---|
| `--project <id>` | Filter by project ID |
| `--env <env>` | Filter by environment (`development`, `staging`, `production`) |

**Example:**

```bash
tombstone flags list --project my-app --env production
```

---

### `tombstone flags get <key>`

Show full details for a single flag as JSON.

```bash
tombstone flags get <key>
```

**Example:**

```bash
tombstone flags get checkout-v2
```

---

### `tombstone flags enable <key>`

Enable a flag in the specified environment at 100% rollout.

```bash
tombstone flags enable <key> --env <env>
```

**Required options:**

| Option | Description |
|---|---|
| `--env <env>` | Target environment (`development`, `staging`, `production`) |

**Example:**

```bash
tombstone flags enable checkout-v2 --env staging
```

---

### `tombstone flags disable <key>`

Kill switch — immediately disable a flag in the specified environment. Sets `enabled: false` and records `manual_kill_switch` as the reason in the audit log.

```bash
tombstone flags disable <key> --env <env>
```

**Required options:**

| Option | Description |
|---|---|
| `--env <env>` | Target environment |

**Example:**

```bash
tombstone flags disable checkout-v2 --env production
```

---

### `tombstone flags flip <key>`

Set an arbitrary rollout percentage (0–100) for a flag. Supports `--dry-run` to preview without making changes.

```bash
tombstone flags flip <key> --env <env> --pct <n> [--dry-run]
```

**Required options:**

| Option | Description |
|---|---|
| `--env <env>` | Target environment |
| `--pct <n>` | Rollout percentage, 0–100 |

**Optional:**

| Option | Description |
|---|---|
| `--dry-run` | Print what would happen without calling the API |

**Examples:**

```bash
# Canary rollout to 10%
tombstone flags flip new-payment-flow --env production --pct 10

# Preview a full enable without committing
tombstone flags flip new-payment-flow --env production --pct 100 --dry-run

# Set to 0% (equivalent to disabling, without the kill-switch audit reason)
tombstone flags flip new-payment-flow --env production --pct 0
```

---

## Quick Reference

```
tombstone flags list   --project <id> --env <env>
tombstone flags get    <key>
tombstone flags enable <key> --env <env>
tombstone flags disable <key> --env <env>
tombstone flags flip   <key> --env <env> --pct <n> [--dry-run]
```

## Version

**2.0.1**
