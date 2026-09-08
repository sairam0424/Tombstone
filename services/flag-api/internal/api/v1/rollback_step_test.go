package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/db"
	"github.com/tombstone/flag-api/internal/db/sqlcgen"
	"github.com/tombstone/flag-api/internal/secrets"
)

// TestRollbackStep is the executable gate for EVAL-4's flag-api
// prerequisite: the circuit breaker's stepped auto-rollback ladder
// (100->50->25->0) needs to set an INTERMEDIATE rollout percentage without
// hitting UpdateEnvironment's require_approval gate (proven separately in
// TestRequireApprovalGate to 403 without a break-glass token evaluator has
// no access to) -- while never being able to INCREASE exposure, unlike the
// design this replaced (see PR #220's adversarial review, which found the
// original approach let a "kill switch" call re-enable a disabled flag or
// raise its rollout_pct). Runs against a real Postgres in the
// flag-api-migrations CI job, skips locally otherwise.
func TestRollbackStep(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed rollback-step test")
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

	projectID := createTestProject(ctx, t, database, "eval4-rollback-step-test")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	auditKey, err := secrets.NewAuditKey("eval4-rollback-step-test-key-000000", "")
	if err != nil {
		t.Fatalf("audit key: %v", err)
	}
	auditW := audit.NewWriter(database, auditKey)

	flagH := NewFlagHandler(database, rdb, logger, nil, auditW, nil, "")

	readFlagEnv := func(t *testing.T, key string) (enabled bool, rolloutPct int) {
		t.Helper()
		if err := database.QueryRowContext(ctx, `
			SELECT fe.enabled, fe.rollout_pct FROM flag_environments fe JOIN flags f ON f.id = fe.flag_id
			WHERE f.key = $1 AND fe.environment = 'production' AND f.project_id = $2
		`, key, projectID).Scan(&enabled, &rolloutPct); err != nil {
			t.Fatalf("read flag_environments: %v", err)
		}
		return enabled, rolloutPct
	}

	stepRequest := func(key string, body map[string]any) *http.Request {
		return newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+key+"/rollback-step", body, projectID, map[string]string{"key": key})
	}

	t.Run("a step down from the flag's initial enabled=true, rollout_pct=100 state succeeds", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-step-down")
		// createTestFlag seeds enabled=false, rollout_pct=0 -- seed a
		// realistic "live at full rollout" starting state via
		// UpdateEnvironment (approval-gated, but require_approval defaults
		// false) before exercising RollbackStep's reduction behavior.
		updateTestEnvironment(t, flagH, projectID, flag.Key, "production", true, 100)

		rec := httptest.NewRecorder()
		flagH.RollbackStep(rec, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 50}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 50 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (true, 50)", enabled, rolloutPct)
		}
	})

	t.Run("a further step down to 0 fully disables the flag", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-step-to-zero")
		updateTestEnvironment(t, flagH, projectID, flag.Key, "production", true, 100)

		rec1 := httptest.NewRecorder()
		flagH.RollbackStep(rec1, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 50}))
		if rec1.Code != http.StatusOK {
			t.Fatalf("step 1 status = %d, want 200; body: %s", rec1.Code, rec1.Body.String())
		}

		rec2 := httptest.NewRecorder()
		flagH.RollbackStep(rec2, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 0}))
		if rec2.Code != http.StatusOK {
			t.Fatalf("step 2 status = %d, want 200; body: %s", rec2.Code, rec2.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if enabled || rolloutPct != 0 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (false, 0)", enabled, rolloutPct)
		}
	})

	t.Run("attempting to INCREASE rollout_pct is rejected", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-reject-increase")
		updateTestEnvironment(t, flagH, projectID, flag.Key, "production", true, 100)

		rec1 := httptest.NewRecorder()
		flagH.RollbackStep(rec1, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 25}))
		if rec1.Code != http.StatusOK {
			t.Fatalf("initial step-down status = %d, want 200; body: %s", rec1.Code, rec1.Body.String())
		}

		rec2 := httptest.NewRecorder()
		flagH.RollbackStep(rec2, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 50}))
		if rec2.Code != http.StatusBadRequest {
			t.Fatalf("increase attempt status = %d, want 400 — a rollback step must never be able to raise exposure; body: %s", rec2.Code, rec2.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 25 {
			t.Errorf("flag_environments changed despite the rejected increase: (enabled=%v, rollout_pct=%d), want unchanged (true, 25)", enabled, rolloutPct)
		}
	})

	t.Run("attempting to re-enable an already-disabled flag is rejected", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-reject-reenable")

		rec1 := httptest.NewRecorder()
		flagH.RollbackStep(rec1, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 0}))
		if rec1.Code != http.StatusOK {
			t.Fatalf("full kill via rollback-step status = %d, want 200; body: %s", rec1.Code, rec1.Body.String())
		}

		rec2 := httptest.NewRecorder()
		flagH.RollbackStep(rec2, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 10}))
		if rec2.Code != http.StatusBadRequest {
			t.Fatalf("re-enable attempt status = %d, want 400 — a disabled flag has 0%% effective exposure, any positive rollout_pct is an increase; body: %s", rec2.Code, rec2.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if enabled || rolloutPct != 0 {
			t.Errorf("flag_environments changed despite the rejected re-enable: (enabled=%v, rollout_pct=%d), want unchanged (false, 0)", enabled, rolloutPct)
		}
	})

	t.Run("repeating the same step is idempotent, not rejected as an increase", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-repeat-same-step")
		updateTestEnvironment(t, flagH, projectID, flag.Key, "production", true, 100)

		for i := 0; i < 2; i++ {
			rec := httptest.NewRecorder()
			flagH.RollbackStep(rec, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 50}))
			if rec.Code != http.StatusOK {
				t.Fatalf("call %d: status = %d, want 200 (repeating the same target must not be treated as an increase); body: %s", i+1, rec.Code, rec.Body.String())
			}
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 50 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (true, 50)", enabled, rolloutPct)
		}
	})

	t.Run("out-of-range rollout_pct is rejected", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-out-of-range")

		for _, pct := range []int{-1, 101} {
			rec := httptest.NewRecorder()
			flagH.RollbackStep(rec, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": pct}))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("rollout_pct=%d: status = %d, want 400; body: %s", pct, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("omitted rollout_pct is rejected, not silently treated as a full kill", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-omitted-pct")
		updateTestEnvironment(t, flagH, projectID, flag.Key, "production", true, 100)

		rec := httptest.NewRecorder()
		flagH.RollbackStep(rec, stepRequest(flag.Key, map[string]any{"environment": "production"}))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 — a caller that omits rollout_pct must get a validation error, not a silent full kill; body: %s", rec.Code, rec.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 100 {
			t.Errorf("flag_environments changed despite the rejected request: (enabled=%v, rollout_pct=%d), want unchanged (true, 100)", enabled, rolloutPct)
		}
	})

	// TestRollbackFlagEnvironmentIsAtomicCAS below proves the underlying
	// query's own guarantee directly and deterministically -- reproducing
	// this same scenario through two genuinely racing HTTP calls would be
	// inherently nondeterministic (Go's connection pool doesn't guarantee
	// which goroutine's SELECT lands before the other's UPDATE commits),
	// so it would not reliably exercise the 409 branch on every run.
	t.Run("a stale target that no longer reflects the live state returns 409 via the atomic write's own guard", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-stale-write")
		updateTestEnvironment(t, flagH, projectID, flag.Key, "production", true, 100)

		// Reduce to 10% first -- this is the "concurrent winner" from the
		// adversarial review's scenario, already committed by the time our
		// own request's atomic write runs.
		rec1 := httptest.NewRecorder()
		flagH.RollbackStep(rec1, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 10}))
		if rec1.Code != http.StatusOK {
			t.Fatalf("winner step status = %d, want 200; body: %s", rec1.Code, rec1.Body.String())
		}

		// Call the atomic write directly with a stale min-exposure guard
		// (100, as if this caller's own read happened before the winner
		// committed) -- this is exactly what RollbackStep's handler would
		// have done had its own read raced the winner's write. The query
		// itself, not the handler's early check, must refuse this.
		n, err := sqlcgen.New(database).RollbackFlagEnvironment(ctx, sqlcgen.RollbackFlagEnvironmentParams{
			Enabled: true, RolloutPct: 50, UpdatedBy: "stale-tester",
			Key: flag.Key, Environment: "production", ProjectID: projectID,
			MinCurrentExposure: 100,
		})
		if err != nil {
			t.Fatalf("RollbackFlagEnvironment: %v", err)
		}
		if n != 0 {
			t.Errorf("rows affected = %d, want 0 — the atomic WHERE clause must refuse a write whose min_current_exposure guard (100) no longer matches the live state (10%%)", n)
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 10 {
			t.Errorf("flag_environments changed despite the refused write: (enabled=%v, rollout_pct=%d), want unchanged (true, 10)", enabled, rolloutPct)
		}
	})

	t.Run("a step bypasses require_approval exactly like a full kill does", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-approval-bypass")
		updateTestEnvironment(t, flagH, projectID, flag.Key, "production", true, 100)

		if _, err := database.ExecContext(ctx, `UPDATE projects SET require_approval = true WHERE id = $1`, projectID); err != nil {
			t.Fatalf("set require_approval: %v", err)
		}
		defer func() {
			if _, err := database.ExecContext(ctx, `UPDATE projects SET require_approval = false WHERE id = $1`, projectID); err != nil {
				t.Fatalf("reset require_approval: %v", err)
			}
		}()

		rec := httptest.NewRecorder()
		flagH.RollbackStep(rec, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 50}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 — the circuit breaker's own rollback step must not be blockable by an approval gate meant for routine human-initiated changes; body: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("nonexistent flag returns 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		flagH.RollbackStep(rec, stepRequest("eval4-does-not-exist", map[string]any{"environment": "production", "rollout_pct": 50}))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("nonexistent environment on an existing flag returns 404", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-missing-env")
		rec := httptest.NewRecorder()
		flagH.RollbackStep(rec, stepRequest(flag.Key, map[string]any{"environment": "does-not-exist", "rollout_pct": 50}))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("audit event_type is distinct from a full kill switch's", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-audit-distinct")
		updateTestEnvironment(t, flagH, projectID, flag.Key, "production", true, 100)

		rec := httptest.NewRecorder()
		flagH.RollbackStep(rec, stepRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 25, "reason": "circuit_breaker"}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		var eventType string
		var newState []byte
		if err := database.QueryRowContext(ctx, `
			SELECT event_type, new_state FROM audit_log WHERE flag_key = $1 ORDER BY created_at DESC LIMIT 1
		`, flag.Key).Scan(&eventType, &newState); err != nil {
			t.Fatalf("query audit_log: %v", err)
		}
		if eventType != "circuit_breaker_rollback_step" {
			t.Errorf("event_type = %q, want %q — must be distinguishable from KillSwitch's kill_switch_activated", eventType, "circuit_breaker_rollback_step")
		}
		var decoded map[string]any
		if err := json.Unmarshal(newState, &decoded); err != nil {
			t.Fatalf("decode audit new_state: %v", err)
		}
		if decoded["rollout_pct"] != float64(25) || decoded["enabled"] != true {
			t.Errorf("audit new_state = %+v, want enabled=true, rollout_pct=25", decoded)
		}
	})
}
