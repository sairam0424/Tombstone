package hub

import (
	"testing"
	"time"
)

func testEvent() FlagEvent {
	return FlagEvent{
		FlagKey:     "test-flag",
		Enabled:     true,
		RolloutPct:  100,
		Reason:      "manual",
		Ts:          1_700_000_000,
		Environment: "production",
	}
}

// TestEventDeduper_FirstClaimSucceeds proves the common case: an event
// nobody has seen before is never suppressed.
func TestEventDeduper_FirstClaimSucceeds(t *testing.T) {
	d := newEventDeduper(time.Minute)
	if !d.claim(testEvent()) {
		t.Fatal("first claim of a never-seen event must succeed")
	}
}

// TestEventDeduper_ImmediateDuplicateSuppressed is the direct regression
// proof for GW-1: the SAME logical event delivered a second time (pub/sub
// vs. streams, or a reclaim retry racing a fresh delivery) within the
// window must be suppressed.
func TestEventDeduper_ImmediateDuplicateSuppressed(t *testing.T) {
	d := newEventDeduper(time.Minute)
	event := testEvent()

	if !d.claim(event) {
		t.Fatal("first claim must succeed")
	}
	if d.claim(event) {
		t.Fatal("second claim of the identical event within the window must be suppressed")
	}
}

// TestEventDeduper_DifferentEventsDoNotCollide proves the dedup key is
// actually keyed on the event's full content, not just, say, FlagKey alone
// — two DIFFERENT real mutations of the same flag must both get through.
func TestEventDeduper_DifferentEventsDoNotCollide(t *testing.T) {
	d := newEventDeduper(time.Minute)

	a := testEvent()
	b := testEvent()
	b.Ts = a.Ts + 1
	b.Enabled = false

	if !d.claim(a) {
		t.Fatal("claiming event a must succeed")
	}
	if !d.claim(b) {
		t.Fatal("claiming a genuinely different event b must succeed even though a was just claimed")
	}
}

// TestEventDeduper_ClaimSucceedsAgainAfterWindowElapses proves suppression
// is bounded, not permanent — a window of essentially zero means any two
// sequential claim() calls (which always take some nonzero wall-clock time
// to execute) land far enough apart that both succeed.
func TestEventDeduper_ClaimSucceedsAgainAfterWindowElapses(t *testing.T) {
	d := newEventDeduper(time.Nanosecond)
	event := testEvent()

	if !d.claim(event) {
		t.Fatal("first claim must succeed")
	}
	time.Sleep(time.Millisecond)
	if !d.claim(event) {
		t.Fatal("claim after the window elapsed must succeed again, not stay suppressed forever")
	}
}

// TestEventDeduper_EvictsExpiredEntriesPastSizeThreshold proves the map
// doesn't grow unbounded under sustained traffic — once past the internal
// size threshold, claim() opportunistically evicts anything older than the
// window.
func TestEventDeduper_EvictsExpiredEntriesPastSizeThreshold(t *testing.T) {
	d := newEventDeduper(time.Nanosecond) // everything expires almost immediately

	for i := 0; i < 1500; i++ {
		e := testEvent()
		e.Ts = int64(i)
		d.claim(e)
		time.Sleep(time.Microsecond) // ensure each entry is actually past the window before the next claim
	}

	d.mu.Lock()
	size := len(d.seen)
	d.mu.Unlock()

	if size > 1000 {
		t.Errorf("deduper map grew to %d entries, want <= 1000 after opportunistic eviction", size)
	}
}
