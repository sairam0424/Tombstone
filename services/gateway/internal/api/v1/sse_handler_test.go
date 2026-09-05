package v1

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/tombstone/gateway/internal/hub"
	"go.uber.org/zap"
)

// xaddTestEvent publishes a FlagEvent onto streamKey the same way flag-api's
// publishFlagEventToStream does, and returns the real Redis-assigned entry
// ID (used as the test's Last-Event-ID input).
func xaddTestEvent(t *testing.T, rdb *redis.Client, streamKey string, event hub.FlagEvent) string {
	t.Helper()
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	id, err := rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{
			"event":       event.Reason,
			"flag_key":    event.FlagKey,
			"environment": event.Environment,
			"payload":     string(payload),
		},
	}).Result()
	if err != nil {
		t.Fatalf("xadd: %v", err)
	}
	return id
}

// TestSSEHandler_LastEventIDReplaysOnlyMissedEvents is the end-to-end proof
// of GW-2's reconnect contract at the HTTP layer: a client reconnecting with
// Last-Event-ID set to an existing entry must receive exactly the events
// published strictly after it (via XRANGE), each carrying its own real
// id: line, and must NOT see the entry named by Last-Event-ID itself
// replayed a second time.
func TestSSEHandler_LastEventIDReplaysOnlyMissedEvents(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	const env = "production"
	streamKey := hub.StreamKey(env)

	h := hub.NewHub(zap.NewNop())
	h.SetRedis(rdb)

	lastSeenID := xaddTestEvent(t, rdb, streamKey, hub.FlagEvent{FlagKey: "already-seen", Environment: env})
	missedID := xaddTestEvent(t, rdb, streamKey, hub.FlagEvent{FlagKey: "missed-while-disconnected", Environment: env})

	sseH := NewSSEHandler(h, zap.NewNop())

	req := httptest.NewRequest("GET", "/api/v1/stream?environment="+env, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Last-Event-ID", lastSeenID)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		sseH.Stream(rec, req)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after context cancellation")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: connected") {
		t.Errorf("expected a connected event, got body: %s", body)
	}
	if !strings.Contains(body, "id: "+missedID) {
		t.Errorf("expected the missed event's real id (%s) in the replay, got body: %s", missedID, body)
	}
	if !strings.Contains(body, `"missed-while-disconnected"`) {
		t.Errorf("expected the missed event's flag_key in the replay, got body: %s", body)
	}
	if strings.Contains(body, `"already-seen"`) {
		t.Errorf("did not expect the already-seen event (named by Last-Event-ID itself) to be replayed, got body: %s", body)
	}
}

// TestSSEHandler_NoLastEventIDSkipsReplay covers the common case: a fresh
// (non-reconnecting) client sends no Last-Event-ID header, so no XRANGE
// call should happen and no replay frames should appear — only the
// connected event, matching pre-GW-2 behavior exactly.
func TestSSEHandler_NoLastEventIDSkipsReplay(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	const env = "production"
	streamKey := hub.StreamKey(env)

	h := hub.NewHub(zap.NewNop())
	h.SetRedis(rdb)
	xaddTestEvent(t, rdb, streamKey, hub.FlagEvent{FlagKey: "pre-existing", Environment: env})

	sseH := NewSSEHandler(h, zap.NewNop())

	req := httptest.NewRequest("GET", "/api/v1/stream?environment="+env, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		sseH.Stream(rec, req)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after context cancellation")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: connected") {
		t.Errorf("expected a connected event, got body: %s", body)
	}
	if strings.Contains(body, `"pre-existing"`) {
		t.Errorf("expected no replay without a Last-Event-ID header, got body: %s", body)
	}
}
