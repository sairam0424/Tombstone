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

// TestKillSwitchGraduatedRollback is the executable gate for EVAL-4's
// flag-api-side prerequisite: the circuit breaker's stepped auto-rollback
// ladder (100->50->25->0) needs a way to set an INTERMEDIATE rollout
// percentage without hitting UpdateEnvironment's require_approval gate
// (proven separately in TestRequireApprovalGate to 403 without a
// break-glass token evaluator has no access to). Runs against a real
// Postgres in the flag-api-migrations CI job, skips locally otherwise --
// same convention as every other DB-backed test in this package.
func TestKillSwitchGraduatedRollback(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed graduated kill-switch test")
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

	projectID := createTestProject(ctx, t, database, "eval4-killswitch-test")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	auditKey, err := secrets.NewAuditKey("eval4-killswitch-test-key-0000000000", "")
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

	killRequest := func(key string, body map[string]any) *http.Request {
		return newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+key+"/kill", body, projectID, map[string]string{"key": key})
	}

	t.Run("omitted rollout_pct behaves exactly like the pre-EVAL-4 binary kill switch", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-kill-omitted")

		rec := httptest.NewRecorder()
		flagH.KillSwitch(rec, killRequest(flag.Key, map[string]any{"environment": "production"}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp["enabled"] != false || resp["rollout_pct"] != float64(0) {
			t.Errorf("response = %+v, want enabled=false, rollout_pct=0", resp)
		}

		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if enabled || rolloutPct != 0 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (false, 0)", enabled, rolloutPct)
		}
	})

	t.Run("explicit rollout_pct=0 is equivalent to omitting it", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-kill-explicit-zero")

		rec := httptest.NewRecorder()
		flagH.KillSwitch(rec, killRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 0}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if enabled || rolloutPct != 0 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (false, 0)", enabled, rolloutPct)
		}
	})

	t.Run("a positive rollout_pct reduces exposure but keeps the flag enabled", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-kill-step-50")

		rec := httptest.NewRecorder()
		flagH.KillSwitch(rec, killRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 50}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp["enabled"] != true || resp["rollout_pct"] != float64(50) {
			t.Errorf("response = %+v, want enabled=true, rollout_pct=50", resp)
		}

		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 50 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (true, 50) — a graduated step must reduce exposure, not fully disable the flag", enabled, rolloutPct)
		}
	})

	t.Run("a subsequent step down to a lower percentage overwrites the previous step", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-kill-step-ladder")

		rec1 := httptest.NewRecorder()
		flagH.KillSwitch(rec1, killRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 50}))
		if rec1.Code != http.StatusOK {
			t.Fatalf("step 1 status = %d, want 200; body: %s", rec1.Code, rec1.Body.String())
		}

		rec2 := httptest.NewRecorder()
		flagH.KillSwitch(rec2, killRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 25}))
		if rec2.Code != http.StatusOK {
			t.Fatalf("step 2 status = %d, want 200; body: %s", rec2.Code, rec2.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 25 {
			t.Errorf("flag_environments after second step = (enabled=%v, rollout_pct=%d), want (true, 25)", enabled, rolloutPct)
		}

		rec3 := httptest.NewRecorder()
		flagH.KillSwitch(rec3, killRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 0}))
		if rec3.Code != http.StatusOK {
			t.Fatalf("final step status = %d, want 200; body: %s", rec3.Code, rec3.Body.String())
		}
		enabled, rolloutPct = readFlagEnv(t, flag.Key)
		if enabled || rolloutPct != 0 {
			t.Errorf("flag_environments after final step = (enabled=%v, rollout_pct=%d), want (false, 0) — the ladder's terminal step must be a full kill", enabled, rolloutPct)
		}
	})

	t.Run("out-of-range rollout_pct is rejected", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-kill-out-of-range")

		for _, pct := range []int{-1, 101} {
			rec := httptest.NewRecorder()
			flagH.KillSwitch(rec, killRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": pct}))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("rollout_pct=%d: status = %d, want 400; body: %s", pct, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("a graduated step bypasses require_approval exactly like a full kill does", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-kill-approval-bypass")

		if _, err := database.ExecContext(ctx, `UPDATE projects SET require_approval = true WHERE id = $1`, projectID); err != nil {
			t.Fatalf("set require_approval: %v", err)
		}
		defer func() {
			if _, err := database.ExecContext(ctx, `UPDATE projects SET require_approval = false WHERE id = $1`, projectID); err != nil {
				t.Fatalf("reset require_approval: %v", err)
			}
		}()

		rec := httptest.NewRecorder()
		flagH.KillSwitch(rec, killRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 50}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 — the circuit breaker's own graduated rollback must not be blockable by an approval gate meant for routine human-initiated changes; body: %s",
				rec.Code, rec.Body.String())
		}
		enabled, rolloutPct := readFlagEnv(t, flag.Key)
		if !enabled || rolloutPct != 50 {
			t.Errorf("flag_environments = (enabled=%v, rollout_pct=%d), want (true, 50)", enabled, rolloutPct)
		}
	})

	t.Run("audit log records the rollout_pct actually applied", func(t *testing.T) {
		flag := createTestFlag(t, flagH, projectID, "eval4-kill-audit")

		rec := httptest.NewRecorder()
		flagH.KillSwitch(rec, killRequest(flag.Key, map[string]any{"environment": "production", "rollout_pct": 25, "reason": "circuit_breaker_step"}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		var newState []byte
		if err := database.QueryRowContext(ctx, `
			SELECT new_state FROM audit_log WHERE flag_key = $1 AND event_type = 'kill_switch_activated' ORDER BY created_at DESC LIMIT 1
		`, flag.Key).Scan(&newState); err != nil {
			t.Fatalf("query audit_log: %v", err)
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
