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

// TestTargetingRulesAgainstPostgres is the real-Postgres regression proof
// for targeting_rules -- the table has existed since the baseline schema
// with zero query file, zero REST endpoint, and zero snapshot wiring
// against it (confirmed while scoping the targeting_rules/target_list gap),
// so every sqlc-generated query this feature adds (InsertTargetingRule,
// ListTargetingRulesForFlag, DeleteTargetingRule,
// GetEnvironmentSnapshotTargetingRules) has ZERO prior real-DB execution
// anywhere. Follows the same TEST_DATABASE_URL-gated convention as
// prerequisites_db_test.go/tenancy_isolation_test.go.
func TestTargetingRulesAgainstPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed targeting_rules test")
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

	projectID := createTestProject(ctx, t, database, "targeting-rule-db-test-tenant")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	flagH := NewFlagHandler(database, rdb, logger, nil, nil, nil, "")
	ruleH := NewTargetingRuleHandler(database, rdb, logger)
	snapH := NewSnapshotHandler(database, logger)

	flag := createTestFlag(t, flagH, projectID, "targeting-rule-db-flag")

	var created TargetingRule

	t.Run("AddTargetingRule succeeds and InsertTargetingRule's RETURNING columns map correctly", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+flag.Key+"/environments/production/rules", map[string]any{
			"rule_type": "USER",
			"attribute": "email",
			"operator":  "CONTAINS",
			"values":    []string{"@acme.com"},
			"variation": "true",
			"priority":  3,
		}, projectID, map[string]string{"key": flag.Key, "env": "production"})
		rec := httptest.NewRecorder()
		ruleH.AddTargetingRule(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
		}
		if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
			t.Fatalf("decode: %v", err)
		}

		// Real values chosen (priority=3, a non-EQ operator, a non-empty
		// values array) deliberately differ from any handler default, so a
		// column-scan-position swap in the generated query can't
		// accidentally look correct.
		if created.FlagID != flag.ID {
			t.Errorf("FlagID = %q, want %q", created.FlagID, flag.ID)
		}
		if created.Environment != "production" {
			t.Errorf("Environment = %q, want %q", created.Environment, "production")
		}
		if created.RuleType != "USER" {
			t.Errorf("RuleType = %q, want %q", created.RuleType, "USER")
		}
		if created.Attribute != "email" {
			t.Errorf("Attribute = %q, want %q", created.Attribute, "email")
		}
		if created.Operator != "CONTAINS" {
			t.Errorf("Operator = %q, want %q", created.Operator, "CONTAINS")
		}
		if created.Variation != "true" {
			t.Errorf("Variation = %q, want %q", created.Variation, "true")
		}
		if created.Priority != 3 {
			t.Errorf("Priority = %d, want 3", created.Priority)
		}
		var values []string
		if err := json.Unmarshal(created.Values, &values); err != nil || len(values) != 1 || values[0] != "@acme.com" {
			t.Errorf("Values = %s, want [\"@acme.com\"]", created.Values)
		}
		if created.CreatedAt <= 0 {
			t.Errorf("CreatedAt = %d, want a positive unix timestamp", created.CreatedAt)
		}
	})

	t.Run("ListTargetingRules returns the real row with matching fields", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodGet, "/api/v1/flags/"+flag.Key+"/environments/production/rules", nil,
			projectID, map[string]string{"key": flag.Key, "env": "production"})
		rec := httptest.NewRecorder()
		ruleH.ListTargetingRules(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			TargetingRules []TargetingRule `json:"targeting_rules"`
			Total          int             `json:"total"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Total != 1 {
			t.Fatalf("total = %d, want 1", resp.Total)
		}
		got := resp.TargetingRules[0]
		if got.ID != created.ID || got.Attribute != "email" || got.Priority != 3 {
			t.Errorf("ListTargetingRulesForFlag returned %+v, want it to match the inserted row %+v", got, created)
		}
	})

	t.Run("a DIFFERENT environment sees ZERO rules for the same flag — per-environment scoping, unlike global prerequisites", func(t *testing.T) {
		// The one behavior that is genuinely NEW here versus
		// flag_prerequisites (which has no environment column at all and
		// applies globally) -- this is the specific regression this test
		// exists to catch.
		req := newTenancyRequest(t, http.MethodGet, "/api/v1/flags/"+flag.Key+"/environments/staging/rules", nil,
			projectID, map[string]string{"key": flag.Key, "env": "staging"})
		rec := httptest.NewRecorder()
		ruleH.ListTargetingRules(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Total int `json:"total"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Total != 0 {
			t.Fatalf("total in staging = %d, want 0 — a production-scoped rule must not leak into another environment", resp.Total)
		}
	})

	t.Run("GetSnapshot's targeting_rules array reflects the real row (previously only ever tested empty)", func(t *testing.T) {
		snap := getTestSnapshot(t, snapH, projectID, "production")
		entry := findSnapshotEntry(snap, flag.Key)
		if entry == nil {
			t.Fatalf("no snapshot entry for %q", flag.Key)
		}
		if len(entry.TargetingRules) != 1 {
			t.Fatalf("TargetingRules = %+v, want exactly 1 entry", entry.TargetingRules)
		}
		got := entry.TargetingRules[0]
		if got.Attribute != "email" || got.Operator != "CONTAINS" || got.Priority != 3 {
			t.Errorf("GetEnvironmentSnapshotTargetingRules returned %+v, want it to match the inserted row", got)
		}

		stagingSnap := getTestSnapshot(t, snapH, projectID, "staging")
		stagingEntry := findSnapshotEntry(stagingSnap, flag.Key)
		if stagingEntry == nil {
			t.Fatalf("no staging snapshot entry for %q", flag.Key)
		}
		if len(stagingEntry.TargetingRules) != 0 {
			t.Errorf("staging TargetingRules = %+v, want empty — a production-scoped rule must not leak into another environment's snapshot", stagingEntry.TargetingRules)
		}
	})

	t.Run("AddTargetingRule 404s for a nonexistent flag", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/never-created-flag/environments/production/rules", map[string]any{
			"rule_type": "USER", "attribute": "email", "operator": "EQ", "variation": "true",
		}, projectID, map[string]string{"key": "never-created-flag", "env": "production"})
		rec := httptest.NewRecorder()
		ruleH.AddTargetingRule(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("AddTargetingRule 404s for a nonexistent/typo'd environment — regression for the orphaned-rule finding", func(t *testing.T) {
		// Before the fix, this returned 201 and silently created a rule no
		// SDK could ever reach (targeting_rules.environment has no FK to
		// flag_environments, unlike flag_id's FK to flags).
		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+flag.Key+"/environments/produciton/rules", map[string]any{
			"rule_type": "USER", "attribute": "email", "operator": "EQ", "variation": "true",
		}, projectID, map[string]string{"key": flag.Key, "env": "produciton"})
		rec := httptest.NewRecorder()
		ruleH.AddTargetingRule(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}

		// Confirm no orphaned row was left behind by a partial insert.
		listReq := newTenancyRequest(t, http.MethodGet, "/api/v1/flags/"+flag.Key+"/environments/produciton/rules", nil,
			projectID, map[string]string{"key": flag.Key, "env": "produciton"})
		listRec := httptest.NewRecorder()
		ruleH.ListTargetingRules(listRec, listReq)
		var resp struct {
			Total int `json:"total"`
		}
		if err := json.NewDecoder(listRec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Total != 0 {
			t.Fatalf("total for typo'd environment = %d, want 0 — the rejected request must not have inserted anything", resp.Total)
		}
	})

	t.Run("DeleteTargetingRule 404s for a rule that exists but in the WRONG environment", func(t *testing.T) {
		// `created` (from the AddTargetingRule subtest above) lives in
		// "production" -- targeting it via "staging" must not match.
		req := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+flag.Key+"/environments/staging/rules/"+created.ID,
			nil, projectID, map[string]string{"key": flag.Key, "env": "staging", "id": created.ID})
		rec := httptest.NewRecorder()
		ruleH.DeleteTargetingRule(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
		}

		// Confirm the row is still there in its real environment.
		listReq := newTenancyRequest(t, http.MethodGet, "/api/v1/flags/"+flag.Key+"/environments/production/rules", nil,
			projectID, map[string]string{"key": flag.Key, "env": "production"})
		listRec := httptest.NewRecorder()
		ruleH.ListTargetingRules(listRec, listReq)
		var resp struct {
			Total int `json:"total"`
		}
		if err := json.NewDecoder(listRec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Total != 1 {
			t.Fatalf("total in production after the failed cross-environment delete = %d, want 1 — the row must survive", resp.Total)
		}
	})

	t.Run("DeleteTargetingRule removes the real row", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+flag.Key+"/environments/production/rules/"+created.ID,
			nil, projectID, map[string]string{"key": flag.Key, "env": "production", "id": created.ID})
		rec := httptest.NewRecorder()
		ruleH.DeleteTargetingRule(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		listReq := newTenancyRequest(t, http.MethodGet, "/api/v1/flags/"+flag.Key+"/environments/production/rules", nil,
			projectID, map[string]string{"key": flag.Key, "env": "production"})
		listRec := httptest.NewRecorder()
		ruleH.ListTargetingRules(listRec, listReq)
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

// TestTargetingRulesPublishLiveUpdateEvent proves AddTargetingRule/
// DeleteTargetingRule actually publish a live "targeting_rules_updated"
// event to the Redis Stream, carrying the flag's real, current, FULL
// targeting-rule list FOR THAT ENVIRONMENT -- and, critically, that it does
// NOT fan out to every environment the way PrerequisitesEvent does (since
// targeting_rules is per-environment, unlike global prerequisites).
func TestTargetingRulesPublishLiveUpdateEvent(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed targeting_rules test")
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

	projectID := createTestProject(ctx, t, database, "targeting-rule-live-event-tenant")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	flagH := NewFlagHandler(database, rdb, logger, nil, nil, nil, "")
	ruleH := NewTargetingRuleHandler(database, rdb, logger)

	flag := createTestFlag(t, flagH, projectID, "targeting-rule-live-flag")

	var created TargetingRule
	t.Run("AddTargetingRule publishes ONLY to its own environment, with the real rule list", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+flag.Key+"/environments/production/rules", map[string]any{
			"rule_type": "ORG",
			"attribute": "orgId",
			"operator":  "IN",
			"values":    []string{"org-1", "org-2"},
			"variation": "true",
		}, projectID, map[string]string{"key": flag.Key, "env": "production"})
		rec := httptest.NewRecorder()
		ruleH.AddTargetingRule(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
		}
		if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
			t.Fatalf("decode: %v", err)
		}

		msgs, err := rdb.XRange(ctx, "tombstone:stream:production", "-", "+").Result()
		if err != nil {
			t.Fatalf("XRange(production): %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("tombstone:stream:production: got %d entries, want exactly 1", len(msgs))
		}
		fields := msgs[0].Values
		if got := fields["kind"]; got != targetingRulesEventKind {
			t.Errorf("kind = %q, want %q", got, targetingRulesEventKind)
		}
		if got := fields["event"]; got != "targeting_rules_updated" {
			t.Errorf("event = %q, want %q", got, "targeting_rules_updated")
		}
		if got := fields["flag_key"]; got != flag.Key {
			t.Errorf("flag_key = %q, want %q", got, flag.Key)
		}
		if got := fields["environment"]; got != "production" {
			t.Errorf("environment field = %q, want %q", got, "production")
		}

		var evt TargetingRulesEvent
		payload, _ := fields["payload"].(string)
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			t.Fatalf("payload unmarshal: %v", err)
		}
		if evt.FlagKey != flag.Key || evt.Environment != "production" {
			t.Errorf("payload = %+v, want flag_key=%q environment=%q", evt, flag.Key, "production")
		}
		if len(evt.TargetingRules) != 1 || evt.TargetingRules[0].Attribute != "orgId" || evt.TargetingRules[0].Operator != "IN" {
			t.Errorf("payload.targeting_rules = %+v, want exactly 1 entry matching the real inserted row", evt.TargetingRules)
		}

		// The critical difference from PrerequisitesEvent: NO fan-out.
		for _, otherEnv := range []string{"development", "staging"} {
			otherMsgs, err := rdb.XRange(ctx, "tombstone:stream:"+otherEnv, "-", "+").Result()
			if err != nil {
				t.Fatalf("XRange(%s): %v", otherEnv, err)
			}
			if len(otherMsgs) != 0 {
				t.Errorf("tombstone:stream:%s: got %d entries, want 0 — a production-scoped rule change must not fan out to other environments", otherEnv, len(otherMsgs))
			}
		}
	})

	t.Run("DeleteTargetingRule publishes an updated (now empty) rule list, still only to production", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+flag.Key+"/environments/production/rules/"+created.ID,
			nil, projectID, map[string]string{"key": flag.Key, "env": "production", "id": created.ID})
		rec := httptest.NewRecorder()
		ruleH.DeleteTargetingRule(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		msgs, err := rdb.XRange(ctx, "tombstone:stream:production", "-", "+").Result()
		if err != nil {
			t.Fatalf("XRange: %v", err)
		}
		if len(msgs) != 2 {
			t.Fatalf("tombstone:stream:production: got %d entries, want exactly 2 (add + delete)", len(msgs))
		}
		var evt TargetingRulesEvent
		payload, _ := msgs[1].Values["payload"].(string)
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			t.Fatalf("payload unmarshal: %v", err)
		}
		if len(evt.TargetingRules) != 0 {
			t.Errorf("payload.targeting_rules after delete = %+v, want empty", evt.TargetingRules)
		}
	})
}
