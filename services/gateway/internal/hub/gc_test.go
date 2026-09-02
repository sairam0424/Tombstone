package hub

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func setupGCTest(t *testing.T) (mr *miniredis.Miniredis, rdb *redis.Client, streamKey string) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	streamKey = StreamKey("production")
	return mr, rdb, streamKey
}

// touchGroup creates group on streamKey and establishes a consumer with a
// real, observable "last seen" timestamp — matching what a healthy
// replica's continuous XReadGroup polling does against real Redis (XINFO
// CONSUMERS's "idle" documents itself as reflecting both XREADGROUP and
// XCLAIM activity there). miniredis@v2.38.0's XREADGROUP implementation
// registers the consumer but never actually stamps its lastSeen field —
// confirmed by reading stream.go directly, only XCLAIM calls setLastSeen/
// setLastSuccess — so an XREADGROUP-only touch would always report Idle as
// "never seen" (-1ms) regardless of real activity, making it impossible to
// observe from this test tool. XCLAIMing the just-delivered message back to
// the SAME consumer is the one miniredis-visible way to advance its idle
// clock, so tests use that as the liveness signal instead. This is a gap in
// the test tool, not something GCIdleGroups itself needs to special-case.
func touchGroup(t *testing.T, ctx context.Context, rdb *redis.Client, streamKey, group, consumer string) {
	t.Helper()
	if err := rdb.XGroupCreateMkStream(ctx, streamKey, group, "0").Err(); err != nil {
		t.Fatalf("XGroupCreateMkStream(%s): %v", group, err)
	}
	id, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey, Values: map[string]interface{}{"payload": "{}"},
	}).Result()
	if err != nil {
		t.Fatalf("XAdd throwaway message: %v", err)
	}
	// Block must be negative, not the zero value: go-redis only omits the
	// BLOCK argument (true non-blocking) when Block < 0 — Block: 0 sends
	// "BLOCK 0", which is Redis's own convention for "block forever."
	if err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: group, Consumer: consumer, Streams: []string{streamKey, ">"}, Count: 1, Block: -1,
	}).Err(); err != nil {
		t.Fatalf("XReadGroup: %v", err)
	}
	if err := rdb.XClaim(ctx, &redis.XClaimArgs{
		Stream: streamKey, Group: group, Consumer: consumer, MinIdle: 0, Messages: []string{id},
	}).Err(); err != nil {
		t.Fatalf("XClaim (to stamp lastSeen): %v", err)
	}
}

func groupExists(t *testing.T, ctx context.Context, rdb *redis.Client, streamKey, group string) bool {
	t.Helper()
	groups, err := rdb.XInfoGroups(ctx, streamKey).Result()
	if err != nil {
		t.Fatalf("XInfoGroups: %v", err)
	}
	for _, g := range groups {
		if g.Name == group {
			return true
		}
	}
	return false
}

// TestGCIdleGroups_LiveGroupSurvives is the direct proof that a group whose
// consumer was JUST seen (idle ~0, well under groupIdleGCThreshold) is left
// alone — this is the common case: every healthy replica's group, checked
// on every sweep.
func TestGCIdleGroups_LiveGroupSurvives(t *testing.T) {
	mr, rdb, streamKey := setupGCTest(t)
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	touchGroup(t, ctx, rdb, streamKey, "gateway-workers-alive-host", "primary")

	GCIdleGroups(ctx, rdb, streamKey, zap.NewNop())

	if !groupExists(t, ctx, rdb, streamKey, "gateway-workers-alive-host") {
		t.Error("a group with a just-seen consumer must not be destroyed")
	}
}

// TestGCIdleGroups_IdleGroupDestroyed is the direct regression proof for
// GW-1's GC mechanism: a group whose consumer has gone quiet longer than
// groupIdleGCThreshold — its owning replica crashed, was OOMKilled, or
// scaled down without running the graceful-shutdown destroy — gets
// destroyed so its PEL and bookkeeping don't linger in Redis forever.
func TestGCIdleGroups_IdleGroupDestroyed(t *testing.T) {
	mr, rdb, streamKey := setupGCTest(t)
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	touchGroup(t, ctx, rdb, streamKey, "gateway-workers-dead-host", "primary")

	// Advance miniredis's clock past the GC threshold — idle-time math is
	// driven by SetTime, matching the existing dlq_test.go convention.
	mr.SetTime(time.Now().Add(groupIdleGCThreshold + time.Second))

	GCIdleGroups(ctx, rdb, streamKey, zap.NewNop())

	if groupExists(t, ctx, rdb, streamKey, "gateway-workers-dead-host") {
		t.Error("a group whose consumer has been idle beyond groupIdleGCThreshold must be destroyed")
	}
}

// TestGCIdleGroups_OnlyDestroysTheIdleGroupAmongMultiple proves the sweep
// correctly distinguishes groups on the SAME stream — a dead replica's
// group must not take a live replica's group down with it.
func TestGCIdleGroups_OnlyDestroysTheIdleGroupAmongMultiple(t *testing.T) {
	mr, rdb, streamKey := setupGCTest(t)
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	touchGroup(t, ctx, rdb, streamKey, "gateway-workers-dead-host", "primary")

	mr.SetTime(time.Now().Add(groupIdleGCThreshold + time.Second))
	// The live group is touched AFTER advancing the clock, so its consumer's
	// idle time is freshly reset to ~0 at the new "now" — exactly what a
	// healthy replica's continuous XReadGroup polling does in production.
	touchGroup(t, ctx, rdb, streamKey, "gateway-workers-alive-host", "primary")

	GCIdleGroups(ctx, rdb, streamKey, zap.NewNop())

	if groupExists(t, ctx, rdb, streamKey, "gateway-workers-dead-host") {
		t.Error("the idle group must be destroyed")
	}
	if !groupExists(t, ctx, rdb, streamKey, "gateway-workers-alive-host") {
		t.Error("the live group must survive — it must not be destroyed just because a SIBLING group on the same stream was idle")
	}
}

// TestGCIdleGroups_GroupWithNoConsumersIsTreatedAsStale covers a group
// created (e.g. via CreateConsumerGroups at startup) but never actually
// read from — XINFO CONSUMERS returns an empty list for it, which must be
// treated as stale (destroy it), not silently skipped.
func TestGCIdleGroups_GroupWithNoConsumersIsTreatedAsStale(t *testing.T) {
	mr, rdb, streamKey := setupGCTest(t)
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	if err := rdb.XGroupCreateMkStream(ctx, streamKey, "gateway-workers-never-read", "0").Err(); err != nil {
		t.Fatalf("XGroupCreateMkStream: %v", err)
	}

	GCIdleGroups(ctx, rdb, streamKey, zap.NewNop())

	if groupExists(t, ctx, rdb, streamKey, "gateway-workers-never-read") {
		t.Error("a group with zero registered consumers must be treated as stale and destroyed")
	}
}

// TestGCIdleGroups_NoStreamIsNoop proves calling GCIdleGroups against a
// stream that doesn't exist yet doesn't panic or log spuriously.
func TestGCIdleGroups_NoStreamIsNoop(t *testing.T) {
	mr, rdb, _ := setupGCTest(t)
	defer mr.Close()
	defer rdb.Close()

	GCIdleGroups(context.Background(), rdb, StreamKey("never-created"), zap.NewNop())
}
