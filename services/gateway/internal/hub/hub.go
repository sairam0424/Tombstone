package hub

import (
	"fmt"
	"sync"
	"sync/atomic"

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
	clients      sync.Map       // clientID (string) → chan []byte
	count        atomic.Int64   // live connection count
	totalSent    atomic.Int64   // cumulative events delivered
	totalDropped atomic.Int64   // cumulative events dropped (backpressure)
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
func (eb *EnvironmentBroadcaster) Broadcast(payload []byte) (sent, dropped int) {
	eb.clients.Range(func(_, v any) bool {
		ch := v.(chan []byte)
		select {
		case ch <- payload:
			sent++
		default:
			// Client is too slow — send lag warning then drop the real event.
			select {
			case ch <- lagEvent:
			default:
				// Lag channel also full; just drop everything for this client.
			}
			dropped++
		}
		return true
	})
	eb.totalSent.Add(int64(sent))
	eb.totalDropped.Add(int64(dropped))
	return sent, dropped
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
	envs   sync.Map // environment (string) → *EnvironmentBroadcaster
	logger *zap.Logger
}

func NewHub(logger *zap.Logger) *Hub {
	return &Hub{logger: logger}
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
func (h *Hub) Broadcast(environment string, event FlagEvent) {
	v, ok := h.envs.Load(environment)
	if !ok {
		return // no clients subscribed to this environment
	}
	eb := v.(*EnvironmentBroadcaster)

	eventType := "flag_updated"
	if !event.Enabled && (event.Reason == "circuit_breaker" ||
		event.Reason == "manual_kill_switch" ||
		event.Reason == "slo_burn_rate") {
		eventType = "kill_switch"
	}

	// Pre-serialize once; all clients receive the same byte slice (read-only).
	// JSON marshaling is done inline to avoid an extra import cycle.
	payload := sseFrame(eventType, event)

	sent, dropped := eb.Broadcast(payload)
	if dropped > 0 {
		h.logger.Warn("backpressure: events dropped for slow clients",
			zap.String("env", environment),
			zap.String("flag", event.FlagKey),
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

// sseFrame builds a raw SSE wire-format frame for a FlagEvent.
// Format: "event: <type>\ndata: <json>\n\n"
func sseFrame(eventType string, event FlagEvent) []byte {
	data := fmt.Sprintf(
		`{"flag_key":%q,"enabled":%v,"rollout_pct":%d,"reason":%q,"ts":%d,"environment":%q}`,
		event.FlagKey,
		event.Enabled,
		event.RolloutPct,
		event.Reason,
		event.Ts,
		event.Environment,
	)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data))
}
