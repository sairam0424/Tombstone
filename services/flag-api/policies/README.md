# Tombstone Flag API — OPA Policies

This directory contains Open Policy Agent (OPA) Rego policies for Tombstone's RBAC layer.

## Structure

| File | Package | Purpose |
|------|---------|---------|
| `flags.rego` | `tombstone.flags` | Controls access to flag CRUD, kill-switch, and scheduling endpoints |
| `audit.rego` | `tombstone.audit` | Controls read access to audit log endpoints |

## How It Works

The `RBACMiddleware` in `internal/middleware/rbac.go` evaluates these policies using the OPA Go SDK embedded in the binary. The evaluator is initialized at startup and watches this directory for changes via `fsnotify` — policy updates are hot-reloaded without restarting the service.

### Input document

Each request is evaluated with the following input document:

```json
{
  "method": "GET",
  "path":   ["api", "v1", "flags", "my-flag", "kill"],
  "role":   "operator",
  "actor":  "user-123"
}
```

- `method` — HTTP method (GET, POST, PATCH, DELETE, …)
- `path` — URL path split on `/`, empty segments stripped
- `role` — resolved role string, lower-cased (viewer | operator | owner | admin)
- `actor` — authenticated user/service identifier

### Main query

The evaluator runs `data.tombstone.flags.allow` for all flag-API routes and `data.tombstone.audit.allow` for audit routes. A `false` result → 403 Forbidden.

## Role hierarchy

| Role | Can do |
|------|--------|
| viewer | GET on all resources |
| operator | viewer + POST/PATCH on flags, environments, segments |
| owner | operator + kill-switch, prerequisites, scheduling |
| admin | everything (catch-all rule) |

## Fallback behavior

If the `POLICY_DIR` environment variable points to a missing or empty directory, the middleware falls back to the hardcoded permission matrix in `rbac.go`. A warning is logged on every request in fallback mode.

## Hot-reload

Any `.rego` file change in this directory triggers an asynchronous reload. The in-flight request uses the current policy; subsequent requests use the new policy. Reload errors are logged but do not crash the service — the last known-good policy is retained.

## Testing policies

```bash
# Install OPA CLI
brew install opa   # or: curl -L -o opa https://openpolicyagent.org/downloads/latest/opa_darwin_amd64

# Evaluate locally
opa eval \
  --input - \
  --data flags.rego \
  'data.tombstone.flags.allow' \
  <<'EOF'
{"method":"GET","path":["api","v1","flags"],"role":"viewer","actor":"u1"}
EOF
```
