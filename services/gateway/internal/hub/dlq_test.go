package hub

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// setupDLQTest starts a miniredis instance, a Broadcaster wired to it
// (whose own per-replica group, per GW-1, is what gets seeded on
// streamKey), returning everything a test needs plus a teardown func.
func setupDLQTest(t *testing.T, environment string) (mr *miniredis.Miniredis, rdb *redis.Client, b *Broadcaster, streamKey string) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}

	rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	h := NewHub(zap.NewNop())
	b = NewBroadcaster(rdb, h, zap.NewNop())
	streamKey = StreamKey(environment)

	if err := rdb.XGroupCreateMkStream(context.Background(), streamKey, b.Group(), "0").Err(); err != nil {
		t.Fatalf("XGroupCreateMkStream: %v", err)
	}

	return mr, rdb, b, streamKey
}

// deliverPoisonMessage publishes a malformed (non-JSON payload) message onto
// streamKey and reads it once via XREADGROUP (against group) as
// consumerName, simulating what RunStreamConsumer's real read loop does:
// read it, fail to unmarshal, and (per the fix under test) leave it
// un-acked in the PEL.
func deliverPoisonMessage(t *testing.T, ctx context.Context, rdb *redis.Client, streamKey, group, consumerName string) string {
	t.Helper()

	id, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{
			"event":       "flag_updated",
			"flag_key":    "poison-flag",
			"environment": "development",
			"payload":     "{not valid json",
		},
	}).Result()
	if err != nil {
		t.Fatalf("XAdd poison message: %v", err)
	}

	msgs, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumerName,
		Streams:  []string{streamKey, ">"},
		Count:    10,
	}).Result()
	if err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}
	if len(msgs) != 1 || len(msgs[0].Messages) != 1 {
		t.Fatalf("expected exactly 1 stream with 1 message, got %+v", msgs)
	}

	// Mirror RunStreamConsumer's fixed behaviour: on unmarshal failure, do
	// NOT ack. We don't call the real unmarshal here since the point of
	// this helper is just to seed the PEL with a delivered-but-unacked
	// entry; broadcaster_test.go-equivalent coverage of the unmarshal path
	// itself lives in broadcaster.go's own review, not here.
	return id
}

// TestReclaimStalePending_RetriesUnderAttemptBudget verifies that a message
// idle beyond reclaimIdleThreshold, with delivery count still under
// maxDeliveryAttempts, gets XCLAIMed (retried) rather than dead-lettered.
func TestReclaimStalePending_RetriesUnderAttemptBudget(t *testing.T) {
	mr, rdb, b, streamKey := setupDLQTest(t, "development")
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	id := deliverPoisonMessage(t, ctx, rdb, streamKey, b.Group(), "gateway-test-consumer")

	// Advance miniredis's clock past the idle threshold so XPENDING considers
	// this entry stale enough to reclaim. XPENDING/XCLAIM idle-time math is
	// driven by miniredis's SetTime, NOT FastForward (which only decrements
	// key TTLs) — see cmd_stream.go's use of m.effectiveNow().
	mr.SetTime(time.Now().Add(reclaimIdleThreshold + time.Second))

	if err := b.ReclaimStalePending(ctx, streamKey); err != nil {
		t.Fatalf("ReclaimStalePending: %v", err)
	}

	// One reclaim cycle with delivery count starting at 1 must NOT dead-letter
	// yet (maxDeliveryAttempts == 3) — the message should still be pending,
	// now owned by the reclaim consumer.
	dlqKey := DLQStreamKey(streamKey)
	dlqLen, err := rdb.XLen(ctx, dlqKey).Result()
	if err != nil && err != redis.Nil {
		t.Fatalf("XLen dlq: %v", err)
	}
	if dlqLen != 0 {
		t.Errorf("expected 0 entries in dlq after 1 reclaim cycle, got %d", dlqLen)
	}

	pending, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: streamKey, Group: b.Group(), Start: "-", End: "+", Count: 10,
	}).Result()
	if err != nil {
		t.Fatalf("XPendingExt: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("expected message %s still pending after 1 reclaim cycle, got %+v", id, pending)
	}
	if pending[0].RetryCount < 2 {
		t.Errorf("expected delivery count >= 2 after XCLAIM, got %d", pending[0].RetryCount)
	}
}

