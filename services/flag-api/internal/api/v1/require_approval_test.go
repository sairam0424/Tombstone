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

	projectID := createTestProject(ctx, t, database, "sec3b-gate-test")

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

	setRequireApproval := func(t *testing.T, enabled bool) {
		t.Helper()
		if _, err := database.ExecContext(ctx, `UPDATE projects SET require_approval = $1 WHERE id = $2`, enabled, projectID); err != nil {
			t.Fatalf("set require_approval: %v", err)
		}
	}

	readFlagEnv := func(t *testing.T) (enabled bool, rolloutPct int) {
		t.Helper()
		if err := database.QueryRowContext(ctx, `
			SELECT fe.enabled, fe.rollout_pct FROM flag_environments fe JOIN flags f ON f.id = fe.flag_id
			WHERE f.key = $1 AND fe.environment = 'production' AND f.project_id = $2
		`, flag.Key, projectID).Scan(&enabled, &rolloutPct); err != nil {
			t.Fatalf("read flag_environments: %v", err)
		}
		return enabled, rolloutPct
	}

	updateEnvRequest := func(enabled bool, rolloutPct int, breakGlassToken string) *http.Request {
		req := changeRequestRequestAs(t, http.MethodPatch, "/api/v1/flags/"+flag.Key+"/environments/production",
			map[string]any{"enabled": enabled, "rollout_pct": rolloutPct}, projectID, "gate-tester",
			map[string]string{"key": flag.Key, "env": "production"})
		if breakGlassToken != "" {
			req.Header.Set(breakGlassHeader, breakGlassToken)
		}
		return req
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
		setRequireApproval(t, true)
		defer setRequireApproval(t, false)

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
		setRequireApproval(t, true)
		defer setRequireApproval(t, false)

		rec := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec, updateEnvRequest(false, 0, "bgt_not-a-real-token"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("require_approval=true rejects an expired break-glass token", func(t *testing.T) {
		setRequireApproval(t, true)
		defer setRequireApproval(t, false)

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

	t.Run("a valid break-glass token bypasses the gate exactly once", func(t *testing.T) {
		setRequireApproval(t, true)
		defer setRequireApproval(t, false)

		token := createTestBreakGlassToken(t, bgH, "gate-tester")

		rec := httptest.NewRecorder()
		flagH.UpdateEnvironment(rec, updateEnvRequest(true, 33, token))
		if rec.Code != http.StatusOK {
			t.Fatalf("first use: status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t)
		if !enabled || rolloutPct != 33 {
			t.Errorf("flag_environments after bypass = (enabled=%v, rollout_pct=%d), want (true, 33)", enabled, rolloutPct)
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

	// The following exercise BreakGlassHandler.UseToken directly — this
	// endpoint predates SEC-3b entirely and had zero test coverage before.
	t.Run("UseToken validates and consumes a token exactly once", func(t *testing.T) {
		token := createTestBreakGlassToken(t, bgH, "use-tester")

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
}

func createTestBreakGlassToken(t *testing.T, h *BreakGlassHandler, createdBy string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/break-glass/tokens", strings.NewReader(
		fmt.Sprintf(`{"created_by":%q,"expires_in_hours":4}`, createdBy)))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), middleware.ContextKeyActor, createdBy)
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
