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
)

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
	{"flags", "write"},        // POST /flags, DELETE /flags/{key}, prerequisites, schedule
	{"environments", "write"}, // PATCH /flags/{key}/environments/{env}
	{"flags", "kill_switch"},  // POST /flags/{key}/kill
	{"admin", "admin"},        // GET /compliance/export, break-glass tokens
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

		// ADMIN: everything, including audit export.
		{RoleAdmin, "admin", "admin", true, "admin exports the audit log"},
		{RoleAdmin, "flags", "kill_switch", true, "admin can kill"},
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

			mock.ExpectQuery("SELECT name, role FROM service_tokens").
				WithArgs("tok-123").
				WillReturnRows(sqlmock.NewRows([]string{"name", "role"}).AddRow("gitops-sync", c.dbRole))

			auth := NewAuthMiddleware(db, "secret")
			actor, role, ok := auth.validateServiceToken(context.Background(), "tok-123")

			if !ok {
				t.Fatal("expected token to validate")
			}
			if actor != c.wantActor {
				t.Errorf("actor = %q, want %q", actor, c.wantActor)
			}
			if role != c.wantRole {
				t.Errorf("role = %s, want %s", role, c.wantRole)
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
	mock.ExpectQuery("SELECT name, role FROM service_tokens").
		WithArgs("revoked").
		WillReturnError(sql.ErrNoRows)

	auth := NewAuthMiddleware(db, "secret")
	actor, role, ok := auth.validateServiceToken(context.Background(), "revoked")

	if ok {
		t.Fatal("revoked token must not validate")
	}
	if actor != "" || role != "" {
		t.Errorf("expected empty actor/role on failure, got %q/%s", actor, role)
	}
}
