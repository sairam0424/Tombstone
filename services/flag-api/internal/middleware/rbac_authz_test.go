package middleware

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/secrets"
)

// testPepper is a fixed pepper so the expected token_hash is deterministic.
const testPepper = "test-pepper-do-not-use-in-prod"

func testHasher(t *testing.T) *secrets.TokenHasher {
	t.Helper()
	h, err := secrets.NewTokenHasher(testPepper)
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	return h
}

// newTestRBAC builds an RBACMiddleware whose OPA evaluator is deliberately
// unavailable, so RequirePermission exercises the hardcoded permissionMatrix
// fallback path (the path that runs when no .rego policies are loaded).
func newTestRBAC() *RBACMiddleware {
	return &RBACMiddleware{
		logger:    zap.NewNop(),
		flagsEval: &opaEvaluator{}, // available=false -> fallback to matrix
	}
}

// writeGates lists the (resource, action) pairs guarding every MUTATING route in
// cmd/main.go, plus the sensitive audit-log export. SEC-1's core guarantee is
// that the default service-token role (VIEWER) is denied all of them. If someone
// adds a mutating route with a weaker gate, add it here.
var writeGates = []struct{ resource, action string }{
	{"flags", "write"},           // POST /flags, DELETE /flags/{key}, prerequisites, schedule
	{"environments", "write"},    // PATCH /flags/{key}/environments/{env}
	{"flags", "kill_switch"},     // POST /flags/{key}/kill
	{"flags", "circuit_breaker"}, // POST /flags/{key}/rollback-step (EVAL-4)
	{"admin", "admin"},           // GET /compliance/export, break-glass tokens
}

func TestViewerDeniedEveryWriteGate(t *testing.T) {
	rbac := newTestRBAC()
	for _, g := range writeGates {
		if rbac.hasPermission(RoleViewer, g.resource, g.action) {
			t.Errorf("VIEWER must NOT hold %s:%s — a read-only SDK token could mutate production", g.resource, g.action)
		}
	}
}

func TestPermissionMatrixLeastPrivilege(t *testing.T) {
	rbac := newTestRBAC()

	cases := []struct {
		role     Role
		resource string
		action   string
		want     bool
		why      string
	}{
		// VIEWER: reads only.
		{RoleViewer, "flags", "read", true, "SDK snapshot/eval hot path must keep working"},
		{RoleViewer, "audit", "read", true, "audit list is readable by any role today"},
		{RoleViewer, "flags", "write", false, "read-only token must not create/archive flags"},
		{RoleViewer, "environments", "write", false, "read-only token must not change rollout %"},
		{RoleViewer, "admin", "admin", false, "read-only token must not export the audit log"},

		// OPERATOR: day-to-day writes, but no emergency or admin powers.
		{RoleOperator, "flags", "write", true, "gitops-sync/operator create+update flags"},
		{RoleOperator, "environments", "write", true, "gitops-sync patches environments"},
		{RoleOperator, "flags", "kill_switch", false, "kill switch is OWNER/ADMIN only"},
		{RoleOperator, "admin", "admin", false, "operator must not export audit log"},

		// OWNER: adds kill switch + approvals, still not admin.
		{RoleOwner, "flags", "kill_switch", true, "evaluator auto-rollback needs this"},
		{RoleOwner, "approvals", "approve", true, "four-eyes approver"},
		{RoleOwner, "admin", "admin", false, "owner is not admin"},
		// EVAL-4 (PR #220 adversarial review): flags:circuit_breaker must
		// NOT come bundled with kill_switch -- the whole point of the new
		// permission is that an OWNER/ADMIN does not get the graduated
		// rollback-step capability "for free" the way they already hold
		// the full kill switch.
		{RoleOwner, "flags", "circuit_breaker", false, "graduated rollback-step must not be OWNER-usable -- would bypass require_approval for routine tuning"},

		// ADMIN: everything, including audit export -- but still NOT
		// circuit_breaker, for the identical reason as OWNER above.
		{RoleAdmin, "admin", "admin", true, "admin exports the audit log"},
		{RoleAdmin, "flags", "kill_switch", true, "admin can kill"},
		{RoleAdmin, "flags", "circuit_breaker", false, "graduated rollback-step must not be ADMIN-usable either -- same reasoning as OWNER"},

		// CIRCUIT_BREAKER: assignable only via service_tokens.role
		// (migration 026), never a human project-membership grant. Holds
		// ONLY circuit_breaker + read -- deliberately not write/kill_switch/
		// admin/approvals, since its sole purpose is the automated
		// rollback-step endpoint.
		{RoleCircuitBreaker, "flags", "circuit_breaker", true, "the automated rollback-step endpoint's whole reason for existing"},
		{RoleCircuitBreaker, "flags", "read", true, "sanity-checking current state before acting"},
		{RoleCircuitBreaker, "flags", "write", false, "not a general flag-administration role"},
		{RoleCircuitBreaker, "flags", "kill_switch", false, "must not also get the full, unconditional kill switch"},
		{RoleCircuitBreaker, "admin", "admin", false, "must not export the audit log"},
		{RoleCircuitBreaker, "approvals", "approve", false, "must not approve change requests"},
	}

	for _, c := range cases {
		if got := rbac.hasPermission(c.role, c.resource, c.action); got != c.want {
			t.Errorf("hasPermission(%s, %s:%s) = %v, want %v — %s",
				c.role, c.resource, c.action, got, c.want, c.why)
		}
	}
}

