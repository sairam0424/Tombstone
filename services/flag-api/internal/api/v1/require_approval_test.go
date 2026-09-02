package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/db"
	"github.com/tombstone/flag-api/internal/middleware"
	"github.com/tombstone/flag-api/internal/secrets"
)

// TestRequireApprovalGate is the executable gate for SEC-3b part 2 — it runs
// against a real Postgres in the flag-api-migrations CI job and skips
// locally (same convention as every other DB-backed test in this package).
// It also closes a pre-existing coverage gap: BreakGlassHandler.UseToken had
// zero tests before this, despite predating SEC-3b entirely.
func TestRequireApprovalGate(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed require_approval gate test")
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

	projectID := createTestProject(ctx, t, database, "sec3b-gate-test-a")
	otherProjectID := createTestProject(ctx, t, database, "sec3b-gate-test-b")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	auditKey, err := secrets.NewAuditKey("sec3b-gate-test-key-000000000000000", "")
	if err != nil {
		t.Fatalf("audit key: %v", err)
	}
	auditW := audit.NewWriter(database, auditKey)

	hasher, err := secrets.NewTokenHasher("sec3b-gate-test-pepper")
	if err != nil {
		t.Fatalf("token hasher: %v", err)
	}

	flagH := NewFlagHandler(database, rdb, logger, nil, auditW, hasher)
	bgH := NewBreakGlassHandler(database, rdb, logger, hasher, auditW)

	flag := createTestFlag(t, flagH, projectID, "sec3b-gate-flag")
	otherFlag := createTestFlag(t, flagH, otherProjectID, "sec3b-gate-flag-b")

	setRequireApproval := func(t *testing.T, pID string, enabled bool) {
		t.Helper()
		if _, err := database.ExecContext(ctx, `UPDATE projects SET require_approval = $1 WHERE id = $2`, enabled, pID); err != nil {
			t.Fatalf("set require_approval: %v", err)
		}
	}

	readFlagEnvOf := func(t *testing.T, pID string, f Flag) (enabled bool, rolloutPct int) {
		t.Helper()
		if err := database.QueryRowContext(ctx, `
			SELECT fe.enabled, fe.rollout_pct FROM flag_environments fe JOIN flags f ON f.id = fe.flag_id
			WHERE f.key = $1 AND fe.environment = 'production' AND f.project_id = $2
		`, f.Key, pID).Scan(&enabled, &rolloutPct); err != nil {
			t.Fatalf("read flag_environments: %v", err)
		}
		return enabled, rolloutPct
	}
	readFlagEnv := func(t *testing.T) (enabled bool, rolloutPct int) { return readFlagEnvOf(t, projectID, flag) }

	auditEventTypesFor := func(t *testing.T, flagKey string) []string {
		t.Helper()
		rows, err := database.QueryContext(ctx, `SELECT event_type FROM audit_log WHERE flag_key = $1 ORDER BY created_at`, flagKey)
		if err != nil {
			t.Fatalf("query audit_log: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var types []string
		for rows.Next() {
			var et string
			if err := rows.Scan(&et); err != nil {
				t.Fatalf("scan audit_log row: %v", err)
			}
			types = append(types, et)
		}
		return types
	}
	containsEventType := func(types []string, want string) bool {
		for _, got := range types {
			if got == want {
				return true
			}
		}
		return false
	}

	updateEnvRequestFor := func(pID string, f Flag, enabled bool, rolloutPct int, breakGlassToken string) *http.Request {
		req := changeRequestRequestAs(t, http.MethodPatch, "/api/v1/flags/"+f.Key+"/environments/production",
			map[string]any{"enabled": enabled, "rollout_pct": rolloutPct}, pID, "gate-tester",
			map[string]string{"key": f.Key, "env": "production"})
		if breakGlassToken != "" {
			req.Header.Set(breakGlassHeader, breakGlassToken)
		}
		return req
	}
	updateEnvRequest := func(enabled bool, rolloutPct int, breakGlassToken string) *http.Request {
		return updateEnvRequestFor(projectID, flag, enabled, rolloutPct, breakGlassToken)
	}

	t.Run("require_approval=false allows direct writes unchanged", func(t *testing.T) {
		rec := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec, updateEnvRequest(true, 50, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t)
		if !enabled || rolloutPct != 50 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (true, 50)", enabled, rolloutPct)
		}
	})

	t.Run("require_approval=true rejects a direct write with no break-glass token", func(t *testing.T) {
		setRequireApproval(t, projectID, true)
		defer setRequireApproval(t, projectID, false)

		rec := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec, updateEnvRequest(false, 0, ""))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
		}

		enabled, rolloutPct := readFlagEnv(t)
		if !enabled || rolloutPct != 50 {
			t.Errorf("flag_environments changed despite the gate rejecting the request: (enabled=%v, rollout_pct=%d), want unchanged (true, 50)", enabled, rolloutPct)
		}
	})

	t.Run("require_approval=true rejects an invalid break-glass token", func(t *testing.T) {
		setRequireApproval(t, projectID, true)
		defer setRequireApproval(t, projectID, false)

		rec := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec, updateEnvRequest(false, 0, "bgt_not-a-real-token"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("require_approval=true rejects an expired break-glass token", func(t *testing.T) {
		setRequireApproval(t, projectID, true)
		defer setRequireApproval(t, projectID, false)

		token := "bgt_expired_test_token_" + flag.Key
		if _, err := database.ExecContext(ctx, `
			INSERT INTO break_glass_tokens (token_hash, scope, created_by, expires_at)
			VALUES ($1, 'all-flags', 'gate-tester', now() - interval '1 hour')
		`, hasher.Hash(token)); err != nil {
			t.Fatalf("insert expired break-glass token: %v", err)
		}

		rec := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec, updateEnvRequest(false, 0, token))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a valid break-glass token bypasses the gate exactly once, audited distinctly", func(t *testing.T) {
		setRequireApproval(t, projectID, true)
		defer setRequireApproval(t, projectID, false)

		token := createTestBreakGlassToken(t, bgH, projectID, "gate-tester")

		rec := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec, updateEnvRequest(true, 33, token))
		if rec.Code != http.StatusOK {
			t.Fatalf("first use: status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t)
		if !enabled || rolloutPct != 33 {
			t.Errorf("flag_environments after bypass = (enabled=%v, rollout_pct=%d), want (true, 33)", enabled, rolloutPct)
		}

		types := auditEventTypesFor(t, flag.Key)
		if !containsEventType(types, "flag_environment_updated_via_breakglass") {
			t.Errorf("expected a flag_environment_updated_via_breakglass audit entry, types seen: %v", types)
		}
		if !containsEventType(types, "break_glass_token_used") {
			t.Errorf("expected a break_glass_token_used audit entry (searchable across every kind of break-glass use), types seen: %v", types)
		}

		// The token was consumed by the first use — it must not work again.
		rec2 := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec2, updateEnvRequest(false, 0, token))
		if rec2.Code != http.StatusGone {
			t.Fatalf("second use of the same token: status = %d, want 410; body: %s", rec2.Code, rec2.Body.String())
		}
		enabled, rolloutPct = readFlagEnv(t)
		if !enabled || rolloutPct != 33 {
			t.Errorf("flag_environments changed on the REJECTED second use: (enabled=%v, rollout_pct=%d), want unchanged (true, 33)", enabled, rolloutPct)
		}
	})

	// Regression proof for the TOCTOU race the adversarial review found in
	// the original SELECT-then-UPDATE implementation: two concurrent
	// requests racing the SAME token must not both bypass the gate.
	t.Run("concurrent uses of the same break-glass token: exactly one bypass succeeds", func(t *testing.T) {
		setRequireApproval(t, projectID, true)
		defer setRequireApproval(t, projectID, false)

		token := createTestBreakGlassToken(t, bgH, projectID, "gate-tester")

		const n = 8
		codes := make([]int, n)
		var wg sync.WaitGroup
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func(i int) {
				defer wg.Done()
				rec := httptest.NewRecorder()
				flagH.UpdateEnvironment(rec, updateEnvRequest(true, 70+i, token))
				codes[i] = rec.Code
			}(i)
		}
		wg.Wait()

		successes, gone := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusOK:
				successes++
			case http.StatusGone:
				gone++
			default:
				t.Errorf("unexpected status %d among concurrent racers, want 200 or 410", c)
			}
		}
		if successes != 1 {
			t.Errorf("successes = %d, want exactly 1 — a race in token consumption let more than one request bypass require_approval with the same token", successes)
		}
		if gone != n-1 {
			t.Errorf("410-rejected = %d, want %d", gone, n-1)
		}
	})

	// A one-shot emergency token must not be spent on a request that never
	// actually mutates anything — the whole point of moving consumption into
	// the same transaction as the write.
	t.Run("a token is not consumed when the target flag/environment does not exist", func(t *testing.T) {
		setRequireApproval(t, projectID, true)
		defer setRequireApproval(t, projectID, false)

		token := createTestBreakGlassToken(t, bgH, projectID, "gate-tester")

		req := changeRequestRequestAs(t, http.MethodPatch, "/api/v1/flags/sec3b-does-not-exist/environments/production",
			map[string]any{"enabled": true, "rollout_pct": 1}, projectID, "gate-tester",
			map[string]string{"key": "sec3b-does-not-exist", "env": "production"})
		req.Header.Set(breakGlassHeader, token)
		rec := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}

		// The SAME token must still work — it was never actually consumed.
		rec2 := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec2, updateEnvRequest(true, 44, token))
		if rec2.Code != http.StatusOK {
			t.Fatalf("token reuse after a not-found write: status = %d, want 200 (token must not have been burned); body: %s", rec2.Code, rec2.Body.String())
		}
	})

	t.Run("require_approval is scoped per-project — enabling it for one project does not block another", func(t *testing.T) {
		setRequireApproval(t, projectID, true)
		defer setRequireApproval(t, projectID, false)

		rec := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec, updateEnvRequestFor(otherProjectID, otherFlag, true, 60, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 — project B has require_approval=false and must be unaffected by project A's setting; body: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("a break-glass token created for one project cannot bypass another project's gate", func(t *testing.T) {
		setRequireApproval(t, otherProjectID, true)
		defer setRequireApproval(t, otherProjectID, false)

		tokenForProjectA := createTestBreakGlassToken(t, bgH, projectID, "gate-tester")

		rec := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec, updateEnvRequestFor(otherProjectID, otherFlag, false, 0, tokenForProjectA))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (a project-A token must not bypass project B's require_approval gate); body: %s",
				rec.Code, rec.Body.String())
		}

		// The rejected cross-project attempt must not have consumed the
		// token — it still works for the project it was actually created for.
		setRequireApproval(t, projectID, true)
		defer setRequireApproval(t, projectID, false)
		rec2 := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec2, updateEnvRequest(true, 91, tokenForProjectA))
		if rec2.Code != http.StatusOK {
			t.Fatalf("using the token for its own project after a rejected cross-project attempt: status = %d, want 200; body: %s",
				rec2.Code, rec2.Body.String())
		}
	})

	// The following exercise BreakGlassHandler.UseToken directly — this
	// endpoint predates SEC-3b entirely and had zero test coverage before.
	t.Run("UseToken validates and consumes a token exactly once", func(t *testing.T) {
		token := createTestBreakGlassToken(t, bgH, projectID, "use-tester")

		req := httptest.NewRequest(http.MethodPost, "/api/v1/break-glass/use", strings.NewReader(
			fmt.Sprintf(`{"token":%q,"used_by":"use-tester","action_description":"test action"}`, token)))
		rec := httptest.NewRecorder()
		bgH.UseToken(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("first use: status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		req2 := httptest.NewRequest(http.MethodPost, "/api/v1/break-glass/use", strings.NewReader(
			fmt.Sprintf(`{"token":%q,"used_by":"use-tester"}`, token)))
		rec2 := httptest.NewRecorder()
		bgH.UseToken(rec2, req2)
		if rec2.Code != http.StatusGone {
			t.Fatalf("second use: status = %d, want 410; body: %s", rec2.Code, rec2.Body.String())
		}
	})

	t.Run("UseToken rejects an invalid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/break-glass/use",
			strings.NewReader(`{"token":"bgt_garbage","used_by":"use-tester"}`))
		rec := httptest.NewRecorder()
		bgH.UseToken(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("UseToken is not project-scoped — it authorizes nothing on its own", func(t *testing.T) {
		token := createTestBreakGlassToken(t, bgH, projectID, "use-tester")

		req := httptest.NewRequest(http.MethodPost, "/api/v1/break-glass/use", strings.NewReader(
			fmt.Sprintf(`{"token":%q,"used_by":"use-tester"}`, token)))
		rec := httptest.NewRecorder()
		bgH.UseToken(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (UseToken deliberately ignores project association — it grants no write authority of its own); body: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("ListTokens only returns the caller's own project's tokens", func(t *testing.T) {
		_ = createTestBreakGlassToken(t, bgH, projectID, "list-tester-a")
		_ = createTestBreakGlassToken(t, bgH, otherProjectID, "list-tester-b")

		req := changeRequestRequestAs(t, http.MethodGet, "/api/v1/break-glass/tokens", nil, projectID, "list-tester-a", nil)
		rec := httptest.NewRecorder()
		bgH.ListTokens(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Tokens []struct {
				CreatedBy string `json:"created_by"`
			} `json:"tokens"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode list-tokens response: %v", err)
		}
		for _, tok := range resp.Tokens {
			if tok.CreatedBy == "list-tester-b" {
				t.Errorf("project A's ListTokens returned a token created for project B — cross-tenant leak")
			}
		}
	})
}

func createTestBreakGlassToken(t *testing.T, h *BreakGlassHandler, projectID, createdBy string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/break-glass/tokens", strings.NewReader(
		fmt.Sprintf(`{"created_by":%q,"expires_in_hours":4}`, createdBy)))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), middleware.ContextKeyProjectID, projectID)
	ctx = context.WithValue(ctx, middleware.ContextKeyActor, createdBy)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.CreateToken(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("createTestBreakGlassToken status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode create-token response: %v", err)
	}
	return resp.Token
}
