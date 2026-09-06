package hub

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// lagEvent is the pre-serialized SSE lag warning frame written to a client channel
// when its buffer is full and we are about to drop the real event.
var lagEvent = []byte("event: lag\ndata: {\"lag_ms\":0}\n\n")

// FlagEvent is the payload sent to SDK clients over SSE.
type FlagEvent struct {
	FlagKey     string `json:"flag_key"`
	Enabled     bool   `json:"enabled"`
	RolloutPct  int    `json:"rollout_pct"`
	Reason      string `json:"reason"`
	Ts          int64  `json:"ts"`
	Environment string `json:"environment"`
}

// EnvironmentBroadcaster manages all SSE clients for a single environment.
// It uses a sync.Map for lock-free client registration and atomic counters
// for metrics — safe for concurrent reads from many goroutines.
type EnvironmentBroadcaster struct {
	clients      sync.Map     // clientID (string) → chan []byte
	count        atomic.Int64 // live connection count
	totalSent    atomic.Int64 // cumulative events delivered
	totalDropped atomic.Int64 // cumulative events dropped (backpressure)
}

// Add registers a new client channel under the given ID.
func (eb *EnvironmentBroadcaster) Add(id string, ch chan []byte) {
	eb.clients.Store(id, ch)
	eb.count.Add(1)
}

// Remove deregisters the client channel. The caller is responsible for closing ch.
func (eb *EnvironmentBroadcaster) Remove(id string) {
	eb.clients.Delete(id)
	eb.count.Add(-1)
}

// Broadcast fans out a pre-serialized SSE frame to every registered client.
// If a client channel is full the lag warning is sent first (non-blocking),
// then the real event is dropped. Returns (sent, dropped) counts for this call.
//
// Sends go through trySend, not a bare select, because sync.Map.Range gives
// no snapshot isolation against a concurrent Delete: Hub.Unsubscribe can
// Remove+close(ch) for a client between Range handing this goroutine ch and
// this goroutine's send running on it. A send on a closed channel always
// panics regardless of the surrounding select/default, and nothing recovers
// panics in the goroutines that call Broadcast (Broadcaster.Run,
// RunStreamConsumer) -- an unrecovered panic there crashes the entire
// gateway process, every environment's clients, not just this one client's
// connection. Found by adversarial review of PR #211 as a pre-existing bug
// (this Range/close race predates GW-2 and this diff does not widen it --
// ReplayOrSnapshot's replay frames are written directly to the
// ResponseWriter, never through ch), fixed here because it was directly
// surfaced by that review of this exact function.
func (eb *EnvironmentBroadcaster) Broadcast(payload []byte) (sent, dropped int) {
	eb.clients.Range(func(_, v any) bool {
		ch := v.(chan []byte)
		if trySend(ch, payload) {
			sent++
			return true
		}
		// Client is too slow (buffer full) or its channel was concurrently
		// closed by Unsubscribe -- either way the real event was not
		// delivered. Attempt the lag warning (non-blocking; also panic-safe
		// via trySend), then count the drop regardless of which case it was.
		trySend(ch, lagEvent)
		dropped++
		return true
	})
	eb.totalSent.Add(int64(sent))
	eb.totalDropped.Add(int64(dropped))
	return sent, dropped
}

// trySend attempts a non-blocking send on ch, recovering if ch was closed
// concurrently by another goroutine (see Broadcast's doc comment above).
// Returns false for both "channel full" and "channel closed" -- callers
// only need to know the payload was not delivered, not which of the two
// prevented it.
func trySend(ch chan []byte, payload []byte) (sentOK bool) {
	defer func() {
		if recover() != nil {
			sentOK = false
		}
	}()
	select {
	case ch <- payload:
		return true
	default:
		return false
	}
}

// Stats returns a snapshot of the broadcaster's current metrics.
func (eb *EnvironmentBroadcaster) Stats() EnvStats {
	return EnvStats{
		ActiveConnections: int(eb.count.Load()),
		TotalEventsSent:   eb.totalSent.Load(),
		TotalDropped:      eb.totalDropped.Load(),
	}
}

// EnvStats is a point-in-time metrics snapshot for one environment.
type EnvStats struct {
	ActiveConnections int   `json:"active_connections"`
	TotalEventsSent   int64 `json:"total_events_sent"`
	TotalDropped      int64 `json:"total_dropped"`
}

