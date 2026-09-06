package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/db"
	"github.com/tombstone/flag-api/internal/middleware"
	"github.com/tombstone/flag-api/internal/secrets"
)

// TestChangeRequestProposeQuorumAndApply is the executable gate for SEC-3b —
// it runs against a real Postgres in the flag-api-migrations CI job and
// skips locally (same convention as TestTenancyIsolation). It proves the
// propose -> accumulate approvals -> quorum -> apply chain actually mutates
// flag_environments, not just the change_requests row's own status.
func TestChangeRequestProposeQuorumAndApply(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed change-request test")
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

	projectID := createTestProject(ctx, t, database, "sec3b-quorum-test")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	auditKey, err := secrets.NewAuditKey("sec3b-quorum-test-key-0000000000000", "")
	if err != nil {
		t.Fatalf("audit key: %v", err)
	}
	auditW := audit.NewWriter(database, auditKey)

	flagH := NewFlagHandler(database, rdb, logger, nil, auditW, nil, "")
	crH := NewChangeRequestHandler(database, rdb, logger, auditW, "")

	flag := createTestFlag(t, flagH, projectID, "sec3b-quorum-flag")

	readFlagEnvironment := func(t *testing.T, env string) (enabled bool, rolloutPct int) {
		t.Helper()
		if err := database.QueryRowContext(ctx, `
			SELECT fe.enabled, fe.rollout_pct FROM flag_environments fe JOIN flags f ON f.id = fe.flag_id
			WHERE f.key = $1 AND fe.environment = $2 AND f.project_id = $3
		`, flag.Key, env, projectID).Scan(&enabled, &rolloutPct); err != nil {
			t.Fatalf("read flag_environments: %v", err)
		}
		return enabled, rolloutPct
	}

	setRequiredApprovals := func(t *testing.T, n int) {
		t.Helper()
		if _, err := database.ExecContext(ctx, `UPDATE projects SET required_approvals = $1 WHERE id = $2`, n, projectID); err != nil {
			t.Fatalf("set required_approvals: %v", err)
		}
	}

	readAuditEventTypes := func(t *testing.T, flagKey string) []string {
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

	t.Run("ProposeChangeRequest creates a PENDING row with the right payload", func(t *testing.T) {
		cr := proposeTestChangeRequest(t, crH, projectID, "proposer-1", flag.Key, "production", true, 42)
		if cr.Status != "PENDING" {
			t.Errorf("status = %q, want PENDING", cr.Status)
		}
		payload, ok := decodeFlagEnvironmentChangePayload(cr.ChangePayload)
		if !ok {
			t.Fatalf("change_payload did not decode as a flag-environment payload: %s", cr.ChangePayload)
		}
		if !payload.Enabled || payload.RolloutPct != 42 {
			t.Errorf("payload = %+v, want {Enabled:true RolloutPct:42}", payload)
		}
		// Reject it — idx_change_requests_one_pending_proposal (migration 020)
		// allows only one PENDING applicable proposal per flag+environment at a
		// time, and later subtests propose against this same flag+environment.
		rejectTestChangeRequest(t, crH, projectID, "cleanup", cr.ID)
	})

	t.Run("ProposeChangeRequest rejects a nonexistent flag/environment", func(t *testing.T) {
		req := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests", map[string]any{
			"flag_key": "sec3b-does-not-exist", "environment": "production", "enabled": true, "rollout_pct": 0,
		}, projectID, "proposer-1", nil)
		rec := httptest.NewRecorder()
		crH.ProposeChangeRequest(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("ProposeChangeRequest rejects an out-of-range rollout_pct", func(t *testing.T) {
		req := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests", map[string]any{
			"flag_key": flag.Key, "environment": "production", "enabled": true, "rollout_pct": 101,
		}, projectID, "proposer-1", nil)
		rec := httptest.NewRecorder()
		crH.ProposeChangeRequest(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("single approval applies immediately at the default quorum of 1", func(t *testing.T) {
		cr := proposeTestChangeRequest(t, crH, projectID, "proposer-1", flag.Key, "production", true, 77)

		resp := approveTestChangeRequest(t, crH, projectID, "approver-1", cr.ID)
		if resp["status"] != "APPLIED" {
			t.Fatalf("status = %v, want APPLIED", resp["status"])
		}

		enabled, rolloutPct := readFlagEnvironment(t, "production")
		if !enabled || rolloutPct != 77 {
			t.Errorf("flag_environments after apply = (enabled=%v, rollout_pct=%d), want (true, 77)", enabled, rolloutPct)
		}
	})

	t.Run("quorum of 2 requires two DISTINCT approvers before applying", func(t *testing.T) {
		setRequiredApprovals(t, 2)
		defer setRequiredApprovals(t, 1)

		baselineEnabled, baselineRolloutPct := readFlagEnvironment(t, "production")
		cr := proposeTestChangeRequest(t, crH, projectID, "proposer-1", flag.Key, "production", !baselineEnabled, baselineRolloutPct+1)

		first := approveTestChangeRequest(t, crH, projectID, "approver-1", cr.ID)
		if first["status"] != "PENDING" {
			t.Fatalf("after 1 of 2 approvals: status = %v, want PENDING (must not apply early)", first["status"])
		}
		enabled, rolloutPct := readFlagEnvironment(t, "production")
		if enabled != baselineEnabled || rolloutPct != baselineRolloutPct {
			t.Errorf("flag_environments changed after only 1 of 2 approvals: (enabled=%v, rollout_pct=%d), want unchanged (%v, %d) — proves quorum is enforced before applying",
				enabled, rolloutPct, baselineEnabled, baselineRolloutPct)
		}

		second := approveTestChangeRequest(t, crH, projectID, "approver-2", cr.ID)
		if second["status"] != "APPLIED" {
			t.Fatalf("after 2 of 2 approvals: status = %v, want APPLIED", second["status"])
		}
		enabled, rolloutPct = readFlagEnvironment(t, "production")
		if enabled != !baselineEnabled || rolloutPct != baselineRolloutPct+1 {
			t.Errorf("flag_environments after quorum met = (enabled=%v, rollout_pct=%d), want (%v, %d)",
				enabled, rolloutPct, !baselineEnabled, baselineRolloutPct+1)
		}
	})

	t.Run("the same actor approving twice does not double-count toward quorum", func(t *testing.T) {
		setRequiredApprovals(t, 2)
		defer setRequiredApprovals(t, 1)

		cr := proposeTestChangeRequest(t, crH, projectID, "proposer-1", flag.Key, "production", true, 1)

		req := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests/"+cr.ID+"/approve",
			nil, projectID, "approver-1", map[string]string{"id": cr.ID})
		rec := httptest.NewRecorder()
		crH.ApproveChangeRequest(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("first approval: status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		req = changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests/"+cr.ID+"/approve",
			nil, projectID, "approver-1", map[string]string{"id": cr.ID})
		rec = httptest.NewRecorder()
		crH.ApproveChangeRequest(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("second approval by the SAME actor: status = %d, want 409 (must not count twice toward quorum)", rec.Code)
		}

		// Still PENDING at 1 of 2 — reject it so it doesn't block later
		// subtests' proposals against this same flag+environment (migration
		// 020's idx_change_requests_one_pending_proposal allows only one).
		rejectTestChangeRequest(t, crH, projectID, "cleanup", cr.ID)
	})

	t.Run("approving a proposal whose target environment no longer exists fails without consuming the approval", func(t *testing.T) {
		flagToDelete := createTestFlag(t, flagH, projectID, "sec3b-quorum-flag-to-delete")
		cr := proposeTestChangeRequest(t, crH, projectID, "proposer-1", flagToDelete.Key, "production", true, 50)

		if _, err := database.ExecContext(ctx, `
			DELETE FROM flag_environments fe USING flags f WHERE f.id = fe.flag_id AND f.key = $1 AND f.project_id = $2
		`, flagToDelete.Key, projectID); err != nil {
			t.Fatalf("delete flag_environments row: %v", err)
		}

		req := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests/"+cr.ID+"/approve",
			nil, projectID, "approver-1", map[string]string{"id": cr.ID})
		rec := httptest.NewRecorder()
		crH.ApproveChangeRequest(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
		}

		var status string
		if err := database.QueryRowContext(ctx, `SELECT status FROM change_requests WHERE id = $1`, cr.ID).Scan(&status); err != nil {
			t.Fatalf("read change_requests status: %v", err)
		}
		if status != "PENDING" {
			t.Errorf("status = %q, want PENDING (the failed apply must not have consumed the approval)", status)
		}
	})

	t.Run("quorum met on an informational (non-flag-environment) payload falls back to APPROVED, never APPLIED", func(t *testing.T) {
		var crID string
		if err := database.QueryRowContext(ctx, `
			INSERT INTO change_requests (flag_key, environment, requested_by, status, change_payload, project_id)
			VALUES ($1, 'production', 'system', 'PENDING', '{"reason":"owner_deprovisioned"}', $2)
			RETURNING id
		`, flag.Key, projectID).Scan(&crID); err != nil {
			t.Fatalf("insert informational change request: %v", err)
		}

		resp := approveTestChangeRequest(t, crH, projectID, "approver-1", crID)
		if resp["status"] != "APPROVED" {
			t.Fatalf("status = %v, want APPROVED (an informational payload has nothing to apply)", resp["status"])
		}
	})

	t.Run("lowering required_approvals after proposing does not retroactively downgrade an in-flight request's quorum", func(t *testing.T) {
		setRequiredApprovals(t, 3)

		cr := proposeTestChangeRequest(t, crH, projectID, "proposer-1", flag.Key, "production", true, 61)

		first := approveTestChangeRequest(t, crH, projectID, "approver-1", cr.ID)
		if first["status"] != "PENDING" {
			t.Fatalf("after 1 of 3 approvals: status = %v, want PENDING", first["status"])
		}

		// Project-wide policy is now relaxed to 2 — but this proposal's quorum
		// was pinned at 3 when it was created, and must stay held to that.
		setRequiredApprovals(t, 2)
		defer setRequiredApprovals(t, 1)

		second := approveTestChangeRequest(t, crH, projectID, "approver-2", cr.ID)
		if second["status"] != "PENDING" {
			t.Fatalf("after 2 of 3 approvals (quorum pinned at propose time to 3, not the now-relaxed project value of 2): status = %v, want PENDING", second["status"])
		}
		if second["required_approvals"] != float64(3) {
			t.Errorf("required_approvals reported = %v, want 3 (the value pinned at propose time)", second["required_approvals"])
		}

		third := approveTestChangeRequest(t, crH, projectID, "approver-3", cr.ID)
		if third["status"] != "APPLIED" {
			t.Fatalf("after 3 of 3 approvals: status = %v, want APPLIED", third["status"])
		}
	})

	t.Run("a duplicate PENDING proposal for the same flag+environment is rejected", func(t *testing.T) {
		dupFlag := createTestFlag(t, flagH, projectID, "sec3b-dup-flag")
		first := proposeTestChangeRequest(t, crH, projectID, "proposer-1", dupFlag.Key, "production", true, 10)

		req := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests", map[string]any{
			"flag_key": dupFlag.Key, "environment": "production", "enabled": false, "rollout_pct": 20,
		}, projectID, "proposer-2", nil)
		rec := httptest.NewRecorder()
		crH.ProposeChangeRequest(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("second concurrent proposal for the same flag+environment: status = %d, want 409; body: %s", rec.Code, rec.Body.String())
		}

		// A different environment for the SAME flag must still be allowed —
		// the constraint is scoped to (flag_key, environment), not flag_key alone.
		req = changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests", map[string]any{
			"flag_key": dupFlag.Key, "environment": "staging", "enabled": true, "rollout_pct": 5,
		}, projectID, "proposer-2", nil)
		rec = httptest.NewRecorder()
		crH.ProposeChangeRequest(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("proposal for a DIFFERENT environment of the same flag: status = %d, want 201; body: %s", rec.Code, rec.Body.String())
		}

		// Rejecting the first frees the slot for a new PENDING proposal.
		rejectReq := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests/"+first.ID+"/reject",
			nil, projectID, "rejector-1", map[string]string{"id": first.ID})
		rejectRec := httptest.NewRecorder()
		crH.RejectChangeRequest(rejectRec, rejectReq)
		if rejectRec.Code != http.StatusOK {
			t.Fatalf("reject first proposal: status = %d, want 200; body: %s", rejectRec.Code, rejectRec.Body.String())
		}

		req = changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests", map[string]any{
			"flag_key": dupFlag.Key, "environment": "production", "enabled": false, "rollout_pct": 20,
		}, projectID, "proposer-2", nil)
		rec = httptest.NewRecorder()
		crH.ProposeChangeRequest(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("proposal after the earlier one was rejected: status = %d, want 201; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("an already-APPLIED change request cannot be approved or rejected again", func(t *testing.T) {
		cr := proposeTestChangeRequest(t, crH, projectID, "proposer-1", flag.Key, "production", true, 99)
		resp := approveTestChangeRequest(t, crH, projectID, "approver-1", cr.ID)
		if resp["status"] != "APPLIED" {
			t.Fatalf("setup: status = %v, want APPLIED", resp["status"])
		}

		reapproveReq := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests/"+cr.ID+"/approve",
			nil, projectID, "approver-2", map[string]string{"id": cr.ID})
		rec := httptest.NewRecorder()
		crH.ApproveChangeRequest(rec, reapproveReq)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("re-approving an APPLIED request: status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}

		rejectReq := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests/"+cr.ID+"/reject",
			nil, projectID, "approver-2", map[string]string{"id": cr.ID})
		rec = httptest.NewRecorder()
		crH.RejectChangeRequest(rec, rejectReq)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("rejecting an APPLIED request: status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("concurrent distinct approvers racing for quorum=2 both get recorded, no lost update", func(t *testing.T) {
		setRequiredApprovals(t, 2)
		defer setRequiredApprovals(t, 1)

		cr := proposeTestChangeRequest(t, crH, projectID, "proposer-1", flag.Key, "production", true, 88)

		actors := []string{"racer-1", "racer-2"}
		codes := make([]int, len(actors))
		var wg sync.WaitGroup
		wg.Add(len(actors))
		for i, actor := range actors {
			go func(i int, actor string) {
				defer wg.Done()
				req := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests/"+cr.ID+"/approve",
					nil, projectID, actor, map[string]string{"id": cr.ID})
				rec := httptest.NewRecorder()
				crH.ApproveChangeRequest(rec, req)
				codes[i] = rec.Code
			}(i, actor)
		}
		wg.Wait()

		for i, code := range codes {
			if code != http.StatusOK {
				t.Errorf("racer %d (%s): status = %d, want 200", i, actors[i], code)
			}
		}

		var status string
		var approvedBy []string
		if err := database.QueryRowContext(ctx, `SELECT status, COALESCE(approved_by, '{}') FROM change_requests WHERE id = $1`, cr.ID).
			Scan(&status, pq.Array(&approvedBy)); err != nil {
			t.Fatalf("read final state: %v", err)
		}
		if status != "APPLIED" {
			t.Fatalf("status = %q, want APPLIED — both concurrent approvals must have been recorded and quorum reached", status)
		}
		if len(approvedBy) != 2 {
			t.Fatalf("approved_by = %v, want exactly 2 entries — a lost update under FOR UPDATE contention would show only 1", approvedBy)
		}
	})

	t.Run("propose, approve, and apply each write a distinct audit_log entry", func(t *testing.T) {
		auditFlag := createTestFlag(t, flagH, projectID, "sec3b-audit-flag")
		cr := proposeTestChangeRequest(t, crH, projectID, "proposer-1", auditFlag.Key, "production", true, 33)
		if resp := approveTestChangeRequest(t, crH, projectID, "approver-1", cr.ID); resp["status"] != "APPLIED" {
			t.Fatalf("setup: status = %v, want APPLIED", resp["status"])
		}

		types := readAuditEventTypes(t, auditFlag.Key)
		for _, want := range []string{"change_request_proposed", "change_request_applied"} {
			found := false
			for _, got := range types {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected an audit_log entry with event_type=%q for flag %s, types seen: %v", want, auditFlag.Key, types)
			}
		}
	})

	t.Run("reject writes an audit_log entry and persists the rejection reason", func(t *testing.T) {
		rejectFlag := createTestFlag(t, flagH, projectID, "sec3b-audit-reject-flag")
		cr := proposeTestChangeRequest(t, crH, projectID, "proposer-1", rejectFlag.Key, "production", true, 1)

		req := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests/"+cr.ID+"/reject",
			map[string]any{"reason": "not needed"}, projectID, "rejector-1", map[string]string{"id": cr.ID})
		rec := httptest.NewRecorder()
		crH.RejectChangeRequest(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("reject status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		types := readAuditEventTypes(t, rejectFlag.Key)
		found := false
		for _, got := range types {
			if got == "change_request_rejected" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a change_request_rejected audit_log entry, types seen: %v", types)
		}

		var rejectionReason sql.NullString
		if err := database.QueryRowContext(ctx, `SELECT rejection_reason FROM change_requests WHERE id = $1`, cr.ID).Scan(&rejectionReason); err != nil {
			t.Fatalf("read rejection_reason: %v", err)
		}
		if !rejectionReason.Valid || rejectionReason.String != "not needed" {
			t.Errorf("rejection_reason = %v, want %q", rejectionReason, "not needed")
		}
	})
}

