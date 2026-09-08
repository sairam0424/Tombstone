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

// TestScheduledChangeLifecycle is the executable gate for DATA-1b PR 2/4's
// conversion of ListSchedule/CancelSchedule to sqlc. Both were previously
// built with dynamically-appended WHERE clauses / variable placeholder
// counts and are now single static queries using an empty-string-sentinel
// pattern (see queries/scheduled.sql). TestTenancyIsolation only exercises
// CreateSchedule's 404 path — this test is the first real-Postgres coverage
// of ListSchedule's environment/status filters and of CancelSchedule at all.
func TestScheduledChangeLifecycle(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed scheduled-change test")
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

	projectID := createTestProject(ctx, t, database, "sched-lifecycle-test")
	otherProjectID := createTestProject(ctx, t, database, "sched-lifecycle-test-other")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	auditKey, err := secrets.NewAuditKey("sched-lifecycle-test-key-00000000000000", "")
	if err != nil {
		t.Fatalf("audit key: %v", err)
	}
	auditW := audit.NewWriter(database, auditKey)

	flagH := NewFlagHandler(database, rdb, logger, nil, auditW, nil, "")
	scheduledH := NewScheduledHandler(database, rdb, logger, auditW)

	flag := createTestFlag(t, flagH, projectID, "sched-lifecycle-flag")

	createSchedule := func(t *testing.T, env string, changePayload map[string]any) ScheduledChange {
		t.Helper()
		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+flag.Key+"/schedule", map[string]any{
			"environment":    env,
			"scheduled_for":  time.Now().Add(5 * time.Minute).Unix(),
			"change_payload": changePayload,
		}, projectID, map[string]string{"key": flag.Key})
		rec := httptest.NewRecorder()
		scheduledH.CreateSchedule(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("createSchedule(%s) status = %d, want 201; body: %s", env, rec.Code, rec.Body.String())
		}
		var sc ScheduledChange
		if err := json.NewDecoder(rec.Body).Decode(&sc); err != nil {
			t.Fatalf("decode create-schedule response: %v", err)
		}
		return sc
	}

	listSchedule := func(t *testing.T, envFilter, statusFilter string) []ScheduledChange {
		t.Helper()
		path := "/api/v1/flags/" + flag.Key + "/schedule"
		q := ""
		if envFilter != "" {
			q += "environment=" + envFilter
		}
		if statusFilter != "" {
			if q != "" {
				q += "&"
			}
			q += "status=" + statusFilter
		}
		if q != "" {
			path += "?" + q
		}
		req := newTenancyRequest(t, http.MethodGet, path, nil, projectID, map[string]string{"key": flag.Key})
		rec := httptest.NewRecorder()
		scheduledH.ListSchedule(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ListSchedule status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			ScheduledChanges []ScheduledChange `json:"scheduled_changes"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode list-schedule response: %v", err)
		}
		return resp.ScheduledChanges
	}

	prod := createSchedule(t, "production", map[string]any{"enabled": true})
	staging := createSchedule(t, "staging", map[string]any{"enabled": false})

	t.Run("no filters returns every scheduled change for the flag", func(t *testing.T) {
		got := listSchedule(t, "", "")
		if len(got) != 2 {
			t.Fatalf("len(got) = %d, want 2; got: %+v", len(got), got)
		}
	})

	t.Run("environment filter narrows to a single environment", func(t *testing.T) {
		got := listSchedule(t, "production", "")
		if len(got) != 1 || got[0].ID != prod.ID {
			t.Fatalf("environment=production returned %+v, want exactly [%s]", got, prod.ID)
		}
	})

	t.Run("status filter narrows to a single status", func(t *testing.T) {
		got := listSchedule(t, "", "PENDING")
		if len(got) != 2 {
			t.Fatalf("status=PENDING returned %d rows, want 2 (both start PENDING); got: %+v", len(got), got)
		}
	})

	t.Run("environment and status filters compose", func(t *testing.T) {
		got := listSchedule(t, "staging", "PENDING")
		if len(got) != 1 || got[0].ID != staging.ID {
			t.Fatalf("environment=staging&status=PENDING returned %+v, want exactly [%s]", got, staging.ID)
		}
	})

	t.Run("a filter value that matches nothing returns an empty list, not an error", func(t *testing.T) {
		got := listSchedule(t, "", "CANCELLED")
		if len(got) != 0 {
			t.Fatalf("status=CANCELLED returned %d rows, want 0; got: %+v", len(got), got)
		}
	})

	t.Run("ListSchedule does not leak another project's scheduled changes for the same flag key", func(t *testing.T) {
		otherFlag := createTestFlag(t, flagH, otherProjectID, flag.Key)
		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+otherFlag.Key+"/schedule", map[string]any{
			"environment":    "production",
			"scheduled_for":  time.Now().Add(5 * time.Minute).Unix(),
			"change_payload": map[string]any{"enabled": true},
		}, otherProjectID, map[string]string{"key": otherFlag.Key})
		rec := httptest.NewRecorder()
		scheduledH.CreateSchedule(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("cross-project setup: status = %d, want 201; body: %s", rec.Code, rec.Body.String())
		}

		got := listSchedule(t, "", "")
		if len(got) != 2 {
			t.Fatalf("project A's ListSchedule returned %d rows after project B created a same-keyed schedule, want 2 (unchanged); got: %+v", len(got), got)
		}
	})

	t.Run("CancelSchedule cancels a PENDING change", func(t *testing.T) {
		sc := createSchedule(t, "cancel-target-env", map[string]any{"enabled": true})
		req := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+flag.Key+"/schedule/"+sc.ID, nil,
			projectID, map[string]string{"key": flag.Key, "id": sc.ID})
		rec := httptest.NewRecorder()
		scheduledH.CancelSchedule(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
		}

		got := listSchedule(t, "cancel-target-env", "")
		if len(got) != 1 || got[0].Status != "CANCELLED" {
			t.Fatalf("after cancel, got %+v, want exactly one CANCELLED row", got)
		}
	})

	t.Run("CancelSchedule on an already-cancelled change returns 409", func(t *testing.T) {
		sc := createSchedule(t, "cancel-twice-env", map[string]any{"enabled": true})
		req := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+flag.Key+"/schedule/"+sc.ID, nil,
			projectID, map[string]string{"key": flag.Key, "id": sc.ID})
		rec := httptest.NewRecorder()
		scheduledH.CancelSchedule(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("first cancel: status = %d, want 204; body: %s", rec.Code, rec.Body.String())
		}

		req2 := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+flag.Key+"/schedule/"+sc.ID, nil,
			projectID, map[string]string{"key": flag.Key, "id": sc.ID})
		rec2 := httptest.NewRecorder()
		scheduledH.CancelSchedule(rec2, req2)
		if rec2.Code != http.StatusConflict {
			t.Fatalf("second cancel: status = %d, want 409; body: %s", rec2.Code, rec2.Body.String())
		}
	})

	t.Run("CancelSchedule on a well-formed but nonexistent id returns 404", func(t *testing.T) {
		// id is a real (RFC 4122) UUID that was never inserted — the id column
		// is typed uuid, so a non-UUID string (e.g. "does-not-exist") fails at
		// the Postgres driver level with "invalid input syntax for type uuid"
		// and surfaces as 500, both before and after this sqlc conversion.
		// That pre-existing behavior is out of scope for this PR to change.
		const nonexistentID = "00000000-0000-0000-0000-000000000000"
		req := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+flag.Key+"/schedule/"+nonexistentID, nil,
			projectID, map[string]string{"key": flag.Key, "id": nonexistentID})
		rec := httptest.NewRecorder()
		scheduledH.CancelSchedule(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("CancelSchedule cannot cancel another project's scheduled change by id", func(t *testing.T) {
		otherFlag := createTestFlag(t, flagH, otherProjectID, "sched-lifecycle-flag-cross")
		reqCreate := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+otherFlag.Key+"/schedule", map[string]any{
			"environment":    "production",
			"scheduled_for":  time.Now().Add(5 * time.Minute).Unix(),
			"change_payload": map[string]any{"enabled": true},
		}, otherProjectID, map[string]string{"key": otherFlag.Key})
		recCreate := httptest.NewRecorder()
		scheduledH.CreateSchedule(recCreate, reqCreate)
		if recCreate.Code != http.StatusCreated {
			t.Fatalf("cross-project setup: status = %d, want 201; body: %s", recCreate.Code, recCreate.Body.String())
		}
		var otherSc ScheduledChange
		if err := json.NewDecoder(recCreate.Body).Decode(&otherSc); err != nil {
			t.Fatalf("decode cross-project create response: %v", err)
		}

		req := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+otherFlag.Key+"/schedule/"+otherSc.ID, nil,
			projectID, map[string]string{"key": otherFlag.Key, "id": otherSc.ID})
		rec := httptest.NewRecorder()
		scheduledH.CancelSchedule(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (a project-A caller must not cancel project-B's scheduled change); body: %s",
				rec.Code, rec.Body.String())
		}
	})
}
