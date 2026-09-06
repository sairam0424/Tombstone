package hub

import (
	"context"
	"encoding/json"
	"os"
	"strings"
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

// TestReclaimStalePendingInGroup_DeadLetterIsGatedBehindAClaim is a
// structural regression guard for the deadLetter atomicity fix.
//
// The property the fix actually relies on — that of several concurrent
// reclaim sweeps racing the SAME over-budget PEL entry, only ONE can ever
// win the atomic claim and proceed to dead-letter it — rests on a real
// Redis guarantee (XCLAIM's MinIdle filter is atomic under concurrent
// access) that could not be reliably reproduced against this package's
// existing miniredis-based tests: miniredis v2.38.0 does NOT correctly
// enforce that guarantee. Confirmed via two throwaway diagnostics: (1)
// against miniredis with SetTime-frozen idle, two concurrent XCLAIM calls
// for the identical entry ID with the same MinIdle both succeeded in 50/50
// trials; (2) against a REAL local redis-server at comparable idle/MinIdle
// margins (100ms idle, 50ms MinIdle — the same ~2x safety margin production
// gets from reclaimIdleThreshold's 30s vs. a sub-millisecond Redis round
// trip), exactly one winner in 300/300 trials. Since the real guarantee
// isn't faithfully testable with this package's test double, this instead
// pins the CODE STRUCTURE the fix depends on: reclaimStalePendingInGroup
// must call XClaim and check whether the claim actually succeeded BEFORE
// ever calling deadLetter — not decide to dead-letter directly off the
// plain (unclaimed) XPendingExt read, which is what let two concurrent
// sweeps both dead-letter the same message before this fix.
func TestReclaimStalePendingInGroup_DeadLetterIsGatedBehindAClaim(t *testing.T) {
	src, err := os.ReadFile("dlq.go")
	if err != nil {
		t.Fatalf("read dlq.go: %v", err)
	}
	body := reclaimStalePendingInGroupBody(t, string(src))

	claimIdx := strings.Index(body, "b.rdb.XClaim(")
	if claimIdx == -1 {
		t.Fatal("reclaimStalePendingInGroup no longer calls XClaim at all")
	}
	emptyCheckIdx := strings.Index(body, "len(msgs) == 0")
	if emptyCheckIdx == -1 || emptyCheckIdx < claimIdx {
		t.Fatal("reclaimStalePendingInGroup no longer checks whether the claim actually succeeded (len(msgs) == 0) after calling XClaim")
	}
	deadLetterIdx := strings.Index(body, "b.deadLetter(")
	if deadLetterIdx == -1 {
		t.Fatal("reclaimStalePendingInGroup no longer calls deadLetter at all")
	}
	if deadLetterIdx < emptyCheckIdx {
		t.Error("reclaimStalePendingInGroup calls deadLetter BEFORE checking whether this sweep actually won the claim race — this reintroduces the double-dead-letter bug: two concurrent sweeps that both read the same over-budget PEL entry would both dead-letter it")
	}
}

// reclaimStalePendingInGroupBody returns the body of
// reclaimStalePendingInGroup's function literal by brace-matching from its
// opening "{" to the matching close, mirroring flag-api's
// ssoConfigBlock helper (cmd/main_helpers_test.go).
func reclaimStalePendingInGroupBody(t *testing.T, src string) string {
	t.Helper()
	const marker = "func (b *Broadcaster) reclaimStalePendingInGroup("
	start := strings.Index(src, marker)
	if start == -1 {
		t.Fatalf("could not find %q in dlq.go", marker)
	}
	open := strings.Index(src[start:], "{")
	if open == -1 {
		t.Fatalf("no opening brace found for reclaimStalePendingInGroup")
	}
	open += start

	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open : i+1]
			}
		}
	}
	t.Fatalf("unbalanced braces in reclaimStalePendingInGroup")
	return ""
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

// TestReclaimStalePending_PrerequisitesUpdatedIsRelayedNotMisunmarshaled is
// the direct regression proof for a HIGH-severity finding from adversarial
// review of the prerequisites-streaming PR: reprocessClaimedMessage (this
// function is what ReclaimStalePending's own-group retry branch calls) used
// to unconditionally json.Unmarshal every reclaimed payload into a
// FlagEvent, with NO discriminator check at all -- unlike RunStreamConsumer
// and BuildReplayFrames, which both check the "kind" Values-map field
// first. A prerequisites_updated payload (flag_key/environment/
// prerequisites/ts) has none of FlagEvent's real keys (enabled/rollout_pct/
// reason), so Go's json.Unmarshal would NOT error -- it silently leaves
// those missing fields at their zero values -- and this would have
// rebroadcast a bogus "flag disabled, 0% rollout" event to every connected
// client for an entry that never touched the flag's enabled/rollout state
// at all. Reproduces the exact scenario that requires reclaim in the first
// place: the message is delivered but never acked (simulating
// RunStreamConsumer crashing/stalling mid-processing), goes idle past
// reclaimIdleThreshold, and is picked up by this replica's own reclaim
// sweep.
func TestReclaimStalePending_PrerequisitesUpdatedIsRelayedNotMisunmarshaled(t *testing.T) {
	mr, rdb, b, streamKey := setupDLQTest(t, "production")
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	ch := b.hub.Subscribe("production", "client")
	defer b.hub.Unsubscribe("production", "client", ch)

	payload := `{"flag_key":"child-flag","environment":"production","prerequisites":[{"flag_key":"parent-flag","required_variation":"true","gate":true}],"ts":1700000000}`
	if _, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]interface{}{
			"kind":        "prerequisites_updated",
			"event":       "prerequisites_updated",
			"flag_key":    "child-flag",
			"environment": "production",
			"payload":     payload,
		},
	}).Result(); err != nil {
		t.Fatalf("XAdd: %v", err)
	}

	// Deliver into b's OWN group but never ack, simulating a stuck consumer
	// (the exact precondition ReclaimStalePending exists to recover from).
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
	frameStr := string(frame)

	if !strings.Contains(frameStr, "event: prerequisites_updated") {
		t.Errorf("reclaimed prerequisites_updated entry was not relayed under its real event name: %s", frameStr)
	}
	if !strings.Contains(frameStr, payload) {
		t.Errorf("reclaimed entry did not carry the real payload verbatim: %s", frameStr)
	}
	if strings.Contains(frameStr, "event: flag_updated") || strings.Contains(frameStr, "event: kill_switch") {
		t.Errorf("reclaimed prerequisites_updated entry was misrouted through the FlagEvent path: %s", frameStr)
	}
}
