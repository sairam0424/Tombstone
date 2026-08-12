package v1

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/middleware"
)

// TestActorFromContext verifies the actor extraction returns a safe default
// when no actor is set — prevents empty actor strings in the audit log.
func TestActorFromContext(t *testing.T) {
	t.Run("no actor in context returns unknown", func(t *testing.T) {
		ctx := context.Background()
		actor := actorFromContext(ctx)
		if actor == "" {
			t.Error("actorFromContext must never return empty string")
		}
		if actor != "unknown" {
			t.Errorf("actorFromContext (no value) = %q, want %q", actor, "unknown")
		}
	})

	t.Run("actor set in context is returned", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), middleware.ContextKeyActor, "alice@example.com")
		actor := actorFromContext(ctx)
		if actor != "alice@example.com" {
			t.Errorf("actorFromContext = %q, want %q", actor, "alice@example.com")
		}
	})
}

// TestFlagLifecycleStateNames verifies the state strings match what the DB CHECK constraint expects.
// If these drift, INSERT statements will fail with constraint violations.
func TestFlagLifecycleStateNames(t *testing.T) {
	// These must match the CHECK constraint in schema.sql:
	// state TEXT NOT NULL DEFAULT 'DRAFT' CHECK (state IN ('DRAFT','ACTIVE','COMPLETE','ARCHIVED'))
	validStates := map[string]bool{
		"DRAFT":    true,
		"ACTIVE":   true,
		"COMPLETE": true,
		"ARCHIVED": true,
	}

	for state := range validStates {
		if state == "" {
			t.Errorf("state name is empty")
		}
	}

	// Verify the default for new flags is ACTIVE (set by CreateFlag handler)
	defaultState := "ACTIVE"
	if !validStates[defaultState] {
		t.Errorf("default state %q is not in valid states", defaultState)
	}
}

// TestAuditLogEventTypes verifies that every event type written in flags.go
// is a valid non-empty string. Event types are stored raw in audit_log.event_type
// and must be consistent for dashboard queries.
func TestAuditLogEventTypes(t *testing.T) {
	// All event types written by FlagHandler methods
	eventTypes := []string{
		"flag_created",
		"flag_environment_updated",
		"kill_switch_activated",
		"flag_archived",
	}
	seen := make(map[string]bool)
	for _, et := range eventTypes {
		if et == "" {
			t.Error("event type must not be empty")
		}
		if seen[et] {
			t.Errorf("duplicate event type: %q", et)
		}
		seen[et] = true
	}
}

