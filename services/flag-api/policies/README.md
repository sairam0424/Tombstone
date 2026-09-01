# Tombstone Flag API — OPA Policies

This directory contains Open Policy Agent (OPA) Rego policies for Tombstone's RBAC layer.

## Structure

| File | Package | Purpose |
|------|---------|---------|
| `flags.rego` | `tombstone.flags` | The SINGLE RBAC policy — every `RequirePermission` gate in the API evaluates this one, for every resource (flags, environments, audit, admin, …), not just flags |

There is only one `opaEvaluator` in `RBACMiddleware` and it always prepares the query `data.tombstone.flags.allow` — a second policy file evaluated by a different query would simply never be queried. (An earlier `audit.rego`/`data.tombstone.audit.allow` existed here describing exactly that dispatch and was dead code; it was removed rather than wired up, since one resource-keyed policy already covers every resource without needing per-resource files.)

## How It Works

The `RBACMiddleware` in `internal/middleware/rbac.go` evaluates this policy using the OPA Go SDK embedded in the binary. The evaluator is initialized at startup and watches this directory for changes via `fsnotify` — policy updates are hot-reloaded without restarting the service.

### Input document

Each request is evaluated with the following input document:

```json
{
  "method":   "POST",
  "path":     ["api", "v1", "flags", "my-flag", "kill"],
  "role":     "operator",
  "actor":    "user-123",
  "resource": "flags",
  "action":   "kill_switch"
}
```

- `method` / `path` — informational (available for future path-scoped policies); the allow decision below does not use them
- `role` — resolved role string, lower-cased (viewer | operator | owner | admin)
- `actor` — authenticated user/service identifier
- `resource` / `action` — the exact permission being checked, i.e. the two strings the route passed to `RequirePermission(resource, action)`. This is what `allow` is keyed on.

### Main query

The evaluator runs `data.tombstone.flags.allow` for every gated route. A `false` result → 403 Forbidden.

`allow` looks up `"<resource>:<action>"` in a per-role permission set that must match `permissionMatrix` in `rbac.go` exactly — `internal/middleware/opa_policy_test.go`'s `TestOPAPolicyMatchesPermissionMatrix` loads this file for real and fails CI if the two ever disagree. (Deriving `allow` from `method`+`path` instead was tried first and was strictly looser than `permissionMatrix` on every gated route — see the comment at the top of `flags.rego`.)

## Role hierarchy

| Role | Can do |
|------|--------|
| viewer | read flags, segments, environments, audit, experiments |
| operator | viewer + write flags, segments, environments |
| owner | operator + kill-switch, approvals, experiments write |
| admin | owner + admin:admin (audit-log export, break-glass token management) |

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
{"method":"GET","path":["api","v1","flags"],"role":"viewer","actor":"u1","resource":"flags","action":"read"}
EOF
```