// Hub manages all active SSE connections keyed by environment.
// One EnvironmentBroadcaster exists per environment; it is created lazily
// on first Subscribe and lives for the lifetime of the Hub.
type Hub struct {
	envs      sync.Map // environment (string) → *EnvironmentBroadcaster
	snapshots sync.Map // environment (string) → []byte (last-known full snapshot JSON, set by the reconciler)
	logger    *zap.Logger
	// rdb is used only for GW-2 catch-up-on-reconnect (ReplayOrSnapshot's
	// XRANGE/XREVRANGE calls). nil is safe -- ReplayOrSnapshot no-ops when
	// unset, which is what relay_proxy.go's localHub wants: a relay has no
	// Redis Streams of its own to replay from, it only forwards frames it
	// already received from an upstream region.
	//
	// Deliberately a plain field, not a sync.Map/atomic.Pointer like envs/
	// snapshots above -- see SetRedis's precondition. Adversarial review of
	// PR #211 flagged this as undocumented; the precondition below is the
	// fix, not a synchronization primitive, because every current call site
	// already satisfies it via Go's happens-before guarantee (SetRedis
	// always completes, in program order on the spawning goroutine, before
	// any `go` statement that could read rdb) -- adding a lock here would
	// guard against a scenario that does not exist today at the cost of
	// contending every ReplayOrSnapshot call against it.
	rdb *redis.Client
}

func NewHub(logger *zap.Logger) *Hub {
	return &Hub{logger: logger}
}

// SetRedis wires the Redis client GW-2's ReplayOrSnapshot uses to serve
// reconnecting clients. Kept as a setter rather than a NewHub parameter so
// every existing NewHub(logger) call site (tests, relay_proxy's localHub)
// keeps compiling unchanged.
//
// PRECONDITION (unenforced -- caller's responsibility): call this at most
// once per Hub, and only before starting any goroutine that could call
// ReplayOrSnapshot on it (broadcaster.Run, RunStreamConsumer, reconciler.Run,
// the HTTP server accepting SSE connections). h.rdb has no lock or atomic
// backing it -- a second SetRedis call on a live Hub, or sharing one Hub
// across goroutines via t.Parallel() with SetRedis called from one of them,
// is a genuine unguarded data race, not merely bad practice. Every call site
// in this codebase today (cmd/main.go, and every test) satisfies this by
// construction; if you are adding one that does not, add real
// synchronization here first.
func (h *Hub) SetRedis(rdb *redis.Client) {
	h.rdb = rdb
}

// getOrCreateEnv returns the EnvironmentBroadcaster for the given environment,
// creating it atomically if it does not exist yet.
func (h *Hub) getOrCreateEnv(environment string) *EnvironmentBroadcaster {
	eb := &EnvironmentBroadcaster{}
	actual, _ := h.envs.LoadOrStore(environment, eb)
	return actual.(*EnvironmentBroadcaster)
}

// Subscribe registers a new SSE client for an environment.
// Returns a buffered channel (size 64) that delivers pre-serialized SSE frames.
// The caller MUST call Unsubscribe when the connection closes.
func (h *Hub) Subscribe(environment, clientID string) chan []byte {
	ch := make(chan []byte, 64) // 64-slot buffer — proven value, never change
	eb := h.getOrCreateEnv(environment)
	eb.Add(clientID, ch)
	h.logger.Debug("SSE client subscribed",
		zap.String("env", environment),
		zap.String("client", clientID))
	return ch
}

// Unsubscribe removes the client channel from the broadcaster and closes it,
// causing the SSE handler's range-over-channel to terminate.
func (h *Hub) Unsubscribe(environment, clientID string, ch chan []byte) {
	if v, ok := h.envs.Load(environment); ok {
		eb := v.(*EnvironmentBroadcaster)
		eb.Remove(clientID)
	}
	close(ch)
	h.logger.Debug("SSE client unsubscribed",
		zap.String("env", environment),
		zap.String("client", clientID))
}

