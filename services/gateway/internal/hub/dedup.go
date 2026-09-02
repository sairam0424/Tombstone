package hub

import (
	"sync"
	"time"
)

// dedupWindow bounds how long a delivered event is remembered for
// suppressing a same-event redelivery. Sized comfortably larger than the
// realistic skew between the two transports (pub/sub is push-based and
// effectively instant; streams adds up to streamBlockDur's 1s of polling
// latency plus any backpressure), while staying short enough that a
// genuinely NEW mutation landing on the exact same whole-second Ts as a
// recent one — the one case this can't tell apart from a true duplicate,
// since Ts is second-precision — is only misclassified for a few seconds,
// not indefinitely.
const dedupWindow = 5 * time.Second

// eventDeduper suppresses delivering the SAME logical flag event to a
// replica's own SSE clients twice within dedupWindow.
//
// GW-1: flags.go's producer dual-writes every mutation to both the legacy
// pub/sub channel and a Redis Stream (temporary, until v2.1's planned
// removal) — Broadcaster.handleMessage (pub/sub) and RunStreamConsumer
// (streams) each independently call Hub.Broadcast for what is logically
// the SAME event. Before GW-1, only the one replica that happened to win
// the shared consumer group's competing-consumers race got both copies,
// double-broadcasting to its own clients — a live, if narrow, bug. Once
// every replica has its OWN consumer group (ReplicaGroupName) and Streams
// therefore fans out to ALL of them, EVERY replica would get both copies
// without this: the fix to the fan-out gap would turn a narrow bug into a
// universal one. This is per-process, in-memory, and deliberately not
// Redis-shared — deduplication is about what this specific replica has
// already pushed to its own locally-connected clients, the same scope
// Hub's client registry already has.
type eventDeduper struct {
	mu     sync.Mutex
	seen   map[FlagEvent]time.Time
	window time.Duration
}

func newEventDeduper(window time.Duration) *eventDeduper {
	return &eventDeduper{seen: make(map[FlagEvent]time.Time), window: window}
}

// claim reports whether this is the first delivery of event within window
// (true: go ahead and broadcast it) or a redelivery to suppress (false).
// Opportunistically evicts expired entries so the map doesn't grow
// unbounded under sustained traffic.
func (d *eventDeduper) claim(event FlagEvent) bool {
	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()

	if seenAt, ok := d.seen[event]; ok && now.Sub(seenAt) < d.window {
		return false
	}
	d.seen[event] = now

	if len(d.seen) > 1000 {
		for k, t := range d.seen {
			if now.Sub(t) >= d.window {
				delete(d.seen, k)
			}
		}
	}
	return true
}
