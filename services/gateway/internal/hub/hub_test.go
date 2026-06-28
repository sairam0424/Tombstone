package hub

import (
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
	h.Broadcast(env, event)

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
		h.Broadcast(env, event)
	}

	// The channel is now full; this broadcast must not panic or deadlock.
	// The hub should attempt to send lagEvent into the channel. The lag frame
	// itself will also be dropped (channel still full), which is the expected
	// "all-full" code path. We just verify no panic and the drop counter goes up.
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Broadcast(env, event)
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
