package middleware

import (
	"net/http"
	"sort"
	"strings"
)

// The tables in this file are the INPUT side of the golden OPA decision test
// (opa_golden_test.go). The known roles and permissions are read from
// permissionMatrix rather than typed out, so a role or permission added to the
// matrix shows up as a golden diff that has to be reviewed and re-recorded on
// purpose. The odd and malformed values around them are fixed.

// opaShape is the part of a request flags.rego is supposed to IGNORE (method,
// path, actor). Pinning decisions across several shapes proves it still does.
type opaShape struct {
	Name   string `json:"name"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Actor  string `json:"actor"`
}

var opaGoldenShapes = []opaShape{
	{Name: "list-flags", Method: http.MethodGet, Path: "/api/v1/flags", Actor: "user-123"},
	{Name: "kill-flag", Method: http.MethodPost, Path: "/api/v1/flags/my-flag/kill", Actor: "user-123"},
	{Name: "patch-env-sdk-actor", Method: http.MethodPatch, Path: "/api/v1/flags/my-flag/environments/production", Actor: "sdk:svc-1"},
	{Name: "unicode-flag-key", Method: http.MethodDelete, Path: "/api/v1/flags/caf\u00e9-flag", Actor: "anonymous"},
	{Name: "export-empty-actor", Method: http.MethodGet, Path: "/compliance/export", Actor: ""},
	{Name: "bare-root-admin-named-actor", Method: "", Path: "/", Actor: "admin"},
}

// opaCheckRecord is one decision taken through checkPermissionWithOPA, the
// function RequirePermission calls on every gated request.
type opaCheckRecord struct {
	Role     string `json:"role"`
	Resource string `json:"resource"`
	Action   string `json:"action"`
	Shape    string `json:"shape"`
	Allow    bool   `json:"allow"`
	Source   string `json:"source"`
}

// opaRawRecord is one decision taken by handing an arbitrary input document
// straight to opaEvaluator.evaluate, the boundary that receives malformed or
// incomplete input.
type opaRawRecord struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
	Allow bool           `json:"allow"`
	OK    bool           `json:"ok"`
}

var (
	goldenOddRoles     = []Role{"", "UNKNOWN", "Admin", "viewer", " ADMIN"}
	goldenOddResources = []string{"unknown_resource", "", "FLAGS"}
	goldenOddActions   = []string{"become_root", ""}
	goldenUnheldPairs  = []Permission{{Resource: "flags", Action: "become_root"}, {Resource: "unknown_resource", Action: "read"}}
	goldenCraftedPairs = []Permission{{Resource: "flags:read"}, {Action: "flags:read"}, {Resource: "flags:write"}, {Resource: "flags:kill_switch"}, {Resource: "flags", Action: "read:"}, {Resource: "flags:", Action: "read"}}
	goldenLongResource = strings.Repeat("flags", 40)
	goldenDeeplyNested = goldenNestedObject(20)
)

func goldenNestedObject(depth int) map[string]any {
	node := map[string]any{"admin": true}
	for i := 0; i < depth; i++ {
		node = map[string]any{"role": node}
	}
	return node
}

func goldenKnownRoles() []Role {
	roles := make([]Role, 0, len(permissionMatrix))
	for role := range permissionMatrix {
		roles = append(roles, role)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i] < roles[j] })
	return roles
}

// goldenHeldPairs is every (resource, action) the matrix grants to at least
// one role, deduplicated and sorted so the table is deterministic.
func goldenHeldPairs() []Permission {
	seen := map[Permission]bool{}
	pairs := []Permission{}
	for _, perms := range permissionMatrix {
		for _, p := range perms {
			if !seen[p] {
				seen[p] = true
				pairs = append(pairs, p)
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Resource != pairs[j].Resource {
			return pairs[i].Resource < pairs[j].Resource
		}
		return pairs[i].Action < pairs[j].Action
	})
	return pairs
}

func goldenUniqueSorted(values []string) []string {
	set := map[string]bool{}
	for _, v := range values {
		set[v] = true
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func goldenResourcesAndActions() (resources, actions []string) {
	for _, p := range goldenHeldPairs() {
		resources = append(resources, p.Resource)
		actions = append(actions, p.Action)
	}
	return append(goldenUniqueSorted(resources), goldenOddResources...), append(goldenUniqueSorted(actions), goldenOddActions...)
}

func goldenCheckRecord(role Role, p Permission, shapeIdx int) opaCheckRecord {
	return opaCheckRecord{
		Role:     string(role),
		Resource: p.Resource,
		Action:   p.Action,
		Shape:    opaGoldenShapes[shapeIdx%len(opaGoldenShapes)].Name,
	}
}

// goldenCheckInputs builds the request-shaped table: every role the matrix
// knows against the full resource x action cross product, odd role spellings
// against the held pairs, crafted colon-joined pairs, and a sweep of every
// held pair under every request shape.
func goldenCheckInputs() []opaCheckRecord {
	known := goldenKnownRoles()
	held := goldenHeldPairs()
	resources, actions := goldenResourcesAndActions()
	records := []opaCheckRecord{}

	for ri, role := range known {
		for resi, resource := range resources {
			for ai, action := range actions {
				records = append(records, goldenCheckRecord(role, Permission{Resource: resource, Action: action}, ri+3*resi+5*ai))
			}
		}
	}
	oddRolePairs := append(append([]Permission{}, held...), goldenUnheldPairs...)
	for ri, role := range goldenOddRoles {
		for pi, p := range oddRolePairs {
			records = append(records, goldenCheckRecord(role, p, ri+pi))
		}
	}
	for ri, role := range known {
		for pi, p := range goldenCraftedPairs {
			records = append(records, goldenCheckRecord(role, p, ri+pi))
		}
	}
	for _, role := range known {
		for _, p := range held {
			for shapeIdx := range opaGoldenShapes {
				records = append(records, goldenCheckRecord(role, p, shapeIdx))
			}
		}
	}
	return records
}

func goldenRawInput(role, resource, action any) map[string]any {
	return map[string]any{"role": role, "resource": resource, "action": action}
}

func goldenRawRecord(name string, input map[string]any) opaRawRecord {
	return opaRawRecord{Name: name, Input: input}
}

// goldenRawInputs builds the malformed / missing-field table.
func goldenRawInputs() []opaRawRecord {
	records := []opaRawRecord{
		goldenRawRecord("nil-input", nil),
		goldenRawRecord("empty-object", map[string]any{}),
		goldenRawRecord("role-only", map[string]any{"role": "viewer"}),
		goldenRawRecord("no-role", map[string]any{"resource": "flags", "action": "read"}),
		goldenRawRecord("no-resource", map[string]any{"role": "admin", "action": "read"}),
		goldenRawRecord("no-action", map[string]any{"role": "admin", "resource": "flags"}),
		goldenRawRecord("well-formed-viewer-read", goldenRawInput("viewer", "flags", "read")),
		goldenRawRecord("well-formed-viewer-write", goldenRawInput("viewer", "flags", "write")),
		goldenRawRecord("well-formed-admin-admin", goldenRawInput("admin", "admin", "admin")),
		goldenRawRecord("upper-case-role-not-lowered", goldenRawInput("ADMIN", "admin", "admin")),
		goldenRawRecord("role-null", goldenRawInput(nil, "flags", "read")),
		goldenRawRecord("role-number", goldenRawInput(42.0, "flags", "read")),
		goldenRawRecord("role-bool", goldenRawInput(true, "flags", "read")),
		goldenRawRecord("role-array", goldenRawInput([]any{"admin"}, "admin", "admin")),
		goldenRawRecord("role-object", goldenRawInput(map[string]any{"admin": true}, "admin", "admin")),
		goldenRawRecord("role-deeply-nested", goldenRawInput(goldenDeeplyNested, "admin", "admin")),
		goldenRawRecord("role-nul-suffix", goldenRawInput("viewer\u0000", "flags", "read")),
		goldenRawRecord("role-cyrillic-lookalike", goldenRawInput("\u0430dmin", "admin", "admin")),
		goldenRawRecord("role-fullwidth", goldenRawInput("\uff41\uff44\uff4d\uff49\uff4e", "admin", "admin")),
		goldenRawRecord("resource-null", goldenRawInput("admin", nil, "read")),
		goldenRawRecord("resource-number", goldenRawInput("admin", 1.0, 2.0)),
		goldenRawRecord("resource-array", goldenRawInput("admin", []any{"flags"}, []any{"read"})),
		goldenRawRecord("resource-object", goldenRawInput("admin", map[string]any{"flags": true}, "read")),
		goldenRawRecord("resource-zero-width-space", goldenRawInput("admin", "flags\u200b", "read")),
		goldenRawRecord("resource-long", goldenRawInput("admin", goldenLongResource, "read")),
		goldenRawRecord("resource-huge-number", goldenRawInput("admin", 1e308, 0.5)),
		goldenRawRecord("action-null", goldenRawInput("admin", "flags", nil)),
		goldenRawRecord("action-bool", goldenRawInput("admin", "flags", false)),
		goldenRawRecord("action-object", goldenRawInput("admin", "flags", map[string]any{"read": true})),
		goldenRawRecord("action-trailing-newline", goldenRawInput("viewer", "flags", "read\n")),
		goldenRawRecord("injection-looking-resource", goldenRawInput("viewer", "flags\" or true", "read")),
		goldenRawRecord("privilege-claims-in-extra-fields", map[string]any{
			"role": "viewer", "resource": "admin", "action": "admin",
			"is_admin": true, "allow": true, "permissions": []any{"admin:admin"},
			"data": map[string]any{"tombstone": map[string]any{"flags": map[string]any{"allow": true}}},
		}),
		goldenRawRecord("method-path-actor-wrong-types", map[string]any{
			"role": "viewer", "resource": "flags", "action": "read",
			"method": 42.0, "path": "api/v1/flags", "actor": nil,
		}),
	}
	return append(records, goldenKnownRoleMalformedInputs()...)
}

// goldenKnownRoleMalformedInputs breaks each known role's otherwise-valid input
// four ways, so every role is pinned against missing and mistyped fields.
func goldenKnownRoleMalformedInputs() []opaRawRecord {
	records := []opaRawRecord{}
	for _, role := range goldenKnownRoles() {
		name := strings.ToLower(string(role))
		records = append(records,
			goldenRawRecord(name+"-missing-resource", map[string]any{"role": name, "action": "read"}),
			goldenRawRecord(name+"-missing-action", map[string]any{"role": name, "resource": "flags"}),
			goldenRawRecord(name+"-null-resource", goldenRawInput(name, nil, "read")),
			goldenRawRecord(name+"-array-action", goldenRawInput(name, "flags", []any{"read"})),
		)
	}
	return records
}