// TestProposeChangeRequestValidation needs no DB — every case here is
// rejected by ProposeChangeRequest before it ever touches h.db.
func TestProposeChangeRequestValidation(t *testing.T) {
	h := &ChangeRequestHandler{logger: zap.NewNop()}

	cases := []struct {
		name string
		body string
	}{
		{"malformed JSON body", `not json`},
		{"missing flag_key", `{"environment":"production","enabled":true,"rollout_pct":0}`},
		{"missing environment", `{"flag_key":"x","enabled":true,"rollout_pct":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/change-requests", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			ctx := context.WithValue(req.Context(), middleware.ContextKeyProjectID, "does-not-matter")
			ctx = context.WithValue(ctx, middleware.ContextKeyActor, "tester")
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			h.ProposeChangeRequest(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func proposeTestChangeRequest(t *testing.T, h *ChangeRequestHandler, projectID, actor, flagKey, env string, enabled bool, rolloutPct int) ChangeRequest {
	t.Helper()
	req := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests", map[string]any{
		"flag_key": flagKey, "environment": env, "enabled": enabled, "rollout_pct": rolloutPct,
	}, projectID, actor, nil)
	rec := httptest.NewRecorder()
	h.ProposeChangeRequest(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("proposeTestChangeRequest status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var cr ChangeRequest
	if err := json.NewDecoder(rec.Body).Decode(&cr); err != nil {
		t.Fatalf("decode propose response: %v", err)
	}
	return cr
}

func approveTestChangeRequest(t *testing.T, h *ChangeRequestHandler, projectID, actor, id string) map[string]any {
	t.Helper()
	req := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests/"+id+"/approve",
		nil, projectID, actor, map[string]string{"id": id})
	rec := httptest.NewRecorder()
	h.ApproveChangeRequest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approveTestChangeRequest(%s) status = %d, want 200; body: %s", id, rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode approve response: %v", err)
	}
	return resp
}

func rejectTestChangeRequest(t *testing.T, h *ChangeRequestHandler, projectID, actor, id string) {
	t.Helper()
	req := changeRequestRequestAs(t, http.MethodPost, "/api/v1/change-requests/"+id+"/reject",
		nil, projectID, actor, map[string]string{"id": id})
	rec := httptest.NewRecorder()
	h.RejectChangeRequest(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rejectTestChangeRequest(%s) status = %d, want 200; body: %s", id, rec.Code, rec.Body.String())
	}
}

// changeRequestRequestAs mirrors newTenancyRequest but takes an explicit
// actor — quorum tests need multiple DISTINCT approvers, which
// newTenancyRequest's hardcoded "tenancy-test" actor can't express.
func changeRequestRequestAs(t *testing.T, method, path string, body any, projectID, actor string, urlParams map[string]string) *http.Request {
	t.Helper()
	var bodyReader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = strings.NewReader(string(raw))
	} else {
		bodyReader = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	for k, v := range urlParams {
		rctx.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, middleware.ContextKeyProjectID, projectID)
	ctx = context.WithValue(ctx, middleware.ContextKeyActor, actor)
	return req.WithContext(ctx)
}

// TestDecodeFlagEnvironmentChangePayload needs no DB — it pins the
// distinction between an applicable flag-environment proposal and an
// informational payload (e.g. orphan-detection's {"reason":...}) that
// ApproveChangeRequest must fall back to a plain APPROVED status flip for,
// never attempting to apply.
func TestDecodeFlagEnvironmentChangePayload(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantOK     bool
		wantResult flagEnvironmentChangePayload
	}{
		{"both fields present", `{"enabled":true,"rollout_pct":42}`, true, flagEnvironmentChangePayload{Enabled: true, RolloutPct: 42}},
		{"both fields present, enabled false", `{"enabled":false,"rollout_pct":0}`, true, flagEnvironmentChangePayload{Enabled: false, RolloutPct: 0}},
		{"informational payload — reason only", `{"reason":"owner_deprovisioned","owner_email":"a@b.com"}`, false, flagEnvironmentChangePayload{}},
		{"empty object", `{}`, false, flagEnvironmentChangePayload{}},
		{"missing rollout_pct", `{"enabled":true}`, false, flagEnvironmentChangePayload{}},
		{"missing enabled", `{"rollout_pct":5}`, false, flagEnvironmentChangePayload{}},
		{"not valid JSON", `not json`, false, flagEnvironmentChangePayload{}},
		{"valid JSON but not an object", `[1,2,3]`, false, flagEnvironmentChangePayload{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decodeFlagEnvironmentChangePayload(json.RawMessage(tc.raw))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.wantResult {
				t.Errorf("result = %+v, want %+v", got, tc.wantResult)
			}
		})
	}
}
