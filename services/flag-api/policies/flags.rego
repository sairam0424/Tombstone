package tombstone.flags

import future.keywords.if
import future.keywords.in

default allow = false

# permissions mirrors permissionMatrix in internal/middleware/rbac.go exactly —
# same role -> allowed (resource, action) pairs. This is the SINGLE query every
# RequirePermission(resource, action) gate evaluates (rbac.go only ever
# prepares "data.tombstone.flags.allow"), for every resource — flags,
# environments, audit, admin, everything — not just flags despite the package
# name.
#
# Before this rewrite the policy derived allow/deny from method+path alone
# (e.g. "any role can GET anything", "any operator can POST/PATCH anything
# under /api/..."), which is coarser than what RequirePermission actually asks
# and was looser than permissionMatrix on every gated route. Because
# checkPermissionWithOPA returns as soon as OPA is available, that looseness
# silently overrode the fallback matrix in any normal deployment (POLICY_DIR
# defaults to "policies/", which ships) — a viewer token could read the signed
# compliance export and the full audit log, and an operator token could
# kill-switch flags, mint break-glass tokens, and approve its own change
# requests. Keying allow on the actual (resource, action) pair — the same two
# strings the Go caller already computed — removes the need to approximate
# anything from the URL shape, and TestOPAPolicyMatchesPermissionMatrix
# (internal/middleware/opa_policy_test.go) fails CI if this map and
# permissionMatrix ever diverge again.
permissions := {
	"viewer": {
		"flags:read", "segments:read", "environments:read",
		"audit:read", "experiments:read",
	},
	"operator": {
		"flags:read", "flags:write",
		"segments:read", "segments:write",
		"environments:read", "environments:write",
		"audit:read",
	},
	"owner": {
		"flags:read", "flags:write", "flags:kill_switch",
		"segments:read", "segments:write",
		"environments:read", "environments:write",
		"approvals:read", "approvals:approve",
		"audit:read", "experiments:read", "experiments:write",
	},
	"admin": {
		"flags:read", "flags:write", "flags:kill_switch",
		"segments:read", "segments:write",
		"environments:read", "environments:write",
		"approvals:read", "approvals:approve",
		"audit:read", "experiments:read", "experiments:write",
		"admin:admin",
	},
}

allow if {
	sprintf("%s:%s", [input.resource, input.action]) in permissions[input.role]
}