// TestReclaimStalePending_DeadLettersAfterMaxAttempts verifies that after
// maxDeliveryAttempts reclaim cycles, the poison message appears in the
// "<stream>:dlq" stream and is gone from the primary stream's PEL.
func TestReclaimStalePending_DeadLettersAfterMaxAttempts(t *testing.T) {
	mr, rdb, b, streamKey := setupDLQTest(t, "development")
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	id := deliverPoisonMessage(t, ctx, rdb, streamKey, b.Group(), "gateway-test-consumer")

	// Run reclaim cycles until the message is dead-lettered. Delivery count
	// starts at 1 (the initial XReadGroup delivery); each XCLAIM increments
	// it by one. Once it reaches maxDeliveryAttempts, the NEXT sweep
	// dead-letters instead of claiming again. Each cycle pushes miniredis's
	// clock further forward so every claim is idle long enough to qualify.
	now := time.Now()
	for i := 0; i < maxDeliveryAttempts+1; i++ {
		now = now.Add(reclaimIdleThreshold + time.Second)
		mr.SetTime(now)
		if err := b.ReclaimStalePending(ctx, streamKey); err != nil {
			t.Fatalf("ReclaimStalePending (cycle %d): %v", i, err)
		}
	}

	dlqKey := DLQStreamKey(streamKey)
	dlqMsgs, err := rdb.XRange(ctx, dlqKey, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange dlq: %v", err)
	}
	if len(dlqMsgs) != 1 {
		t.Fatalf("expected exactly 1 entry in dlq after %d reclaim cycles, got %d", maxDeliveryAttempts+1, len(dlqMsgs))
	}
	if dlqMsgs[0].Values["flag_key"] != "poison-flag" {
		t.Errorf("dlq entry missing expected flag_key field, got %+v", dlqMsgs[0].Values)
	}

	// The original message must be gone from the primary stream's PEL.
	pending, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: streamKey, Group: b.Group(), Start: "-", End: "+", Count: 10,
	}).Result()
	if err != nil {
		t.Fatalf("XPendingExt: %v", err)
	}
	for _, p := range pending {
		if p.ID == id {
			t.Errorf("expected message %s removed from PEL after dead-lettering, still present: %+v", id, p)
		}
	}
}

// TestReclaimStalePending_NoPendingIsNoop verifies that calling
// ReclaimStalePending against a stream with no stale pending entries is a
// clean no-op (no error, no dlq writes).
func TestReclaimStalePending_NoPendingIsNoop(t *testing.T) {
	mr, rdb, b, streamKey := setupDLQTest(t, "staging")
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	if err := b.ReclaimStalePending(ctx, streamKey); err != nil {
		t.Fatalf("ReclaimStalePending on empty PEL: %v", err)
	}

	dlqKey := DLQStreamKey(streamKey)
	dlqLen, err := rdb.XLen(ctx, dlqKey).Result()
	if err != nil && err != redis.Nil {
		t.Fatalf("XLen dlq: %v", err)
	}
	if dlqLen != 0 {
		t.Errorf("expected no dlq entries when PEL was empty, got %d", dlqLen)
	}
}

// TestReclaimStalePending_CrossGroupReclaimDoesNotRebroadcast is the direct
// regression proof for the dedup/reclaim-window fix: reclaiming a stuck
// entry from a DIFFERENT (dead) replica's group must drain it (ack) but must
// NEVER broadcast it. Under GW-1's per-replica fan-out, this replica's own
// group has already independently delivered the identical event via the
// normal RunStreamConsumer path, so redelivering a dead replica's abandoned
// copy would be a pure duplicate to this replica's own already-served
// clients, not a recovery of anything — see reprocessClaimedMessage's doc
// comment.
func TestReclaimStalePending_CrossGroupReclaimDoesNotRebroadcast(t *testing.T) {
	mr, rdb, b, streamKey := setupDLQTest(t, "production")
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	ch := b.hub.Subscribe("production", "client")
	defer b.hub.Unsubscribe("production", "client", ch)

	// A second, foreign group on the SAME stream — standing in for a dead
	// replica that read this message into its own PEL but never acked or
	// broadcast it before crashing.
	deadGroup := ReplicaGroupName("dead-replica")
	if err := rdb.XGroupCreateMkStream(ctx, streamKey, deadGroup, "0").Err(); err != nil {
		t.Fatalf("XGroupCreateMkStream(dead): %v", err)
	}

	event := FlagEvent{FlagKey: "cross-group-flag", Enabled: true, RolloutPct: 100, Reason: "manual", Ts: 1_700_000_020, Environment: "production"}
	payload, _ := json.Marshal(event)
	if _, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{"payload": string(payload), "environment": "production"},
	}).Result(); err != nil {
		t.Fatalf("XAdd: %v", err)
	}

	// The dead group reads (and never acks) the message — simulating its
	// consumer crashing mid-processing, before it could broadcast or ack.
	// b's OWN group never reads this message in this test — standing in for
	// the (near-certain, in production) case where b's own group's
	// RunStreamConsumer already independently delivered and acked its own
	// copy moments after publish, well before this reclaim sweep runs.
	if _, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: deadGroup, Consumer: replicaConsumerName, Streams: []string{streamKey, ">"}, Count: 1,
	}).Result(); err != nil {
		t.Fatalf("XReadGroup(dead): %v", err)
	}

	mr.SetTime(time.Now().Add(reclaimIdleThreshold + time.Second))
	if err := b.ReclaimStalePending(ctx, streamKey); err != nil {
		t.Fatalf("ReclaimStalePending: %v", err)
	}

	assertNoFrame(t, ch, 200*time.Millisecond, "cross-group reclaim of a dead replica's abandoned entry")

	pending, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: streamKey, Group: deadGroup, Start: "-", End: "+", Count: 10,
	}).Result()
	if err != nil {
		t.Fatalf("XPendingExt(dead): %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected the dead group's PEL entry to be drained by the reclaim sweep, got %+v", pending)
	}
}

