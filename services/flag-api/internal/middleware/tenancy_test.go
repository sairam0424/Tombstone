package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"go.uber.org/zap"
)

// requireProjectIDProbe wraps RequireProjectID with a terminal handler that
// records the project_id it observes in context, so tests can assert both
// the HTTP outcome and what got resolved.
func requireProjectIDProbe(rbac *RBACMiddleware) (http.Handler, *string) {
	var seen string
	h := rbac.RequireProjectID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pid, _ := ProjectIDFromContext(r.Context())
		seen = pid
		w.WriteHeader(http.StatusOK)
	}))
	return h, &seen
}

const (
	testActorAlice = "alice@example.com"
	testProjectA   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

func TestRequireProjectID_ServiceTokenUsesOwnProjectIgnoringHeader(t *testing.T) {
	rbac := &RBACMiddleware{logger: zap.NewNop()}
	handler, seen := requireProjectIDProbe(rbac)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
	// A service token's project comes from its own DB row (set by auth.go at
	// validation time), never from a header — a compromised or forged header
	// must not be able to widen which project a service token can touch.
	req.Header.Set("X-Project-Id", "attacker-supplied-project")
	req = req.WithContext(context.WithValue(req.Context(), ContextKeyServiceProjectID, testProjectA))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if *seen != testProjectA {
		t.Errorf("resolved project_id = %q, want %q (the token's own project, not the header)", *seen, testProjectA)
	}
}

func TestRequireProjectID_HumanCallerMissingHeaderIsRejected(t *testing.T) {
	rbac := &RBACMiddleware{logger: zap.NewNop()}
	handler, _ := requireProjectIDProbe(rbac)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
	req = req.WithContext(context.WithValue(req.Context(), ContextKeyActor, testActorAlice))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (X-Project-Id required)", rec.Code)
	}
}

func TestRequireProjectID_HumanCallerWithMembershipIsAccepted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(testActorAlice, testProjectA).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	rbac := &RBACMiddleware{db: db, logger: zap.NewNop()}
	handler, seen := requireProjectIDProbe(rbac)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
	req.Header.Set("X-Project-Id", testProjectA)
	req = req.WithContext(context.WithValue(req.Context(), ContextKeyActor, testActorAlice))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if *seen != testProjectA {
		t.Errorf("resolved project_id = %q, want %q", *seen, testProjectA)
	}
}

// TestRequireProjectID_HumanCallerWithoutMembershipIsDenied is the core TEN-1a
// guarantee: the header alone proves nothing. A caller naming a project they
// hold no user_roles row in must be denied, not silently trusted.
func TestRequireProjectID_HumanCallerWithoutMembershipIsDenied(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(testActorAlice, "someone-elses-project").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	rbac := &RBACMiddleware{db: db, logger: zap.NewNop()}
	handler, _ := requireProjectIDProbe(rbac)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
	req.Header.Set("X-Project-Id", "someone-elses-project")
	req = req.WithContext(context.WithValue(req.Context(), ContextKeyActor, testActorAlice))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (not a member of that project)", rec.Code)
	}
}

// TestResolveRoleIsScopedPerProject is the regression guard for the
// nondeterminism bug this fix also closes: user_roles is keyed by
// (user_id, project_id), so the SAME actor legitimately holds DIFFERENT roles
// in different projects. Before TEN-1a, resolveRole queried by user_id alone,
// so which row Postgres happened to return (and therefore which role applied)
// was undefined whenever a user belonged to more than one project.
func TestResolveRoleIsScopedPerProject(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	const projectB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	mock.ExpectQuery("SELECT role FROM user_roles").
		WithArgs(testActorAlice, testProjectA).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("OWNER"))
	mock.ExpectQuery("SELECT role FROM user_roles").
		WithArgs(testActorAlice, projectB).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("VIEWER"))

	rbac := &RBACMiddleware{db: db, logger: zap.NewNop()}

	ctxA := context.WithValue(context.Background(), ContextKeyProjectID, testProjectA)
	if got := rbac.resolveRole(ctxA, testActorAlice); got != RoleOwner {
		t.Errorf("role in project A = %s, want OWNER", got)
	}

	ctxB := context.WithValue(context.Background(), ContextKeyProjectID, projectB)
	if got := rbac.resolveRole(ctxB, testActorAlice); got != RoleViewer {
		t.Errorf("role in project B = %s, want VIEWER", got)
	}
}

// TestResolveRole_NoResolvedProjectDefaultsToViewer proves resolveRole never
// guesses a project when RequireProjectID has not (or could not) run.
func TestResolveRole_NoResolvedProjectDefaultsToViewer(t *testing.T) {
	rbac := &RBACMiddleware{logger: zap.NewNop()}
	if got := rbac.resolveRole(context.Background(), testActorAlice); got != RoleViewer {
		t.Errorf("resolveRole with no project in context = %s, want VIEWER", got)
	}
}
