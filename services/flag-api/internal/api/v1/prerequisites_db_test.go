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

	"github.com/tombstone/flag-api/internal/db"
)

// TestPrerequisitesAgainstPostgres is the real-Postgres regression proof this
// package was missing: an adversarial review of the DATA-1b sqlc conversion
// found that TestTenancyIsolation's ONE AddPrerequisite subtest always hits
// the early cross-project rejection (422) and never reaches InsertPrerequisite,
// meaning ListPrerequisitesForFlag, DeletePrerequisite, and
// ListPrereqFlagKeysForFlag (used inside detectCycle) had zero real-DB
// execution anywhere in the suite, and GetEnvironmentSnapshotPrerequisites
// (environments.go) was likewise only ever exercised with an empty result
// set. A parameter-order or column-mapping bug in any of these sqlc-generated
// queries (e.g. swapped gate/priority scan positions) would have shipped
// undetected. This file exercises every one of those paths against a real
// database, following the same TEST_DATABASE_URL-gated convention as
// chain_db_test.go/retention_db_test.go/tenancy_isolation_test.go.
func TestPrerequisitesAgainstPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed prerequisites test")
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

	projectID := createTestProject(ctx, t, database, "prereq-db-test-tenant")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	flagH := NewFlagHandler(database, rdb, logger, nil, nil, nil, "")
	prereqH := NewPrerequisiteHandler(database, logger)
	snapH := NewSnapshotHandler(database, logger)

	parent := createTestFlag(t, flagH, projectID, "prereq-db-parent")
	dep := createTestFlag(t, flagH, projectID, "prereq-db-dependency")

	var created Prerequisite

	t.Run("AddPrerequisite succeeds and InsertPrerequisite's RETURNING columns map correctly", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+parent.Key+"/prerequisites", map[string]any{
			"prereq_flag_key":    dep.Key,
			"required_variation": "false",
			"gate":               false,
			"priority":           7,
		}, projectID, map[string]string{"key": parent.Key})
		rec := httptest.NewRecorder()
		prereqH.AddPrerequisite(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
		}
		if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
			t.Fatalf("decode: %v", err)
		}

		// A gate/priority (or any other field) scan-position swap in the
		// generated InsertPrerequisite would show up exactly here — real
		// values chosen (gate=false, priority=7) deliberately differ from
		// AddPrerequisite's own defaults (true, 0), so a swap can't
		// accidentally look correct.
		if created.PrereqFlagKey != dep.Key {
			t.Errorf("PrereqFlagKey = %q, want %q", created.PrereqFlagKey, dep.Key)
		}
		if created.RequiredVariation != "false" {
			t.Errorf("RequiredVariation = %q, want %q", created.RequiredVariation, "false")
		}
		if created.Gate != false {
			t.Errorf("Gate = %v, want false", created.Gate)
		}
		if created.Priority != 7 {
			t.Errorf("Priority = %d, want 7", created.Priority)
		}
		if created.FlagID != parent.ID {
			t.Errorf("FlagID = %q, want parent flag id %q", created.FlagID, parent.ID)
		}
		if created.CreatedAt <= 0 {
			t.Errorf("CreatedAt = %d, want a positive unix timestamp", created.CreatedAt)
		}
	})

	t.Run("ListPrerequisites returns the real row with matching fields", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodGet, "/api/v1/flags/"+parent.Key+"/prerequisites", nil, projectID,
			map[string]string{"key": parent.Key})
		rec := httptest.NewRecorder()
		prereqH.ListPrerequisites(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Prerequisites []Prerequisite `json:"prerequisites"`
			Total         int            `json:"total"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Total != 1 {
			t.Fatalf("total = %d, want 1", resp.Total)
		}
		got := resp.Prerequisites[0]
		if got.ID != created.ID || got.PrereqFlagKey != dep.Key || got.Gate != false || got.Priority != 7 {
			t.Errorf("ListPrerequisitesForFlag returned %+v, want it to match the inserted row %+v", got, created)
		}
	})

	t.Run("GetSnapshot's prerequisites array reflects the real row (previously only ever tested empty)", func(t *testing.T) {
		snap := getTestSnapshot(t, snapH, projectID, "production")
		entry := findSnapshotEntry(snap, parent.Key)
		if entry == nil {
			t.Fatalf("no snapshot entry for %q", parent.Key)
		}
		if len(entry.Prerequisites) != 1 {
			t.Fatalf("Prerequisites = %+v, want exactly 1 entry", entry.Prerequisites)
		}
		p := entry.Prerequisites[0]
		if p.PrereqFlagKey != dep.Key || p.Gate != false || p.Priority != 7 || p.RequiredVariation != "false" {
			t.Errorf("GetEnvironmentSnapshotPrerequisites returned %+v, want it to match the inserted row", p)
		}
	})

	t.Run("AddPrerequisite detects a real cycle via ListPrereqFlagKeysForFlag (previously only ever tested empty)", func(t *testing.T) {
		// dep already depends on nothing; parent depends on dep (added above).
		// Making dep depend on parent would close a 2-hop cycle: this walks
		// ListPrereqFlagKeysForFlag with REAL rows present, not an empty table.
		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+dep.Key+"/prerequisites", map[string]any{
			"prereq_flag_key": parent.Key,
		}, projectID, map[string]string{"key": dep.Key})
		rec := httptest.NewRecorder()
		prereqH.AddPrerequisite(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (circular dependency); body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("DeletePrerequisite removes the real row", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+parent.Key+"/prerequisites/"+created.ID,
			nil, projectID, map[string]string{"key": parent.Key, "id": created.ID})
		rec := httptest.NewRecorder()
		prereqH.DeletePrerequisite(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		listReq := newTenancyRequest(t, http.MethodGet, "/api/v1/flags/"+parent.Key+"/prerequisites", nil, projectID,
			map[string]string{"key": parent.Key})
		listRec := httptest.NewRecorder()
		prereqH.ListPrerequisites(listRec, listReq)
		var resp struct {
			Total int `json:"total"`
		}
		if err := json.NewDecoder(listRec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Total != 0 {
			t.Fatalf("total after delete = %d, want 0", resp.Total)
		}
	})
}
