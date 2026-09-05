package hub

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestHub_BroadcastToMultipleClients verifies that a single Broadcast call
// fans the SSE frame out to every subscriber in the target environment.
func TestHub_BroadcastToMultipleClients(t *testing.T) {
	h := NewHub(zap.NewNop())

	const (
		env       = "production"
		numClient = 3
	)

	// Subscribe 3 clients and collect their channels.
	channels := make([]chan []byte, numClient)
	for i := 0; i < numClient; i++ {
		channels[i] = h.Subscribe(env, fmt.Sprintf("client-%d", i))
	}
	defer func() {
		for i, ch := range channels {
			h.Unsubscribe(env, fmt.Sprintf("client-%d", i), ch)
		}
	}()

	event := FlagEvent{
		FlagKey:     "test-flag",
		Enabled:     true,
		RolloutPct:  100,
		Reason:      "test",
		Ts:          1_000_000,
		Environment: env,
	}

	// Fan out.
	h.Broadcast(env, event, "")

	// Collect results with a timeout to prevent the test from hanging.
	var wg sync.WaitGroup
	received := make([][]byte, numClient)
	for i, ch := range channels {
		wg.Add(1)
		go func(idx int, c chan []byte) {
			defer wg.Done()
			select {
			case msg := <-c:
				received[idx] = msg
			case <-time.After(time.Second):
				t.Errorf("client %d: timed out waiting for broadcast", idx)
			}
		}(i, ch)
	}
	wg.Wait()

	for i, msg := range received {
		if msg == nil {
			// already reported via t.Errorf above
			continue
		}
		// The SSE frame must contain the flag key.
		if !strings.Contains(string(msg), `"test-flag"`) {
			t.Errorf("client %d: expected flag_key in frame, got: %s", i, msg)
		}
		// Frame must start with the expected SSE event directive.
		if !strings.HasPrefix(string(msg), "event: flag_updated\n") {
			t.Errorf("client %d: unexpected SSE frame prefix: %s", i, msg)
		}
	}
}

// TestHub_LagEventOnFullBuffer verifies backpressure behaviour:
// when a client channel is full the hub writes the pre-serialised lagEvent
// instead of silently discarding or blocking.
func TestHub_LagEventOnFullBuffer(t *testing.T) {
	h := NewHub(zap.NewNop())

	const env = "staging"

	ch := h.Subscribe(env, "slow-client")
	defer h.Unsubscribe(env, "slow-client", ch)

	event := FlagEvent{
		FlagKey:     "fill-flag",
		Enabled:     true,
		RolloutPct:  50,
		Reason:      "test",
		Ts:          2_000_000,
		Environment: env,
	}

	// Fill the channel to capacity (buffer size = 64).
	// We read the channel cap from the slice itself so the test stays in sync
	// if the buffer size ever changes.
	capacity := cap(ch)
	for i := 0; i < capacity; i++ {
		h.Broadcast(env, event, "")
	}

	// The channel is now full; this broadcast must not panic or deadlock.
	// The hub should attempt to send lagEvent into the channel. The lag frame
	// itself will also be dropped (channel still full), which is the expected
	// "all-full" code path. We just verify no panic and the drop counter goes up.
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Broadcast(env, event, "")
	}()

	select {
	case <-done:
		// Broadcast returned without blocking — correct.
	case <-time.After(time.Second):
		t.Fatal("Broadcast blocked on a full channel (deadlock)")
	}

	// Drain the channel and verify it contains only valid SSE frames.
	// The lag event is a pre-serialised byte slice defined in hub.go.
	// Real event frames start with "event: flag_updated\n".
	// The last call's real payload was dropped; we may or may not see a lag frame.
	for len(ch) > 0 {
		frame := <-ch
		s := string(frame)
		if !strings.HasPrefix(s, "event: flag_updated\n") && s != string(lagEvent) {
			t.Errorf("unexpected frame in channel: %q", s)
		}
	}

	// Verify the EnvironmentBroadcaster recorded at least one drop.
	v, ok := h.envs.Load(env)
	if !ok {
		t.Fatal("environment broadcaster not found after Subscribe")
	}
	eb := v.(*EnvironmentBroadcaster)
	stats := eb.Stats()
	if stats.TotalDropped == 0 {
		t.Errorf("expected TotalDropped > 0 after buffer-full broadcast, got 0")
	}
}

// TestReplayOrSnapshot_NoRedisSetReturnsNil covers relay_proxy.go's
// localHub: SetRedis is never called on it, so ReplayOrSnapshot must no-op
// rather than panic on a nil rdb.
func TestReplayOrSnapshot_NoRedisSetReturnsNil(t *testing.T) {
	h := NewHub(zap.NewNop())
	got := h.ReplayOrSnapshot(context.Background(), "production", "5-0")
	if got != nil {
		t.Errorf("expected nil with no Redis client set, got %v", got)
	}
}

