package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// realOPARBAC builds an RBACMiddleware against the ACTUAL policies/ directory
// shipped in this repo — the one POLICY_DIR defaults to in production — instead
// of the stubbed opaEvaluator{} every other test in this file uses. That stub
// exercises the fallback permissionMatrix, which is NOT what a real deployment
// runs: POLICY_DIR defaults to "policies/", so OPA is the live decision path,
// and a policy that only approximated permissionMatrix from method+path could
// diverge from it silently — which is exactly what flags.rego did before this
// test existed (any role could GET /compliance/export and /audit; any operator
// could POST/PATCH kill-switch, break-glass, and change-request routes), with
// every SEC-1 authz test passing regardless because they all stub OPA out.
func realOPARBAC(t *testing.T) *RBACMiddleware {
	t.Helper()
	eval := newOPAEvaluator("../../policies", "data.tombstone.flags.allow", zap.NewNop())
	if !eval.available {
		t.Fatal("real policies/*.rego failed to load — is services/flag-api/policies/flags.rego present and valid?")
	}
	return &RBACMiddleware{logger: zap.NewNop(), flagsEval: eval}
}

// allPermissionPairs is every (resource, action) permissionMatrix grants to at
// least one role, deduplicated. It is the domain OPA and the Go fallback must
// agree on.
func allPermissionPairs() []Permission {
	seen := map[Permission]bool{}
	var pairs []Permission
	for _, perms := range permissionMatrix {
		for _, p := range perms {
			if !seen[p] {
				seen[p] = true
				pairs = append(pairs, p)
			}
		}
	}
	return pairs
}

// TestOPAPolicyMatchesPermissionMatrix is the regression guard for the OPA/
// fallback split-brain: it proves the REAL flags.rego and the hardcoded
// permissionMatrix produce the IDENTICAL allow/deny decision for every role
// across every known (resource, action) pair, plus one pair nobody holds (to
// catch an accidental blanket-allow rule, e.g. "role==admin => allow"
// regardless of resource/action). If a future edit loosens flags.rego — or
// tightens permissionMatrix — without updating the other, this fails.
func TestOPAPolicyMatchesPermissionMatrix(t *testing.T) {
	rbac := realOPARBAC(t)
	roles := []Role{RoleViewer, RoleOperator, RoleOwner, RoleAdmin, RoleCircuitBreaker}

	pairs := allPermissionPairs()
	pairs = append(pairs, Permission{Resource: "flags", Action: "become_root"}) // held by nobody

	for _, role := range roles {
		for _, p := range pairs {
			want := rbac.hasPermission(role, p.Resource, p.Action)

			input := map[string]interface{}{
				"role":     strings.ToLower(string(role)),
				"resource": p.Resource,
				"action":   p.Action,
			}
			got, ok := rbac.flagsEval.evaluate(context.Background(), input)
			if !ok {
				t.Fatalf("OPA evaluator reported unavailable mid-test for %s %s:%s", role, p.Resource, p.Action)
			}
			if got != want {
				t.Errorf("%s %s:%s — OPA allow=%v, fallback matrix allow=%v (must agree)",
					role, p.Resource, p.Action, got, want)
			}
		}
	}
}

// TestRequirePermissionDeniesViaRealOPA proves the fix end-to-end through the
// actual HTTP middleware, not just the raw policy query: a VIEWER token hitting
// a write-gated route is denied, and the decision is attributed to "opa" (not
// "fallback_matrix") — confirming the denial came from the real policy that
// POLICY_DIR loads in production, not from a stub.
func TestRequirePermissionDeniesViaRealOPA(t *testing.T) {
	rbac := realOPARBAC(t)

	handler := rbac.RequirePermission("flags", "write")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/flags", nil)
	req = req.WithContext(context.WithValue(req.Context(), ContextKeyRole, RoleViewer))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (real OPA policy must deny VIEWER flags:write)", rec.Code)
	}
	if got := rbac.PolicySource(); got != "opa" {
		t.Fatalf("PolicySource() = %q, want %q — this test is meaningless if it silently ran the fallback", got, "opa")
	}
}