func TestRequirePermissionEnforcesRole(t *testing.T) {
	rbac := newTestRBAC()

	reached := false
	handler := rbac.RequirePermission("flags", "write")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}),
	)

	t.Run("VIEWER is denied with 403 and a descriptive body", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/flags/checkout/environments/production", nil)
		req = req.WithContext(context.WithValue(req.Context(), ContextKeyRole, RoleViewer))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		if reached {
			t.Fatal("handler ran despite insufficient permissions")
		}
		var body map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["required"] != "flags:write" || body["your_role"] != "VIEWER" {
			t.Errorf("body = %v, want required=flags:write your_role=VIEWER", body)
		}
	})

	t.Run("OPERATOR is allowed through", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "/api/v1/flags", nil)
		req = req.WithContext(context.WithValue(req.Context(), ContextKeyRole, RoleOperator))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK || !reached {
			t.Fatalf("status = %d reached = %v, want 200 and handler reached", rec.Code, reached)
		}
	})

	t.Run("missing role in context is denied (fail closed)", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "/api/v1/flags", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden || reached {
			t.Fatalf("status = %d reached = %v, want 403 and handler NOT reached", rec.Code, reached)
		}
	})
}

// TestCircuitBreakerPermissionExcludesHumanRoles is the regression guard for
// the HIGH-severity finding from PR #220's adversarial review: reusing
// flags:kill_switch for EVAL-4's graduated rollback-step capability would
// have let any OWNER/ADMIN bypass require_approval for a routine rollout
// change, not just genuine incident response, since those are exactly the
// roles that already hold kill_switch. flags:circuit_breaker is a distinct
// permission, held only by RoleCircuitBreaker (assignable only via
// service_tokens.role, migration 026 -- never a human project-membership
// grant). This test drives the REAL middleware chain (not just
// hasPermission's table lookup above) end to end, exactly as
// cmd/main.go's route registration does for POST /flags/{key}/rollback-step.
func TestCircuitBreakerPermissionExcludesHumanRoles(t *testing.T) {
	rbac := newTestRBAC()

	reached := false
	handler := rbac.RequirePermission("flags", "circuit_breaker")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}),
	)

	callAs := func(role Role) int {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "/api/v1/flags/checkout/rollback-step", nil)
		req = req.WithContext(context.WithValue(req.Context(), ContextKeyRole, role))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	for _, role := range []Role{RoleViewer, RoleOperator, RoleOwner, RoleAdmin} {
		if code := callAs(role); code != http.StatusForbidden {
			t.Errorf("role %s: status = %d, want 403 -- every human-assignable role must be denied the graduated rollback-step capability", role, code)
		}
		if reached {
			t.Errorf("role %s: handler ran despite insufficient permissions", role)
		}
	}

	if code := callAs(RoleCircuitBreaker); code != http.StatusOK {
		t.Errorf("RoleCircuitBreaker: status = %d, want 200 -- this is the ONE role the endpoint exists for", code)
	}
	if !reached {
		t.Error("RoleCircuitBreaker: handler did not run despite holding the required permission")
	}
}

