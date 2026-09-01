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

	"github.com/tombstone/flag-api/internal/db"
	"github.com/tombstone/flag-api/internal/middleware"
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

	flagH := NewFlagHandler(database, rdb, logger, nil, nil)
	snapH := NewSnapshotHandler(database, logger)
	prereqH := NewPrerequisiteHandler(database, logger)

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
