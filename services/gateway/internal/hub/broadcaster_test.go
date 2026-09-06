package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// newTestBroadcaster builds a Broadcaster with an EXPLICIT group name
// (bypassing NewBroadcaster's os.Hostname()-derived one) so tests can
// simulate multiple distinct replicas sharing one Redis instance within a
// single test process, where os.Hostname() would otherwise be identical
// for every Broadcaster constructed.
func newTestBroadcaster(rdb *redis.Client, h *Hub, group string) *Broadcaster {
	return &Broadcaster{
		rdb:     rdb,
		hub:     h,
		logger:  zap.NewNop(),
		group:   group,
		deduper: newEventDeduper(dedupWindow),
	}
}

func waitForFrame(t *testing.T, ch chan []byte, timeout time.Duration) []byte {
	t.Helper()
	select {
	case frame := <-ch:
		return frame
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a broadcast frame")
		return nil
	}
}

// TestRunStreamConsumer_FansOutToEveryReplicaViaOwnGroup is the direct
// regression proof for GW-1's core claim: giving each replica its OWN
// consumer group means EVERY replica sees EVERY stream message, not just
// whichever one wins the old shared group's competing-consumers race.
// Two independent Broadcaster+Hub pairs (simulating two gateway replicas)
// share one Redis instance; a single XAdd must reach BOTH replicas' own
// locally-connected clients.
func TestRunStreamConsumer_FansOutToEveryReplicaViaOwnGroup(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	const env = "production"
	streamKey := StreamKey(env)

	hubA, hubB := NewHub(zap.NewNop()), NewHub(zap.NewNop())
	bA := newTestBroadcaster(rdb, hubA, ReplicaGroupName("replica-a"))
	bB := newTestBroadcaster(rdb, hubB, ReplicaGroupName("replica-b"))

	CreateConsumerGroups(context.Background(), rdb, []string{env}, bA.Group(), zap.NewNop())
	CreateConsumerGroups(context.Background(), rdb, []string{env}, bB.Group(), zap.NewNop())

	chA := hubA.Subscribe(env, "client-a")
	defer hubA.Unsubscribe(env, "client-a", chA)
	chB := hubB.Subscribe(env, "client-b")
	defer hubB.Unsubscribe(env, "client-b", chB)

	event := FlagEvent{FlagKey: "fanout-flag", Enabled: true, RolloutPct: 100, Reason: "manual", Ts: 1_700_000_001, Environment: env}
	payload, _ := json.Marshal(event)
	if _, err := rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{"payload": string(payload), "environment": env},
	}).Result(); err != nil {
		t.Fatalf("XAdd: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bA.RunStreamConsumer(ctx, env)
	go bB.RunStreamConsumer(ctx, env)

	frameA := waitForFrame(t, chA, 5*time.Second)
	frameB := waitForFrame(t, chB, 5*time.Second)

	if !strings.Contains(string(frameA), "fanout-flag") {
		t.Errorf("replica A's client did not receive the event: %s", frameA)
	}
	if !strings.Contains(string(frameB), "fanout-flag") {
		t.Errorf("replica B's client did not receive the event: %s", frameB)
	}
}

// TestRunStreamConsumer_DedupSuppressesPubSubAndStreamsDoubleDelivery is
// the direct regression proof for dedup.go: the SAME logical event
// arriving via BOTH the legacy pub/sub path (handleMessage) and this
// replica's own Streams consumer group must reach this replica's own
// client exactly ONCE, not twice — the exact scenario per-replica groups
// would otherwise turn from a narrow, single-replica bug into a universal
// one (see dedup.go's doc comment).
func TestRunStreamConsumer_DedupSuppressesPubSubAndStreamsDoubleDelivery(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	const env = "production"
	streamKey := StreamKey(env)

	h := NewHub(zap.NewNop())
	b := newTestBroadcaster(rdb, h, ReplicaGroupName("solo-replica"))
	CreateConsumerGroups(context.Background(), rdb, []string{env}, b.Group(), zap.NewNop())

	ch := h.Subscribe(env, "client")
	defer h.Unsubscribe(env, "client", ch)

	event := FlagEvent{FlagKey: "dual-delivery-flag", Enabled: true, RolloutPct: 100, Reason: "manual", Ts: 1_700_000_002, Environment: env}
	payload, _ := json.Marshal(event)

	// Streams copy.
	if _, err := rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{"payload": string(payload), "environment": env},
	}).Result(); err != nil {
		t.Fatalf("XAdd: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.RunStreamConsumer(ctx, env)

	first := waitForFrame(t, ch, 5*time.Second)
	if !strings.Contains(string(first), "dual-delivery-flag") {
		t.Fatalf("did not receive the streams-delivered frame: %s", first)
	}

	// pub/sub copy of the SAME logical event, delivered directly (bypassing
	// real Redis pub/sub plumbing, which handleMessage doesn't need to
	// exercise here — only the dedup decision does).
	b.handleMessage(&redis.Message{Channel: "stream:" + env + ":updates", Payload: string(payload)})

	assertNoFrame(t, ch, 200*time.Millisecond, "pub/sub redelivery of the same streams-delivered event")
}

// TestHandleMessage_BroadcastsOnFirstDelivery proves handleMessage's happy
// path in isolation (no streams involved at all) still works after dedup
// was added — a single pub/sub delivery must still reach the client.
func TestHandleMessage_BroadcastsOnFirstDelivery(t *testing.T) {
	h := NewHub(zap.NewNop())
	b := newTestBroadcaster(nil, h, "unused-group")

	const env = "staging"
	ch := h.Subscribe(env, "client")
	defer h.Unsubscribe(env, "client", ch)

	event := FlagEvent{FlagKey: "pubsub-only-flag", Enabled: true, RolloutPct: 50, Reason: "manual", Ts: 1_700_000_003, Environment: env}
	payload, _ := json.Marshal(event)

	b.handleMessage(&redis.Message{Channel: "stream:" + env + ":updates", Payload: string(payload)})

	frame := waitForFrame(t, ch, time.Second)
	if !strings.Contains(string(frame), "pubsub-only-flag") {
		t.Errorf("did not receive the pub/sub-delivered frame: %s", frame)
	}
}

// TestRunStreamConsumer_RelaysPrerequisitesUpdatedVerbatim is the direct
// regression proof for the prerequisites-streaming follow-up (SDK-4):
// flag-api's prerequisites.go publishes a PrerequisitesEvent to the Stream
// with the XAdd Values map's "event" field set to "prerequisites_updated"
// (a shape this test constructs by hand, matching flag-api's own real
// wire format exactly, rather than importing across the module boundary).
// Before this change, RunStreamConsumer unconditionally tried to
// json.Unmarshal every stream payload into a FlagEvent -- a
// PrerequisitesEvent payload has no "enabled"/"rollout_pct" keys, which
// Unmarshal tolerates (they zero-value), but the frame it would have
// produced would carry meaningless flag-toggle fields, not the real
// prerequisites list, and would never reach the client under the
// "prerequisites_updated" event: name at all (eventTypeFor only ever
// returns "flag_updated"/"kill_switch"). This proves the payload is now
// relayed to the client BYTE-FOR-BYTE under its own real event: name,
// with NO dedup applied (dedup is legacy-pub/sub-vs-streams only, and this
// event kind is never dual-written to pub/sub).
func TestRunStreamConsumer_RelaysPrerequisitesUpdatedVerbatim(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	const env = "production"
	streamKey := StreamKey(env)

	h := NewHub(zap.NewNop())
	b := newTestBroadcaster(rdb, h, ReplicaGroupName("prereq-replica"))
	CreateConsumerGroups(context.Background(), rdb, []string{env}, b.Group(), zap.NewNop())

	ch := h.Subscribe(env, "client")
	defer h.Unsubscribe(env, "client", ch)

	// Exactly flag-api's real PrerequisitesEvent JSON shape (services/
	// flag-api/internal/api/v1/prerequisites.go).
	payload := `{"flag_key":"child-flag","environment":"production","prerequisites":[{"id":"p1","flag_id":"f1","flag_key":"parent-flag","required_variation":"true","gate":true,"priority":0,"created_at":1700000000}],"ts":1700000000}`

	if _, err := rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{
			"kind":        "prerequisites_updated",
			"event":       "prerequisites_updated",
			"flag_key":    "child-flag",
			"environment": env,
			"payload":     payload,
		},
	}).Result(); err != nil {
		t.Fatalf("XAdd: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.RunStreamConsumer(ctx, env)

	frame := waitForFrame(t, ch, 5*time.Second)
	frameStr := string(frame)

	if !strings.Contains(frameStr, "event: prerequisites_updated\n") {
		t.Errorf("frame does not carry the real event: name: %s", frameStr)
	}
	if !strings.Contains(frameStr, payload) {
		t.Errorf("frame does not carry the payload verbatim: %s", frameStr)
	}
	// Confirms this did NOT get misrouted through the FlagEvent path: a
	// FlagEvent-shaped frame would carry "event: flag_updated" or
	// "event: kill_switch", never the real prerequisites_updated name.
	if strings.Contains(frameStr, "event: flag_updated") || strings.Contains(frameStr, "event: kill_switch") {
		t.Errorf("frame was misrouted through the FlagEvent path: %s", frameStr)
	}
}

// TestRunStreamConsumer_KillSwitchReasonCannotCollideWithPrerequisitesUpdated
// is the direct regression proof for a HIGH-severity finding from
// adversarial review of the prerequisites-streaming PR: an earlier version
// discriminated a PrerequisitesEvent purely via the XAdd Values map's
// "event" field -- but that SAME field is set to a FlagEvent's own Reason
// for every ordinary flag-toggle event, and Reason is a free-text,
// unvalidated, caller-supplied string for KillSwitch/RollbackStep/
// RecoveryStep. A caller supplying reason="prerequisites_updated" (a
// plausible string now that it's a real domain term, not just a
// hypothetical attack) would have made this consumer misroute a genuine
// kill-switch event through Hub.BroadcastRaw, delivering it to clients as
// a bogus "zero prerequisites" update instead of the real flag_updated/
// kill_switch frame. This constructs EXACTLY that colliding FlagEvent
// (via xaddEvent, which sets Values["event"] = event.Reason and never
// sets "kind" at all, matching flag-api's own real publishFlagEventToStream)
// and proves it is routed correctly.
func TestRunStreamConsumer_KillSwitchReasonCannotCollideWithPrerequisitesUpdated(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	const env = "production"
	streamKey := StreamKey(env)

	h := NewHub(zap.NewNop())
	b := newTestBroadcaster(rdb, h, ReplicaGroupName("collision-replica"))
	CreateConsumerGroups(context.Background(), rdb, []string{env}, b.Group(), zap.NewNop())

	ch := h.Subscribe(env, "client")
	defer h.Unsubscribe(env, "client", ch)

	// A real KillSwitch call whose (unvalidated, free-text) reason happens
	// to equal the prerequisites-streaming sentinel string.
	xaddEvent(t, rdb, streamKey, FlagEvent{
		FlagKey: "payments-flag", Enabled: false, RolloutPct: 0,
		Reason: "prerequisites_updated", Ts: 1_700_000_004, Environment: env,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.RunStreamConsumer(ctx, env)

	frame := waitForFrame(t, ch, 5*time.Second)
	frameStr := string(frame)

	// Must be classified as a real kill_switch (eventTypeFor: !Enabled +
	// this exact Reason string does NOT match its own kill_switch reason
	// allowlist, so this specific Reason value actually falls through to
	// "flag_updated" -- either is correct here; what must NEVER happen is
	// BroadcastRaw's verbatim relay under "event: prerequisites_updated"
	// with a payload that has no "prerequisites" key at all).
	if strings.Contains(frameStr, "event: prerequisites_updated") {
		t.Fatalf("a real FlagEvent was misrouted as prerequisites_updated: %s", frameStr)
	}
	if !strings.Contains(frameStr, "payments-flag") {
		t.Errorf("did not receive the real FlagEvent frame at all: %s", frameStr)
	}
}