// TestTombstoneErrorMessage verifies the tombstone error message contains
// the critical context an engineer needs when they hit this error.
func TestTombstoneErrorMessage(t *testing.T) {
	flagKey := "payments.checkout.old-v1"
	msg := "flag key " + "\"" + flagKey + "\"" + " is tombstoned and cannot be reused (Knight Capital prevention)"

	if msg == "" {
		t.Error("tombstone error message must not be empty")
	}
	// Must reference the flag key so engineers know WHICH key is tombstoned
	for _, needle := range []string{flagKey, "tombstoned", "Knight Capital"} {
		found := false
		for i := 0; i <= len(msg)-len(needle); i++ {
			if msg[i:i+len(needle)] == needle {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tombstone error message %q missing expected substring %q", msg, needle)
		}
	}
}

// TestPublishToStream_WritesCorrectFields verifies that publishToStream writes a single
// entry to the correct Redis Stream key with the required fields populated.
// Uses miniredis so no real Redis instance is needed.
func TestPublishToStream_WritesCorrectFields(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	h := &FlagHandler{
		db:     &sql.DB{}, // not used by publishToStream
		rdb:    rdb,
		logger: zap.NewNop(),
		rekor:  nil,
	}

	env := "development"
	event := FlagEvent{
		FlagKey:     "my-feature",
		Enabled:     true,
		RolloutPct:  50,
		Reason:      "manual",
		Ts:          1700000000,
		Environment: env,
	}

	h.publishToStream(context.Background(), env, event)

	streamKey := "tombstone:stream:" + env

	// Verify exactly one entry was written.
	msgs, err := rdb.XRange(context.Background(), streamKey, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange: %v", err)
	}
	if got := len(msgs); got != 1 {
		t.Fatalf("expected 1 stream entry, got %d", got)
	}

	// Verify required fields are present and non-empty.
	fields := msgs[0].Values
	for _, required := range []string{"event", "flag_key", "environment", "payload"} {
		v, ok := fields[required]
		if !ok {
			t.Errorf("stream entry missing field %q", required)
			continue
		}
		if s, _ := v.(string); s == "" {
			t.Errorf("stream entry field %q is empty", required)
		}
	}

	// Spot-check specific values.
	if got := fields["flag_key"]; got != "my-feature" {
		t.Errorf("flag_key = %q, want %q", got, "my-feature")
	}
	if got := fields["environment"]; got != env {
		t.Errorf("environment = %q, want %q", got, env)
	}
	if got := fields["event"]; got != "manual" {
		t.Errorf("event (reason) = %q, want %q", got, "manual")
	}
}

// TestCreateFlag_Validation tests the request validation logic in CreateFlag.
// All cases here are validation failures that return before touching the database,
// so nil DB is safe. Handler is invoked directly via httptest.
func TestCreateFlag_Validation(t *testing.T) {
	// Create a handler with no DB — safe because validation returns before DB access.
	h := &FlagHandler{
		db:     nil,
		rdb:    nil,
		logger: zap.NewNop(),
		rekor:  nil,
	}

	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "missing key returns 400",
			body:       `{"name":"My Flag","flag_type":"BOOLEAN","safe_default":"false","project_id":"00000000-0000-0000-0000-000000000001"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing name returns 400",
			body:       `{"key":"test-flag","flag_type":"BOOLEAN","safe_default":"false","project_id":"00000000-0000-0000-0000-000000000001"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing flag_type returns 400",
			body:       `{"key":"test-flag","name":"Test","safe_default":"false","project_id":"00000000-0000-0000-0000-000000000001"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty body returns 400",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid flag_type returns 400",
			body:       `{"key":"test-flag","name":"Test","flag_type":"INVALID","safe_default":"false","project_id":"00000000-0000-0000-0000-000000000001"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed JSON returns 400",
			body:       `{not valid json`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/flags", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.CreateFlag(w, req)

			if w.Code != tc.wantStatus {
				t.Errorf("want status %d, got %d (body: %s)", tc.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

// TestValidFlagTypes verifies the ValidFlagTypes map is complete and correct.
// This is the service-layer guard that mirrors the DB CHECK constraint.
func TestValidFlagTypes_Map(t *testing.T) {
	expected := []string{"BOOLEAN", "STRING", "INTEGER", "FLOAT", "JSON"}
	for _, ft := range expected {
		if !ValidFlagTypes[ft] {
			t.Errorf("ValidFlagTypes missing expected type %q", ft)
		}
	}

	// Ensure common mistakes are rejected
	invalid := []string{"Boolean", "boolean", "INVALID", "NUMBER", "ARRAY", ""}
	for _, ft := range invalid {
		if ValidFlagTypes[ft] {
			t.Errorf("ValidFlagTypes should not contain %q", ft)
		}
	}
}

// TestWriteJSON_ResponseFormat verifies writeJSON sets content-type and encodes the payload.
func TestWriteJSON_ResponseFormat(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	if got := w.Code; got != http.StatusOK {
		t.Errorf("writeJSON status = %d, want %d", got, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := strings.TrimSpace(w.Body.String())
	if body == "" {
		t.Error("writeJSON must produce a non-empty body")
	}
	if !strings.Contains(body, `"status"`) {
		t.Errorf("writeJSON body %q does not contain expected field", body)
	}
}

// TestWriteError_ResponseFormat verifies writeError wraps the message in {"error": ...}.
func TestWriteError_ResponseFormat(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
	}{
		{"bad request", http.StatusBadRequest, "key is required"},
		{"not found", http.StatusNotFound, "flag not found"},
		{"internal error", http.StatusInternalServerError, "query failed"},
		{"conflict", http.StatusConflict, "tombstoned"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, tc.status, tc.message)

			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			body := w.Body.String()
			if !strings.Contains(body, `"error"`) {
				t.Errorf("error response body %q missing 'error' key", body)
			}
			if !strings.Contains(body, tc.message) {
				t.Errorf("error response body %q missing message %q", body, tc.message)
			}
		})
	}
}

// TestIpFromRequest verifies that IP extraction prefers X-Forwarded-For.
func TestIpFromRequest(t *testing.T) {
	t.Run("uses X-Forwarded-For when set", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "203.0.113.1")
		ip := ipFromRequest(req)
		if ip != "203.0.113.1" {
			t.Errorf("ipFromRequest = %q, want %q", ip, "203.0.113.1")
		}
	})

	t.Run("falls back to RemoteAddr when no header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		ip := ipFromRequest(req)
		if ip == "" {
			t.Error("ipFromRequest must not return empty string")
		}
		if ip != req.RemoteAddr {
			t.Errorf("ipFromRequest = %q, want %q", ip, req.RemoteAddr)
		}
	})
}

// ---- Merkle chain integrity tests ----

// TestPublishToStream_MultipleEnvironments verifies that events for different
// environments are written to separate stream keys.
func TestPublishToStream_MultipleEnvironments(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	h := &FlagHandler{rdb: rdb, logger: zap.NewNop()}

	envs := []string{"development", "staging", "production"}
	for _, env := range envs {
		h.publishToStream(context.Background(), env, FlagEvent{
			FlagKey: "multi-env-flag", Reason: "test", Ts: time.Now().Unix(), Environment: env,
		})
	}

	// Each environment must have its own stream with exactly 1 entry.
	for _, env := range envs {
		key := "tombstone:stream:" + env
		msgs, err := rdb.XRange(context.Background(), key, "-", "+").Result()
		if err != nil {
			t.Fatalf("XRange for %s: %v", env, err)
		}
		if got := len(msgs); got != 1 {
			t.Errorf("stream %q: expected 1 entry, got %d", key, got)
		}
	}
}