// TestReclaimStalePending_OwnGroupReclaimStillRebroadcasts proves the
// complement of the cross-group test above: reclaiming a stuck entry from
// THIS replica's OWN group (e.g. recovering from a transient ack failure,
// not a crash) still goes through the normal broadcast+dedup path — the
// cross-group fix must only suppress broadcast for FOREIGN groups, never
// for a replica's own.
func TestReclaimStalePending_OwnGroupReclaimStillRebroadcasts(t *testing.T) {
	mr, rdb, b, streamKey := setupDLQTest(t, "production")
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	ch := b.hub.Subscribe("production", "client")
	defer b.hub.Unsubscribe("production", "client", ch)

	event := FlagEvent{FlagKey: "self-reclaim-flag", Enabled: true, RolloutPct: 100, Reason: "manual", Ts: 1_700_000_021, Environment: "production"}
	payload, _ := json.Marshal(event)
	if _, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{"payload": string(payload), "environment": "production"},
	}).Result(); err != nil {
		t.Fatalf("XAdd: %v", err)
	}

	// Deliver into b's OWN group but never ack, simulating a stuck consumer.
	if _, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: b.Group(), Consumer: replicaConsumerName, Streams: []string{streamKey, ">"}, Count: 1,
	}).Result(); err != nil {
		t.Fatalf("XReadGroup(own): %v", err)
	}

	mr.SetTime(time.Now().Add(reclaimIdleThreshold + time.Second))
	if err := b.ReclaimStalePending(ctx, streamKey); err != nil {
		t.Fatalf("ReclaimStalePending: %v", err)
	}

	frame := waitForFrame(t, ch, 5*time.Second)
	if !strings.Contains(string(frame), "self-reclaim-flag") {
		t.Errorf("own-group reclaim must still broadcast: %s", frame)
	}
}

// TestReclaimStalePending_ConcurrentSweepsDoNotDoubleDeadLetter is the
// direct regression proof for the deadLetter atomicity fix: two concurrent
// reclaim sweeps (simulating two live replicas' independently-ticking 15s
// reclaim loops) racing the SAME over-budget PEL entry must produce exactly
// ONE dead-letter entry, not two.
func TestReclaimStalePending_ConcurrentSweepsDoNotDoubleDeadLetter(t *testing.T) {
	mr, rdb, b, streamKey := setupDLQTest(t, "development")
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	id := deliverPoisonMessage(t, ctx, rdb, streamKey, b.Group(), "gateway-test-consumer")

	// Drive the delivery count to maxDeliveryAttempts via successive claims,
	// exactly as TestReclaimStalePending_DeadLettersAfterMaxAttempts does,
	// but stop one cycle short so this test's two concurrent sweeps are the
	// ones that actually trigger the dead-letter branch.
	now := time.Now()
	for i := 0; i < maxDeliveryAttempts; i++ {
		now = now.Add(reclaimIdleThreshold + time.Second)
		mr.SetTime(now)
		if err := b.ReclaimStalePending(ctx, streamKey); err != nil {
			t.Fatalf("ReclaimStalePending (warmup cycle %d): %v", i, err)
		}
	}
	now = now.Add(reclaimIdleThreshold + time.Second)
	mr.SetTime(now)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.ReclaimStalePending(ctx, streamKey)
		}()
	}
	wg.Wait()

	dlqKey := DLQStreamKey(streamKey)
	dlqMsgs, err := rdb.XRange(ctx, dlqKey, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange dlq: %v", err)
	}
	if len(dlqMsgs) != 1 {
		t.Fatalf("expected exactly 1 dlq entry after 2 concurrent sweeps raced the same over-budget message %s, got %d", id, len(dlqMsgs))
	}
}

// TestDLQStreamKey_MatchesConvention pins the "<stream>:dlq" naming
// convention that MUST stay byte-identical with the Python intelligence
// service's derivation (tombstone:stream:{environment} + ":dlq").
func TestDLQStreamKey_MatchesConvention(t *testing.T) {
	got := DLQStreamKey(StreamKey("production"))
	want := "tombstone:stream:production:dlq"
	if got != want {
		t.Errorf("DLQStreamKey(StreamKey(%q)) = %q, want %q", "production", got, want)
	}
}
