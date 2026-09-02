package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/db"
	"github.com/tombstone/flag-api/internal/secrets"
)

// TestSCIMDeprovisionRevokesUserRoles is the executable gate for SEC-5's
// "deprovision revokes roles" fix — it runs against a real Postgres in the
// flag-api-migrations CI job and skips locally (same convention as every
// other DB-backed test in this package).
//
// Before this fix, DeprovisionUser/UpdateUser(active=false) only ever
// flipped scim_users.active — a bookkeeping column rbac.go's resolveRole
// never reads. This proves the actual RBAC grant (user_roles, keyed by the
// same email string as the login JWT's sub) is gone afterward, not just
// that scim_users looks deactivated.
func TestSCIMDeprovisionRevokesUserRoles(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed SCIM deprovision test")
	}

	database, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := db.Migrate(ctx, database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	projectID := createTestProject(ctx, t, database, "sec5-scim-revoke-test-a")
	otherProjectID := createTestProject(ctx, t, database, "sec5-scim-revoke-test-b")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	auditKey, err := secrets.NewAuditKey("sec5-scim-revoke-test-key-00000000", "")
	if err != nil {
		t.Fatalf("audit key: %v", err)
	}
	auditW := audit.NewWriter(database, auditKey)

	scimH := NewSCIMHandler(database, rdb, logger, auditW)

	grantRole := func(t *testing.T, userEmail, pID string) {
		t.Helper()
		if _, err := database.ExecContext(ctx, `
			INSERT INTO user_roles (user_id, project_id, role, granted_by) VALUES ($1, $2, 'OPERATOR', 'test-admin')
			ON CONFLICT (user_id, project_id) DO NOTHING
		`, userEmail, pID); err != nil {
			t.Fatalf("grant role: %v", err)
		}
	}
	roleCount := func(t *testing.T, userEmail string) int {
		t.Helper()
		var n int
		if err := database.QueryRowContext(ctx, `SELECT count(*) FROM user_roles WHERE user_id = $1`, userEmail).Scan(&n); err != nil {
			t.Fatalf("count roles: %v", err)
		}
		return n
	}
	createScimUser := func(t *testing.T, externalID, userEmail string) {
		t.Helper()
		if _, err := database.ExecContext(ctx, `
			INSERT INTO scim_users (external_id, user_id, email, display_name, active)
			VALUES ($1, $1, $2, $2, true)
			ON CONFLICT (external_id) DO UPDATE SET email = EXCLUDED.email, active = true
		`, externalID, userEmail); err != nil {
			t.Fatalf("create scim user: %v", err)
		}
	}

	type auditEntry struct {
		EventType string
		ProjectID sql.NullString
		NewState  []byte
	}
	auditEntriesFor := func(t *testing.T, actor string) []auditEntry {
		t.Helper()
		rows, err := database.QueryContext(ctx, `
			SELECT event_type, project_id, new_state FROM audit_log WHERE actor = $1 ORDER BY created_at
		`, actor)
		if err != nil {
			t.Fatalf("query audit_log: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var entries []auditEntry
		for rows.Next() {
			var e auditEntry
			if err := rows.Scan(&e.EventType, &e.ProjectID, &e.NewState); err != nil {
				t.Fatalf("scan audit_log row: %v", err)
			}
			entries = append(entries, e)
		}
		return entries
	}
	revocationEntriesFor := func(t *testing.T, userEmail string) []auditEntry {
		t.Helper()
		var out []auditEntry
		for _, e := range auditEntriesFor(t, "system-scim") {
			if e.EventType != "user_roles_revoked" {
				continue
			}
			var payload struct {
				User string `json:"user"`
			}
			if err := json.Unmarshal(e.NewState, &payload); err != nil {
				t.Fatalf("unmarshal user_roles_revoked payload: %v", err)
			}
			if payload.User == userEmail {
				out = append(out, e)
			}
		}
		return out
	}

	scimRequest := func(method, path, externalID string, body map[string]any) *http.Request {
		var bodyReader *strings.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal scim request body: %v", err)
			}
			bodyReader = strings.NewReader(string(raw))
		} else {
			bodyReader = strings.NewReader("")
		}
		req := httptest.NewRequest(method, path, bodyReader)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", externalID)
		return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}

	t.Run("DeprovisionUser (DELETE) revokes the role and writes a project-scoped audit entry", func(t *testing.T) {
		email := "sec5-deprovision-delete@example.com"
		createScimUser(t, "ext-delete-1", email)
		grantRole(t, email, projectID)
		if got := roleCount(t, email); got != 1 {
			t.Fatalf("setup: role count = %d, want 1", got)
		}

		rec := httptest.NewRecorder()
		scimH.DeprovisionUser(rec, scimRequest(http.MethodDelete, "/scim/v2/Users/ext-delete-1", "ext-delete-1", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
		}

		if got := roleCount(t, email); got != 0 {
			t.Errorf("role count after deprovision = %d, want 0 — the RBAC grant must not survive deprovisioning", got)
		}

		entries := revocationEntriesFor(t, email)
		if len(entries) != 1 {
			t.Fatalf("user_roles_revoked entries for %s = %d, want 1", email, len(entries))
		}
		if !entries[0].ProjectID.Valid || entries[0].ProjectID.String != projectID {
			t.Errorf("entry project_id = %v, want %q — an unscoped entry is unreachable through ListAuditLog/VerifyChain/ExportAuditLog", entries[0].ProjectID, projectID)
		}
	})

	t.Run("revoking a user with roles in TWO projects revokes both and audits both", func(t *testing.T) {
		email := "sec5-multi-project@example.com"
		createScimUser(t, "ext-multi-1", email)
		grantRole(t, email, projectID)
		grantRole(t, email, otherProjectID)
		if got := roleCount(t, email); got != 2 {
			t.Fatalf("setup: role count = %d, want 2", got)
		}

		rec := httptest.NewRecorder()
		scimH.DeprovisionUser(rec, scimRequest(http.MethodDelete, "/scim/v2/Users/ext-multi-1", "ext-multi-1", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
		}

		if got := roleCount(t, email); got != 0 {
			t.Errorf("role count after deprovision = %d, want 0 — a bug that only processes the first returned row would leave this at 1", got)
		}

		entries := revocationEntriesFor(t, email)
		if len(entries) != 2 {
			t.Fatalf("user_roles_revoked entries = %d, want 2 (one per revoked project)", len(entries))
		}
		gotProjects := map[string]bool{}
		for _, e := range entries {
			if e.ProjectID.Valid {
				gotProjects[e.ProjectID.String] = true
			}
		}
		if !gotProjects[projectID] || !gotProjects[otherProjectID] {
			t.Errorf("revoked project_ids = %v, want both %q and %q represented", gotProjects, projectID, otherProjectID)
		}
	})

	t.Run("revocation matches case-insensitively — a role granted under different casing is still revoked", func(t *testing.T) {
		email := "sec5-case-mismatch@example.com"
		grantedAs := "SEC5-Case-Mismatch@Example.com"
		createScimUser(t, "ext-case-1", email)
		grantRole(t, grantedAs, projectID)
		if got := roleCount(t, grantedAs); got != 1 {
			t.Fatalf("setup: role count = %d, want 1", got)
		}

		rec := httptest.NewRecorder()
		scimH.DeprovisionUser(rec, scimRequest(http.MethodDelete, "/scim/v2/Users/ext-case-1", "ext-case-1", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
		}

		if got := roleCount(t, grantedAs); got != 0 {
			t.Errorf("role count after deprovision = %d, want 0 — a case-sensitive match would silently no-op here", got)
		}
	})

	t.Run("UpdateUser (PUT active=false) also revokes roles — many IdPs deprovision this way", func(t *testing.T) {
		email := "sec5-deprovision-put@example.com"
		createScimUser(t, "ext-put-1", email)
		grantRole(t, email, projectID)
		if got := roleCount(t, email); got != 1 {
			t.Fatalf("setup: role count = %d, want 1", got)
		}

		req := scimRequest(http.MethodPut, "/scim/v2/Users/ext-put-1", "ext-put-1", map[string]any{
			"userName": "ext-put-1", "active": false,
			"emails": []map[string]any{{"value": email, "primary": true}},
		})
		rec := httptest.NewRecorder()
		scimH.UpdateUser(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		if got := roleCount(t, email); got != 0 {
			t.Errorf("role count after PUT active=false = %d, want 0", got)
		}
	})

	// SEC-5 adversarial review: primaryEmail(u) falls back to u.UserName
	// when the body's "emails" array is empty — a legitimate
	// deactivation-only payload some IdPs send. Revocation must use the
	// identity ALREADY on file for this external_id, never a value derived
	// from this specific request's (possibly incomplete) body.
	t.Run("UpdateUser with no emails array in the body still revokes the correct user's roles", func(t *testing.T) {
		email := "sec5-partial-body@example.com"
		createScimUser(t, "ext-partial-1", email)
		grantRole(t, email, projectID)
		if got := roleCount(t, email); got != 1 {
			t.Fatalf("setup: role count = %d, want 1", got)
		}

		req := scimRequest(http.MethodPut, "/scim/v2/Users/ext-partial-1", "ext-partial-1", map[string]any{
			"userName": "ext-partial-1", "active": false,
		})
		rec := httptest.NewRecorder()
		scimH.UpdateUser(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		if got := roleCount(t, email); got != 0 {
			t.Errorf("role count after partial-body deactivation = %d, want 0 — revocation must not have silently targeted "+
				"the request body's fallback identity (the SCIM userName) instead of the real email on file", got)
		}
		// The username itself must never have accumulated a (bogus) role
		// count of its own — proves revocation didn't accidentally operate
		// against "ext-partial-1" as if it were an email.
		if got := roleCount(t, "ext-partial-1"); got != 0 {
			t.Errorf("role count for the raw username = %d, want 0", got)
		}
	})

	t.Run("UpdateUser with active=true does not touch roles", func(t *testing.T) {
		email := "sec5-still-active@example.com"
		createScimUser(t, "ext-active-1", email)
		grantRole(t, email, projectID)

		req := scimRequest(http.MethodPut, "/scim/v2/Users/ext-active-1", "ext-active-1", map[string]any{
			"userName": "ext-active-1", "active": true,
			"emails": []map[string]any{{"value": email, "primary": true}},
		})
		rec := httptest.NewRecorder()
		scimH.UpdateUser(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		if got := roleCount(t, email); got != 1 {
			t.Errorf("role count for a still-active user = %d, want 1 (unchanged)", got)
		}
	})

	t.Run("deprovisioning a user with no roles granted is a clean no-op", func(t *testing.T) {
		email := "sec5-no-roles@example.com"
		createScimUser(t, "ext-none-1", email)

		rec := httptest.NewRecorder()
		scimH.DeprovisionUser(rec, scimRequest(http.MethodDelete, "/scim/v2/Users/ext-none-1", "ext-none-1", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
		}

		if entries := revocationEntriesFor(t, email); len(entries) != 0 {
			t.Errorf("user_roles_revoked entries for a user with no roles = %d, want 0", len(entries))
		}
	})
}
