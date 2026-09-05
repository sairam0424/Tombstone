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
	"github.com/tombstone/flag-api/internal/secrets"
)

// TestRecoveryStep is the executable gate for EVAL-4's HALF_OPEN recovery
// ladder capability -- the mirror image of TestRollbackStep, proving
// RecoveryStep can only ever INCREASE a flag's exposure, never decrease it
// (RollbackStep already owns the decrease direction). Runs against a real
// Postgres in the flag-api-migrations CI job, skips locally otherwise.
func TestRecoveryStep(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed recovery-step test")
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

	projectID := createTestProject(ctx, t, database, "eval4-recovery-step-test")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	auditKey, err := secrets.NewAuditKey("eval4-recovery-step-test-key-00000", "")
	if err != nil {
		t.Fatalf("audit key: %v", err)
	}
	auditW := audit.NewWriter(database, auditKey)

	flagH := NewFlagHandler(database, rdb, logger, nil, auditW, nil)

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

	recoveryRequest := func(key string, body map[string]any) *http.Request {
		return newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+key+"/recovery-step", body, projectID, map[string]string{"key": key})
	}

	t.Run("a step up from a fully-killed flag succeeds", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-recovery-step-up")
		// createTestFlag seeds enabled=false, rollout_pct=0 -- exactly the
		// "fully killed, waiting to recover" starting state.

		rec := httptest.NewRecorder()
		flagH.RecoveryStep(rec, recoveryRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 10}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 10 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (true, 10)", enabled, rolloutPct)
		}
	})

	t.Run("a further step up climbs the full ladder to 100", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-recovery-climb")

		for _, pct := range []int{10, 25, 50, 100} {
			rec := httptest.NewRecorder()
			flagH.RecoveryStep(rec, recoveryRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": pct}))
			if rec.Code != http.StatusOK {
				t.Fatalf("step to %d: status = %d, want 200; body: %s", pct, rec.Code, rec.Body.String())
			}
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 100 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (true, 100)", enabled, rolloutPct)
		}
	})

	t.Run("attempting to DECREASE exposure is rejected", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-recovery-reject-decrease")
		updateTestEnvironment(t, flagH, projectID, flag.Key, "production", true, 50)

		rec := httptest.NewRecorder()
		flagH.RecoveryStep(rec, recoveryRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 25}))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 — a recovery step must never be able to lower exposure (use rollback-step for that); body: %s", rec.Code, rec.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 50 {
			t.Errorf("flag_environments changed despite the rejected decrease: (enabled=%v, rollout_pct=%d), want unchanged (true, 50)", enabled, rolloutPct)
		}
	})

	t.Run("repeating the same step is idempotent, not rejected as a decrease", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-recovery-repeat-same-step")

		for i := 0; i < 2; i++ {
			rec := httptest.NewRecorder()
			flagH.RecoveryStep(rec, recoveryRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 25}))
			if rec.Code != http.StatusOK {
				t.Fatalf("call %d: status = %d, want 200; body: %s", i+1, rec.Code, rec.Body.String())
			}
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 25 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (true, 25)", enabled, rolloutPct)
		}
	})

	t.Run("a stale target that no longer reflects the live state returns 409 via the atomic write's own guard", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-recovery-stale-write")

		rec1 := httptest.NewRecorder()
		flagH.RecoveryStep(rec1, recoveryRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 50}))
		if rec1.Code != http.StatusOK {
			t.Fatalf("winner step status = %d, want 200; body: %s", rec1.Code, rec1.Body.String())
		}

		// A stale, lower target (25) issued as if this caller's own read
		// happened before the winner's more-aggressive step to 50 committed.
		rec2 := httptest.NewRecorder()
		flagH.RecoveryStep(rec2, recoveryRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 25}))
		if rec2.Code != http.StatusBadRequest {
			t.Fatalf("stale lower target status = %d, want 400 (25 < live 50 is a decrease from RecoveryStep's perspective); body: %s", rec2.Code, rec2.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 50 {
			t.Errorf("flag_environments changed despite the rejected stale request: (enabled=%v, rollout_pct=%d), want unchanged (true, 50)", enabled, rolloutPct)
		}
	})

	t.Run("out-of-range rollout_pct is rejected", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-recovery-out-of-range")

		for _, pct := range []int{-1, 101} {
			rec := httptest.NewRecorder()
			flagH.RecoveryStep(rec, recoveryRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": pct}))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("rollout_pct=%d: status = %d, want 400; body: %s", pct, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("omitted rollout_pct is rejected", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-recovery-omitted-pct")

		rec := httptest.NewRecorder()
		flagH.RecoveryStep(rec, recoveryRequest(flag.Key, map[string]any{"environment": "production"}))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a recovery step bypasses require_approval exactly like a rollback step does", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-recovery-approval-bypass")

		if _, err := database.ExecContext(ctx, `UPDATE projects SET require_approval = true WHERE id = $1`, projectID); err != nil {
			t.Fatalf("set require_approval: %v", err)
		}
		defer func() {
			if _, err := database.ExecContext(ctx, `UPDATE projects SET require_approval = false WHERE id = $1`, projectID); err != nil {
				t.Fatalf("reset require_approval: %v", err)
			}
		}()

		rec := httptest.NewRecorder()
		flagH.RecoveryStep(rec, recoveryRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 10}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("nonexistent flag returns 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		flagH.RecoveryStep(rec, recoveryRequest("eval4-recovery-does-not-exist", map[string]any{"environment": "production", "rollout_pct": 10}))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("audit event_type is distinct from both kill switch and rollback step", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-recovery-audit-distinct")

		rec := httptest.NewRecorder()
		flagH.RecoveryStep(rec, recoveryRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 25, "reason": "circuit_breaker"}))
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
		if eventType != "circuit_breaker_recovery_step" {
			t.Errorf("event_type = %q, want %q", eventType, "circuit_breaker_recovery_step")
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