func TestResolveRoleServiceTokenUsesPerTokenRole(t *testing.T) {
	rbac := newTestRBAC()

	cases := []struct {
		name    string
		ctxRole any // value stored under ContextKeyServiceRole; nil = absent
		want    Role
	}{
		{"absent role defaults to VIEWER", nil, RoleViewer},
		{"explicit OPERATOR is honored", RoleOperator, RoleOperator},
		{"explicit OWNER is honored", RoleOwner, RoleOwner},
		{"unknown role falls back to VIEWER", Role("SUPERUSER"), RoleViewer},
		{"empty role falls back to VIEWER", Role(""), RoleViewer},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.ctxRole != nil {
				ctx = context.WithValue(ctx, ContextKeyServiceRole, c.ctxRole)
			}
			if got := rbac.resolveRole(ctx, "sdk:my-service"); got != c.want {
				t.Errorf("resolveRole = %s, want %s", got, c.want)
			}
		})
	}
}

func TestValidateServiceTokenResolvesRoleFromDB(t *testing.T) {
	const wantProjectID = "11111111-1111-1111-1111-111111111111"
	cases := []struct {
		name      string
		dbRole    string
		wantRole  Role
		wantActor string
	}{
		{"operator token keeps write access", "OPERATOR", RoleOperator, "sdk:gitops-sync"},
		{"owner token keeps kill-switch access", "OWNER", RoleOwner, "sdk:gitops-sync"},
		{"viewer token is read-only", "VIEWER", RoleViewer, "sdk:gitops-sync"},
		{"garbage role degrades to VIEWER", "root", RoleViewer, "sdk:gitops-sync"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()

			hasher := testHasher(t)
			// SEC-4: the query must present the HASH, never the plaintext token.
			mock.ExpectQuery("SELECT name, role, project_id FROM service_tokens").
				WithArgs(hasher.Hash("tok-123")).
				WillReturnRows(sqlmock.NewRows([]string{"name", "role", "project_id"}).
					AddRow("gitops-sync", c.dbRole, wantProjectID))

			auth := NewAuthMiddleware(db, "secret", hasher, zap.NewNop())
			actor, role, projectID, ok := auth.validateServiceToken(context.Background(), "tok-123")

			if !ok {
				t.Fatal("expected token to validate")
			}
			if actor != c.wantActor {
				t.Errorf("actor = %q, want %q", actor, c.wantActor)
			}
			if role != c.wantRole {
				t.Errorf("role = %s, want %s", role, c.wantRole)
			}
			// TEN-1a: a service token is scoped to exactly one project — the
			// row's project_id, never client-suppliable.
			if projectID != wantProjectID {
				t.Errorf("projectID = %q, want %q", projectID, wantProjectID)
			}
		})
	}
}

func TestValidateServiceTokenRejectsUnknownOrRevoked(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	// Revoked/absent tokens produce no rows — the query filters revoked_at.
	hasher := testHasher(t)
	mock.ExpectQuery("SELECT name, role, project_id FROM service_tokens").
		WithArgs(hasher.Hash("revoked")).
		WillReturnError(sql.ErrNoRows)

	auth := NewAuthMiddleware(db, "secret", hasher, zap.NewNop())
	actor, role, projectID, ok := auth.validateServiceToken(context.Background(), "revoked")

	if ok {
		t.Fatal("revoked token must not validate")
	}
	if actor != "" || role != "" || projectID != "" {
		t.Errorf("expected empty actor/role/projectID on failure, got %q/%s/%q", actor, role, projectID)
	}
}