// Broadcast serializes a FlagEvent into a pre-formed SSE frame and fans it
// out to all clients in the given environment via EnvironmentBroadcaster.
// Backpressure: slow clients receive a lag warning, then the event is dropped.
//
// streamMsgID is the real Redis Stream entry ID this event was read at (GW-2),
// or "" when the caller has no such ID (the legacy pub/sub path, the
// reconciler's synthetic drift events, and relay-forwarded events all pass
// "" today). It becomes the SSE frame's id: line, which is what lets a
// reconnecting client's Last-Event-ID header drive ReplayOrSnapshot's XRANGE
// catch-up below. It is deliberately NOT a field on FlagEvent: eventDeduper
// (dedup.go) keys claim() on the full FlagEvent value, and this same logical
// event arrives via both pub/sub (no ID) and streams (real ID) within the
// same dedup window -- making the ID part of FlagEvent would make those two
// deliveries hash as different map keys and defeat dedup entirely.
func (h *Hub) Broadcast(environment string, event FlagEvent, streamMsgID string) {
	v, ok := h.envs.Load(environment)
	if !ok {
		return // no clients subscribed to this environment
	}
	eb := v.(*EnvironmentBroadcaster)

	// Pre-serialize once; all clients receive the same byte slice (read-only).
	// JSON marshaling is done inline to avoid an extra import cycle.
	payload := sseFrame(eventTypeFor(event), event, streamMsgID)

	sent, dropped := eb.Broadcast(payload)
	if dropped > 0 {
		h.logger.Warn("backpressure: events dropped for slow clients",
			zap.String("env", environment),
			zap.String("flag", event.FlagKey),
			zap.Int("dropped", dropped),
			zap.Int("sent", sent))
	}
}

// BroadcastRaw fans out a pre-serialized JSON payload verbatim under the
// given SSE event: name, for event kinds this Hub does not need to
// typed-unmarshal or dedupe (currently only prerequisites_updated).
// Deliberately does NOT run eventDeduper.claim -- dedup exists solely to
// suppress a second delivery of the SAME logical event via the legacy
// pub/sub transport, and prerequisites_updated is never dual-written there
// (see RunStreamConsumer's own "event" Values-map discriminator check).
func (h *Hub) BroadcastRaw(environment, eventType string, payload []byte, streamMsgID string) {
	v, ok := h.envs.Load(environment)
	if !ok {
		return // no clients subscribed to this environment
	}
	eb := v.(*EnvironmentBroadcaster)

	frame := rawFrame(eventType, payload, streamMsgID)

	sent, dropped := eb.Broadcast(frame)
	if dropped > 0 {
		h.logger.Warn("backpressure: events dropped for slow clients",
			zap.String("env", environment),
			zap.String("event_type", eventType),
			zap.Int("dropped", dropped),
			zap.Int("sent", sent))
	}
}

// AllStats returns per-environment metrics for the /gateway/metrics endpoint.
func (h *Hub) AllStats() map[string]EnvStats {
	result := make(map[string]EnvStats)
	h.envs.Range(func(k, v any) bool {
		env := k.(string)
		eb := v.(*EnvironmentBroadcaster)
		result[env] = eb.Stats()
		return true
	})
	return result
}

// ConnectionCount returns the number of active connections for an environment.
// Kept for backward compatibility with the health endpoint in main.go.
func (h *Hub) ConnectionCount(environment string) int {
	if v, ok := h.envs.Load(environment); ok {
		return int(v.(*EnvironmentBroadcaster).count.Load())
	}
	return 0
}

// AllConnectionCounts returns environment → connection count.
// Kept for backward compatibility with the health endpoint in main.go.
func (h *Hub) AllConnectionCounts() map[string]int {
	result := make(map[string]int)
	h.envs.Range(func(k, v any) bool {
		env := k.(string)
		eb := v.(*EnvironmentBroadcaster)
		result[env] = int(eb.count.Load())
		return true
	})
	return result
}

// LastSnapshot returns the last-known full snapshot JSON for environment, as
// recorded by the reconciler, and whether one has been recorded yet. Used to
// diff the freshly-fetched flag-api snapshot against what the hub last saw.
func (h *Hub) LastSnapshot(environment string) ([]byte, bool) {
	v, ok := h.snapshots.Load(environment)
	if !ok {
		return nil, false
	}
	return v.([]byte), true
}

// SetLastSnapshot records the full snapshot JSON for environment. Called by
// the reconciler after every poll so the next poll can diff against it.
func (h *Hub) SetLastSnapshot(environment string, snapshot []byte) {
	h.snapshots.Store(environment, snapshot)
}

