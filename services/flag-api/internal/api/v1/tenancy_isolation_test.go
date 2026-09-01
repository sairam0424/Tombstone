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
	"github.com/tombstone/flag-api/internal/middleware"
	"github.com/tombstone/flag-api/internal/secrets"
)

// TestTenancyIsolation is the executable gate for TEN-1a. It runs against a
// real Postgres in the flag-api-migrations CI job and skips locally (same
// convention as internal/db and internal/audit's DB-backed tests).
//
// It proves, against real handlers and a real database, that every query
// TEN-1a touched is actually scoped to the caller's project — not just that
// the SQL text contains "project_id somewhere. Each subtest creates the SAME
// flag key in TWO different projects and checks that an operation in one
// project never observes or mutates the other's copy. Before TEN-1a every one
// of these was a real cross-tenant leak or a real cross-tenant write.
func TestTenancyIsolation(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed tenancy isolation test")
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

	projectA := createTestProject(ctx, t, database, "ten1a-tenant-a")
	projectB := createTestProject(ctx, t, database, "ten1a-tenant-b")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	auditKey, err := secrets.NewAuditKey("tenancy-isolation-test-key-000000000000", "")
	if err != nil {
		t.Fatalf("audit key: %v", err)
	}
	auditW := audit.NewWriter(database, auditKey)

	flagH := NewFlagHandler(database, rdb, logger, nil, auditW)
	snapH := NewSnapshotHandler(database, logger)
	prereqH := NewPrerequisiteHandler(database, logger)
	scheduledH := NewScheduledHandler(database, rdb, logger, auditW)
	auditH := NewAuditHandler(database, logger, auditW)
	crH := NewChangeRequestHandler(database, rdb, logger)

	const sharedKey = "ten1a-shared-key"

	// Same key, created independently in both projects. UNIQUE(project_id, key)
	// permits this — it is the exact situation TEN-1a's predicates must handle.
	flagA := createTestFlag(t, flagH, projectA, sharedKey)
	flagB := createTestFlag(t, flagH, projectB, sharedKey)

	t.Run("GetFlag returns only the caller's own copy", func(t *testing.T) {
		gotA := getTestFlag(t, flagH, projectA, sharedKey)
		if gotA.ID != flagA.ID {
			t.Fatalf("project A's GetFlag returned id %s, want its own flag %s", gotA.ID, flagA.ID)
		}
		gotB := getTestFlag(t, flagH, projectB, sharedKey)
		if gotB.ID != flagB.ID {
			t.Fatalf("project B's GetFlag returned id %s, want its own flag %s", gotB.ID, flagB.ID)
		}
		if gotA.ID == gotB.ID {
			t.Fatal("both projects resolved to the SAME flag row for the same key — no isolation")
		}
	})

	t.Run("ListFlags does not leak the other project's same-keyed flag", func(t *testing.T) {
		listA := listTestFlags(t, flagH, projectA)
		assertExactlyOneFlagWithID(t, listA, sharedKey, flagA.ID)

		listB := listTestFlags(t, flagH, projectB)
		assertExactlyOneFlagWithID(t, listB, sharedKey, flagB.ID)
	})

	t.Run("UpdateEnvironment in one project does not affect the other's", func(t *testing.T) {
		updateTestEnvironment(t, flagH, projectA, sharedKey, "production", true, 50)

		snapA := getTestSnapshot(t, snapH, projectA, "production")
		stateA := findSnapshotEntry(snapA, sharedKey)
		if stateA == nil || !stateA.Enabled || stateA.RolloutPct != 50 {
			t.Fatalf("project A snapshot after its own update = %+v, want enabled=true rollout=50", stateA)
		}

		snapB := getTestSnapshot(t, snapH, projectB, "production")
		stateB := findSnapshotEntry(snapB, sharedKey)
		if stateB == nil {
			t.Fatal("project B's own flag is missing from its own snapshot")
		}
		if stateB.Enabled || stateB.RolloutPct != 0 {
			t.Fatalf("project B's environment was mutated by project A's UpdateEnvironment call: %+v", stateB)
		}
	})

	t.Run("GetSnapshot never returns another project's flags", func(t *testing.T) {
		snapA := getTestSnapshot(t, snapH, projectA, "production")
		for _, f := range snapA.Flags {
			if f.FlagID == flagB.ID {
				t.Fatalf("project A's snapshot contains project B's flag row (id %s)", flagB.ID)
			}
		}
		snapB := getTestSnapshot(t, snapH, projectB, "production")
		for _, f := range snapB.Flags {
			if f.FlagID == flagA.ID {
				t.Fatalf("project B's snapshot contains project A's flag row (id %s)", flagA.ID)
			}
		}
	})

	t.Run("KillSwitch in one project does not touch the other's", func(t *testing.T) {
		updateTestEnvironment(t, flagH, projectB, sharedKey, "production", true, 100)

		killTestSwitch(t, flagH, projectA, sharedKey, "production")

		snapA := getTestSnapshot(t, snapH, projectA, "production")
		if s := findSnapshotEntry(snapA, sharedKey); s == nil || s.Enabled {
			t.Fatalf("project A's flag was not killed: %+v", s)
		}
		snapB := getTestSnapshot(t, snapH, projectB, "production")
		if s := findSnapshotEntry(snapB, sharedKey); s == nil || !s.Enabled {
			t.Fatalf("project B's flag was killed by project A's KillSwitch call: %+v", s)
		}
	})

	// TEN-1a-2: before this fix, ListAuditLog and VerifyChain had no project
	// filter at all, so a VIEWER-level caller in either project could read
	// (or learn the integrity status of) the OTHER project's full audit
	// history — the writes above (CreateFlag/UpdateEnvironment/KillSwitch)
	// have already generated real, distinct audit entries in both projects.
	t.Run("ListAuditLog and VerifyChain never expose another project's history", func(t *testing.T) {
		beforeA := listTestAuditEntries(t, auditH, projectA)
		beforeB := listTestAuditEntries(t, auditH, projectB)

		// One more write, to project A only.
		updateTestEnvironment(t, flagH, projectA, sharedKey, "staging", true, 77)

		afterA := listTestAuditEntries(t, auditH, projectA)
		afterB := listTestAuditEntries(t, auditH, projectB)

		if len(afterA) != len(beforeA)+1 {
			t.Fatalf("project A's audit log should have grown by exactly 1 entry, went from %d to %d",
				len(beforeA), len(afterA))
		}
		if len(afterB) != len(beforeB) {
			t.Fatalf("project A's write leaked into project B's audit log: went from %d to %d entries",
				len(beforeB), len(afterB))
		}

		reportA := getTestAuditVerify(t, auditH, projectA)
		if !reportA.Intact {
			t.Fatalf("project A's chain should verify intact: %+v", reportA)
		}
		if reportA.TotalEntries != len(afterA) {
			t.Fatalf("VerifyChain TotalEntries = %d, want %d (must match ListAuditLog's own-project count)",
				reportA.TotalEntries, len(afterA))
		}

		reportB := getTestAuditVerify(t, auditH, projectB)
		if reportB.TotalEntries != len(afterB) {
			t.Fatalf("VerifyChain for project B TotalEntries = %d, want %d — must not include project A's entries",
				reportB.TotalEntries, len(afterB))
		}
	})

	// TEN-1a-3: before this fix, change_requests had no project_id at all —
	// ListChangeRequests (deliberately reachable by any authenticated user,
	// per the SEC-3 exemption) leaked every project's requests, and
	// Approve/RejectChangeRequest matched by id alone.
	t.Run("ListChangeRequests and ApproveChangeRequest never cross projects", func(t *testing.T) {
		crA := createTestChangeRequest(ctx, t, database, projectA, sharedKey)
		crB := createTestChangeRequest(ctx, t, database, projectB, sharedKey)

		listA := listTestChangeRequests(t, crH, projectA)
		assertExactlyOneChangeRequestWithID(t, listA, crA)

		listB := listTestChangeRequests(t, crH, projectB)
		assertExactlyOneChangeRequestWithID(t, listB, crB)

		req := newTenancyRequest(t, http.MethodPost, "/api/v1/change-requests/"+crB+"/approve",
			map[string]any{"approved_by": "someone"}, projectA, map[string]string{"id": crB})
		rec := httptest.NewRecorder()
		crH.ApproveChangeRequest(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("project A approving project B's change request: status = %d, want 404; body: %s",
				rec.Code, rec.Body.String())
		}

		req = newTenancyRequest(t, http.MethodPost, "/api/v1/change-requests/"+crA+"/approve",
			map[string]any{"approved_by": "someone"}, projectA, map[string]string{"id": crA})
		rec = httptest.NewRecorder()
		crH.ApproveChangeRequest(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("approving its own change request: status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("AddPrerequisite rejects a prereq_flag_key that only exists in another project", func(t *testing.T) {
		onlyInB := createTestFlag(t, flagH, projectB, "ten1a-only-in-b")

		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+sharedKey+"/prerequisites",
			map[string]any{"prereq_flag_key": onlyInB.Key}, projectA, map[string]string{"key": sharedKey})
		rec := httptest.NewRecorder()
		prereqH.AddPrerequisite(rec, req)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422 (prereq_flag_key must not resolve across projects); body: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("CreateSchedule rejects a key that only exists in another project", func(t *testing.T) {
		onlyInB := createTestFlag(t, flagH, projectB, "ten1a-schedule-only-in-b")

		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+onlyInB.Key+"/schedule", map[string]any{
			"environment":    "production",
			"scheduled_for":  time.Now().Add(5 * time.Minute).Unix(),
			"change_payload": map[string]any{"enabled": false},
		}, projectA, map[string]string{"key": onlyInB.Key})
		rec := httptest.NewRecorder()
		scheduledH.CreateSchedule(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (a caller must not be able to schedule a change against "+
				"another project's flag by guessing its key); body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("ArchiveFlag archives only the caller's own copy", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+sharedKey, nil, projectA,
			map[string]string{"key": sharedKey})
		rec := httptest.NewRecorder()
		flagH.ArchiveFlag(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("archive status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		gotA := getTestFlag(t, flagH, projectA, sharedKey)
		if gotA.State != "ARCHIVED" {
			t.Errorf("project A's flag state = %q, want ARCHIVED", gotA.State)
		}
		gotB := getTestFlag(t, flagH, projectB, sharedKey)
		if gotB.State == "ARCHIVED" {
			t.Fatal("project B's same-keyed flag was archived by project A's ArchiveFlag call — the exact cross-tenant bug TEN-1a fixes")
		}
	})
}

func createTestProject(ctx context.Context, t *testing.T, database *sql.DB, slug string) string {
	t.Helper()
	var id string
	err := database.QueryRowContext(ctx, `
		INSERT INTO projects (name, slug) VALUES ($1, $1)
		ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
		RETURNING id
	`, slug).Scan(&id)
	if err != nil {
		t.Fatalf("create test project %q: %v", slug, err)
	}
	return id
}

func newTenancyRequest(t *testing.T, method, path string, body any, projectID string, urlParams map[string]string) *http.Request {
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
	ctx = context.WithValue(ctx, middleware.ContextKeyActor, "tenancy-test")
	return req.WithContext(ctx)
}

func createTestFlag(t *testing.T, h *FlagHandler, projectID, key string) Flag {
	t.Helper()
	req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags", map[string]any{
		"key": key, "name": key, "flag_type": "BOOLEAN", "safe_default": "false",
	}, projectID, nil)
	rec := httptest.NewRecorder()
	h.CreateFlag(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("createTestFlag(%s, %s) status = %d, want 201; body: %s", projectID, key, rec.Code, rec.Body.String())
	}
	var f Flag
	if err := json.NewDecoder(rec.Body).Decode(&f); err != nil {
		t.Fatalf("decode create-flag response: %v", err)
	}
	return f
}

func getTestFlag(t *testing.T, h *FlagHandler, projectID, key string) Flag {
	t.Helper()
	req := newTenancyRequest(t, http.MethodGet, "/api/v1/flags/"+key, nil, projectID, map[string]string{"key": key})
	rec := httptest.NewRecorder()
	h.GetFlag(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("getTestFlag(%s, %s) status = %d, want 200; body: %s", projectID, key, rec.Code, rec.Body.String())
	}
	var f Flag
	if err := json.NewDecoder(rec.Body).Decode(&f); err != nil {
		t.Fatalf("decode get-flag response: %v", err)
	}
	return f
}

func listTestFlags(t *testing.T, h *FlagHandler, projectID string) []Flag {
	t.Helper()
	req := newTenancyRequest(t, http.MethodGet, "/api/v1/flags", nil, projectID, nil)
	rec := httptest.NewRecorder()
	h.ListFlags(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("listTestFlags(%s) status = %d, want 200; body: %s", projectID, rec.Code, rec.Body.String())
	}
	var out struct {
		Flags []Flag `json:"flags"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode list-flags response: %v", err)
	}
	return out.Flags
}

func assertExactlyOneFlagWithID(t *testing.T, flags []Flag, key, wantID string) {
	t.Helper()
	matches := 0
	for _, f := range flags {
		if f.Key != key {
			continue
		}
		matches++
		if f.ID != wantID {
			t.Errorf("ListFlags returned a flag keyed %q with id %s, want %s (cross-tenant leak)", key, f.ID, wantID)
		}
	}
	if matches != 1 {
		t.Errorf("ListFlags returned %d flags keyed %q, want exactly 1", matches, key)
	}
}

func updateTestEnvironment(t *testing.T, h *FlagHandler, projectID, key, env string, enabled bool, rolloutPct int) {
	t.Helper()
	req := newTenancyRequest(t, http.MethodPatch, "/api/v1/flags/"+key+"/environments/"+env, map[string]any{
		"enabled": enabled, "rollout_pct": rolloutPct,
	}, projectID, map[string]string{"key": key, "env": env})
	rec := httptest.NewRecorder()
	h.UpdateEnvironment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("updateTestEnvironment(%s, %s, %s) status = %d, want 200; body: %s",
			projectID, key, env, rec.Code, rec.Body.String())
	}
}

func killTestSwitch(t *testing.T, h *FlagHandler, projectID, key, env string) {
	t.Helper()
	req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+key+"/kill", map[string]any{
		"environment": env,
	}, projectID, map[string]string{"key": key})
	rec := httptest.NewRecorder()
	h.KillSwitch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("killTestSwitch(%s, %s, %s) status = %d, want 200; body: %s",
			projectID, key, env, rec.Code, rec.Body.String())
	}
}

func getTestSnapshot(t *testing.T, h *SnapshotHandler, projectID, env string) Snapshot {
	t.Helper()
	req := newTenancyRequest(t, http.MethodGet, "/api/v1/environments/snapshot?environment="+env, nil, projectID, nil)
	rec := httptest.NewRecorder()
	h.GetSnapshot(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("getTestSnapshot(%s, %s) status = %d, want 200; body: %s", projectID, env, rec.Code, rec.Body.String())
	}
	var s Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&s); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	return s
}

func findSnapshotEntry(s Snapshot, key string) *FlagEnvironmentStateWithPrereqs {
	for i := range s.Flags {
		if s.Flags[i].FlagKey == key {
			return &s.Flags[i]
		}
	}
	return nil
}

func listTestAuditEntries(t *testing.T, h *AuditHandler, projectID string) []AuditEntry {
	t.Helper()
	req := newTenancyRequest(t, http.MethodGet, "/api/v1/audit", nil, projectID, nil)
	rec := httptest.NewRecorder()
	h.ListAuditLog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("listTestAuditEntries(%s) status = %d, want 200; body: %s", projectID, rec.Code, rec.Body.String())
	}
	var out struct {
		Entries []AuditEntry `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode audit list response: %v", err)
	}
	return out.Entries
}

func getTestAuditVerify(t *testing.T, h *AuditHandler, projectID string) audit.VerifyReport {
	t.Helper()
	req := newTenancyRequest(t, http.MethodGet, "/api/v1/audit/verify", nil, projectID, nil)
	rec := httptest.NewRecorder()
	h.VerifyChain(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("getTestAuditVerify(%s) status = %d, want 200; body: %s", projectID, rec.Code, rec.Body.String())
	}
	var report audit.VerifyReport
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatalf("decode verify response: %v", err)
	}
	return report
}

// createTestChangeRequest inserts a PENDING change_requests row directly —
// there is no HTTP creation endpoint (real rows are only ever written by
// background processes: scim.go's detectOrphans and orphan_detector.go) —
// and returns its generated id.
func createTestChangeRequest(ctx context.Context, t *testing.T, database *sql.DB, projectID, flagKey string) string {
	t.Helper()
	var id string
	err := database.QueryRowContext(ctx, `
		INSERT INTO change_requests (flag_key, environment, requested_by, status, change_payload, project_id)
		VALUES ($1, 'production', 'system', 'PENDING', '{}', $2)
		RETURNING id
	`, flagKey, projectID).Scan(&id)
	if err != nil {
		t.Fatalf("create test change request: %v", err)
	}
	return id
}

func listTestChangeRequests(t *testing.T, h *ChangeRequestHandler, projectID string) []ChangeRequest {
	t.Helper()
	req := newTenancyRequest(t, http.MethodGet, "/api/v1/change-requests", nil, projectID, nil)
	rec := httptest.NewRecorder()
	h.ListChangeRequests(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("listTestChangeRequests(%s) status = %d, want 200; body: %s", projectID, rec.Code, rec.Body.String())
	}
	var out struct {
		Requests []ChangeRequest `json:"requests"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode change-requests list response: %v", err)
	}
	return out.Requests
}

func assertExactlyOneChangeRequestWithID(t *testing.T, requests []ChangeRequest, wantID string) {
	t.Helper()
	if len(requests) != 1 || requests[0].ID != wantID {
		t.Errorf("expected the list to contain exactly one change request (id %s), got %d: %+v — "+
			"the other project's change request must not appear here", wantID, len(requests), requests)
	}
}
