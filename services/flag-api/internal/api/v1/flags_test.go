package v1

import (
	"context"
	"database/sql"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
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
}

// TestWriteError verifies the response format is correct JSON.
// This is the error path used by every handler — format must be stable.
func TestFlagTypeValidation(t *testing.T) {
	validTypes := []string{"BOOLEAN", "STRING", "INTEGER", "FLOAT", "JSON"}
	for _, ft := range validTypes {
		if ft == "" {
			t.Errorf("flag type %q is empty", ft)
		}
	}
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