// eventTypeFor derives the SSE event: name from a FlagEvent's fields. Shared
// by Broadcast and ReplayOrSnapshot's replayed-message path so a flag change
// gets the same event: name whether it is delivered live or replayed after a
// reconnect.
func eventTypeFor(event FlagEvent) string {
	if !event.Enabled && (event.Reason == "circuit_breaker" ||
		event.Reason == "manual_kill_switch" ||
		event.Reason == "slo_burn_rate") {
		return "kill_switch"
	}
	return "flag_updated"
}

// sseFrame builds a raw SSE wire-format frame for a FlagEvent.
// Format: "id: <id>\nevent: <type>\ndata: <json>\n\n" (id: line omitted when
// id is "" -- SSE's id: field is optional, and only the Streams delivery
// path has a real, XRANGE-replayable ID to offer).
func sseFrame(eventType string, event FlagEvent, id string) []byte {
	data := fmt.Sprintf(
		`{"flag_key":%q,"enabled":%v,"rollout_pct":%d,"reason":%q,"ts":%d,"environment":%q}`,
		event.FlagKey,
		event.Enabled,
		event.RolloutPct,
		event.Reason,
		event.Ts,
		event.Environment,
	)
	return []byte(fmt.Sprintf("%sevent: %s\ndata: %s\n\n", idLine(id), eventType, data))
}

// snapshotFrame builds an SSE "snapshot" frame carrying a full flag-state
// JSON payload (as already produced by the reconciler/flag-api's snapshot
// endpoint) plus, when known, the stream's current newest entry ID so the
// client's Last-Event-ID cursor advances to "now" instead of staying stuck
// at the (already-trimmed) ID it reconnected with.
func snapshotFrame(snapshot []byte, newestID string) []byte {
	return []byte(fmt.Sprintf("%sevent: snapshot\ndata: %s\n\n", idLine(newestID), snapshot))
}

// rawFrame builds an SSE frame carrying a pre-serialized JSON payload
// verbatim -- for event kinds the gateway relays without ever needing to
// typed-unmarshal, inspect a field, or dedupe (currently only
// prerequisites_updated, which is Streams-only with no legacy pub/sub
// dual-write to dedupe against -- see Broadcaster.RunStreamConsumer's own
// "event" Values-map discriminator check). Shares snapshotFrame's exact
// shape, just with a caller-supplied event: name instead of a hardcoded
// "snapshot".
func rawFrame(eventType string, payload []byte, id string) []byte {
	return []byte(fmt.Sprintf("%sevent: %s\ndata: %s\n\n", idLine(id), eventType, payload))
}

func idLine(id string) string {
	if id == "" {
		return ""
	}
	return fmt.Sprintf("id: %s\n", id)
}

// ReplayOrSnapshot catches an SSE client up after it reconnects with a
// Last-Event-ID value (GW-2). It returns ready-to-write SSE frames, in
// order:
//   - the events published to environment's stream strictly after lastID,
//     each carrying its own real id: line (XRANGE, O(delta) — the whole
//     point of resumable streams over a full re-sync), or
//   - if lastID predates what the stream currently retains (MaxLen/Approx
//     eviction already trimmed past it -- ReplaySince cannot otherwise tell
//     "caught up" apart from "gap already lost" from an empty XRANGE result
//     alone), a single "snapshot" frame carrying the environment's
//     last-known full state.
//
// Returns nil if this Hub has no Redis client set (SetRedis never called),
// lastID is empty, or neither a replay nor a snapshot is available.
func (h *Hub) ReplayOrSnapshot(ctx context.Context, environment, lastID string) [][]byte {
	if h.rdb == nil || lastID == "" {
		return nil
	}
	streamKey := StreamKey(environment)

	msgs, ok, err := ReplaySince(ctx, h.rdb, streamKey, lastID)
	if err != nil {
		h.logger.Warn("replay: XRANGE failed, skipping catch-up",
			zap.String("env", environment), zap.Error(err))
		return nil
	}
	if ok {
		return BuildReplayFrames(msgs)
	}

	snapshot, hasSnapshot := h.LastSnapshot(environment)
	if !hasSnapshot {
		return nil
	}
	newestID := ""
	if newest, err := h.rdb.XRevRangeN(ctx, streamKey, "+", "-", 1).Result(); err == nil && len(newest) > 0 {
		newestID = newest[0].ID
	}
	return [][]byte{snapshotFrame(snapshot, newestID)}
}