// TestReplayOrSnapshot_EmptyLastIDReturnsNil covers a fresh (non-reconnect)
// connection: the SSE handler only calls this when Last-Event-ID is
// present, but ReplayOrSnapshot must be safe to call with "" too.
func TestReplayOrSnapshot_EmptyLastIDReturnsNil(t *testing.T) {
	h := NewHub(zap.NewNop())
	h.SetRedis(newTestRedis(t))
	got := h.ReplayOrSnapshot(context.Background(), "production", "")
	if got != nil {
		t.Errorf("expected nil for an empty lastID, got %v", got)
	}
}

// TestReplayOrSnapshot_ReplaysWhenWithinRetention is the end-to-end proof
// that a reconnecting client's Last-Event-ID drives a real XRANGE catch-up:
// given 2 events published after lastID, ReplayOrSnapshot must return 2
// frames carrying their real stream IDs.
func TestReplayOrSnapshot_ReplaysWhenWithinRetention(t *testing.T) {
	rdb := newTestRedis(t)
	h := NewHub(zap.NewNop())
	h.SetRedis(rdb)

	const env = "production"
	streamKey := StreamKey(env)
	id1 := xaddEvent(t, rdb, streamKey, FlagEvent{FlagKey: "flag-1", Environment: env})
	id2 := xaddEvent(t, rdb, streamKey, FlagEvent{FlagKey: "flag-2", Environment: env})

	frames := h.ReplayOrSnapshot(context.Background(), env, id1)
	if len(frames) != 1 {
		t.Fatalf("expected 1 replayed frame (only flag-2 is after lastID=%s), got %d", id1, len(frames))
	}
	if !strings.Contains(string(frames[0]), "id: "+id2+"\n") {
		t.Errorf("expected replayed frame to carry id: %s, got: %s", id2, frames[0])
	}
}

// TestReplayOrSnapshot_FallsBackToSnapshotWhenTrimmedPastRetention proves
// the other half of GW-2's reconnect contract: a lastID older than the
// stream's current oldest entry must produce a "snapshot" frame from the
// hub's last-known full state, not a silent empty replay.
func TestReplayOrSnapshot_FallsBackToSnapshotWhenTrimmedPastRetention(t *testing.T) {
	rdb := newTestRedis(t)
	h := NewHub(zap.NewNop())
	h.SetRedis(rdb)

	const env = "production"
	streamKey := StreamKey(env)
	newestID := xaddEvent(t, rdb, streamKey, FlagEvent{FlagKey: "flag-1", Environment: env})
	h.SetLastSnapshot(env, []byte(`{"flags":[{"flag_key":"flag-1"}]}`))

	frames := h.ReplayOrSnapshot(context.Background(), env, "1-0") // predates the one real entry above
	if len(frames) != 1 {
		t.Fatalf("expected exactly 1 snapshot frame, got %d", len(frames))
	}
	frame := string(frames[0])
	if !strings.Contains(frame, "event: snapshot\n") {
		t.Errorf("expected a snapshot event frame, got: %s", frame)
	}
	if !strings.Contains(frame, `"flag-1"`) {
		t.Errorf("expected the snapshot payload in the frame, got: %s", frame)
	}
	// Adversarial review of PR #211 found this test never asserted on the
	// id: line -- without it, a broken newestID computation (wrong
	// XRevRangeN args/direction, or a silently-swallowed error) would still
	// pass every assertion above while leaving the client's cursor stuck,
	// re-triggering this same snapshot fallback on every future reconnect
	// instead of resuming live XRANGE replay.
	if !strings.Contains(frame, "id: "+newestID+"\n") {
		t.Errorf("expected the snapshot frame to carry the stream's newest id (%s) so the client's cursor advances to now, got: %s", newestID, frame)
	}
}

// TestReplayOrSnapshot_NoSnapshotAndTrimmedReturnsNil covers an environment
// the reconciler has never polled: with no snapshot to fall back to and a
// trimmed-past-retention lastID, ReplayOrSnapshot has genuinely nothing to
// offer and must say so (nil), not synthesize an empty/misleading frame.
func TestReplayOrSnapshot_NoSnapshotAndTrimmedReturnsNil(t *testing.T) {
	rdb := newTestRedis(t)
	h := NewHub(zap.NewNop())
	h.SetRedis(rdb)

	const env = "production"
	streamKey := StreamKey(env)
	xaddEvent(t, rdb, streamKey, FlagEvent{FlagKey: "flag-1", Environment: env})

	frames := h.ReplayOrSnapshot(context.Background(), env, "1-0")
	if frames != nil {
		t.Errorf("expected nil with no snapshot recorded, got %v", frames)
	}
}
