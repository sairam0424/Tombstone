package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestCompareStreamIDs_NumericNotLexical is the direct regression proof for
// GW-2's documented footgun: a plain string compare of Redis Stream IDs
// breaks the moment the seq part has a different digit width, even though
// the same ms part makes it look like a same-width comparison at a glance.
func TestCompareStreamIDs_NumericNotLexical(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"5-9", "5-10", -1}, // lexically "5-10" < "5-9"; numerically 9 < 10
		{"5-10", "5-9", 1},
		{"5-5", "5-5", 0},
		{"100-0", "99-0", 1}, // different ms width, numerically 100 > 99
		{"99-0", "100-0", -1},
		{"1735599999999-0", "1735599999999-1", -1},
	}
	for _, c := range cases {
		got := compareStreamIDs(c.a, c.b)
		if got != c.want {
			t.Errorf("compareStreamIDs(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func xaddEvent(t *testing.T, rdb *redis.Client, streamKey string, event FlagEvent) string {
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

// TestReplaySince_ReturnsOnlyMessagesAfterLastID proves the O(delta)
// catch-up contract: given 3 published events and the ID of the 1st,
// ReplaySince must return exactly events 2 and 3, not a full re-scan.
func TestReplaySince_ReturnsOnlyMessagesAfterLastID(t *testing.T) {
	rdb := newTestRedis(t)
	streamKey := StreamKey("production")
	ctx := context.Background()

	id1 := xaddEvent(t, rdb, streamKey, FlagEvent{FlagKey: "flag-1", Environment: "production"})
	id2 := xaddEvent(t, rdb, streamKey, FlagEvent{FlagKey: "flag-2", Environment: "production"})
	id3 := xaddEvent(t, rdb, streamKey, FlagEvent{FlagKey: "flag-3", Environment: "production"})

	msgs, ok, err := ReplaySince(ctx, rdb, streamKey, id1)
	if err != nil {
		t.Fatalf("ReplaySince: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true (lastID is not trimmed), got false")
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 replayed messages, got %d", len(msgs))
	}
	if msgs[0].ID != id2 || msgs[1].ID != id3 {
		t.Errorf("expected [%s, %s], got [%s, %s]", id2, id3, msgs[0].ID, msgs[1].ID)
	}
}

// TestReplaySince_NoMessagesAfterLastIDReturnsEmptyOkTrue covers the
// "genuinely caught up" case: lastID is the newest entry, nothing was
// missed, and ReplaySince must say so (ok=true, zero messages) rather than
// mistaking it for a trimmed-past-retention gap.
func TestReplaySince_NoMessagesAfterLastIDReturnsEmptyOkTrue(t *testing.T) {
	rdb := newTestRedis(t)
	streamKey := StreamKey("production")
	ctx := context.Background()

	id1 := xaddEvent(t, rdb, streamKey, FlagEvent{FlagKey: "flag-1", Environment: "production"})

	msgs, ok, err := ReplaySince(ctx, rdb, streamKey, id1)
	if err != nil {
		t.Fatalf("ReplaySince: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for a fully-caught-up client, got false")
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 replayed messages, got %d", len(msgs))
	}
}

// TestReplaySince_EmptyStreamReturnsOkFalse is the direct regression proof
// for a bug found by adversarial review of PR #211: a completely empty (or
// nonexistent) stream key used to fall through the trimmed-past-retention
// guard entirely (its "len(oldest) > 0 &&" condition was false, not true,
// for zero entries) and return ok=true with zero messages -- indistinguishable
// from "genuinely caught up" even though a Redis restart/flush/eviction
// could just as easily mean every event since lastID was lost. An empty
// stream must report ok=false, the same as any other retention-boundary
// violation, so the caller falls back to a snapshot instead of silently
// claiming nothing was missed.
func TestReplaySince_EmptyStreamReturnsOkFalse(t *testing.T) {
	rdb := newTestRedis(t)
	streamKey := StreamKey("production") // never XADDed to -- genuinely empty/nonexistent
	ctx := context.Background()

	_, ok, err := ReplaySince(ctx, rdb, streamKey, "5-0")
	if err != nil {
		t.Fatalf("ReplaySince: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for a completely empty stream, got true")
	}
}

// TestReplaySince_LastIDOlderThanRetentionReturnsOkFalse is the direct
// regression proof for the trimmed-past-retention branch: a lastID from
// before the stream's current oldest entry must report ok=false so the
// caller falls back to a snapshot instead of silently under-replaying.
func TestReplaySince_LastIDOlderThanRetentionReturnsOkFalse(t *testing.T) {
	rdb := newTestRedis(t)
	streamKey := StreamKey("production")
	ctx := context.Background()

	// A lastID from "before recorded history" -- older than anything ever
	// XADDed to this (fresh, empty) stream once we add one real entry.
	staleLastID := "1-0"
	xaddEvent(t, rdb, streamKey, FlagEvent{FlagKey: "flag-1", Environment: "production"})

	_, ok, err := ReplaySince(ctx, rdb, streamKey, staleLastID)
	if err != nil {
		t.Fatalf("ReplaySince: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for a lastID predating the stream's oldest retained entry, got true")
	}
}

// TestBuildReplayFrames_CarriesRealIDAndCorrectEventType proves replayed
// frames are indistinguishable from live ones except for now carrying a
// real, XRANGE-replayable id: line, and that kill_switch events keep their
// derived event type through the replay path too.
func TestBuildReplayFrames_CarriesRealIDAndCorrectEventType(t *testing.T) {
	rdb := newTestRedis(t)
	streamKey := StreamKey("production")
	ctx := context.Background()

	// A sentinel entry establishes a real, in-retention lastID -- using an
	// arbitrary "beginning of time" ID like "0-0" here would instead hit
	// ReplaySince's trimmed-past-retention branch (it predates the stream's
	// only entry), which is exactly what
	// TestReplaySince_LastIDOlderThanRetentionReturnsOkFalse already covers.
	sentinelID := xaddEvent(t, rdb, streamKey, FlagEvent{FlagKey: "sentinel", Environment: "production"})
	xaddEvent(t, rdb, streamKey, FlagEvent{
		FlagKey: "flag-1", Environment: "production",
		Enabled: false, Reason: "manual_kill_switch",
	})

	msgs, ok, err := ReplaySince(ctx, rdb, streamKey, sentinelID)
	if err != nil || !ok {
		t.Fatalf("ReplaySince: ok=%v err=%v", ok, err)
	}
	frames := BuildReplayFrames(msgs)
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	frame := string(frames[0])
	wantIDLine := "id: " + msgs[0].ID + "\n"
	if frame[:len(wantIDLine)] != wantIDLine {
		t.Errorf("expected frame to start with %q, got %q", wantIDLine, frame)
	}
	if !strings.Contains(frame, "event: kill_switch\n") {
		t.Errorf("expected kill_switch event type, got frame: %s", frame)
	}
	if !strings.Contains(frame, `"flag-1"`) {
		t.Errorf("expected flag_key in replayed frame, got: %s", frame)
	}
}
