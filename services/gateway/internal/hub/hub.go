package hub

import (
	"sync"

	"go.uber.org/zap"
)

// FlagEvent is the payload sent to SDK clients over SSE.
type FlagEvent struct {
	FlagKey     string `json:"flag_key"`
	Enabled     bool   `json:"enabled"`
	RolloutPct  int    `json:"rollout_pct"`
	Reason      string `json:"reason"`
	Ts          int64  `json:"ts"`
	Environment string `json:"environment"`
}

// Hub manages all active SSE connections keyed by environment.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[chan FlagEvent]struct{}
	logger  *zap.Logger
}

func NewHub(logger *zap.Logger) *Hub {
	return &Hub{
		clients: make(map[string]map[chan FlagEvent]struct{}),
		logger:  logger,
	}
}

// Subscribe registers a new SSE client for an environment.
// Returns a buffered channel to receive events.
func (h *Hub) Subscribe(environment string) chan FlagEvent {
	ch := make(chan FlagEvent, 64) // buffered — never block Broadcast on slow clients
	h.mu.Lock()
	if h.clients[environment] == nil {
		h.clients[environment] = make(map[chan FlagEvent]struct{})
	}
	h.clients[environment][ch] = struct{}{}
	h.mu.Unlock()
	h.logger.Debug("client subscribed", zap.String("env", environment))
	return ch
}

// Unsubscribe removes a client channel and closes it.
func (h *Hub) Unsubscribe(environment string, ch chan FlagEvent) {
	h.mu.Lock()
	delete(h.clients[environment], ch)
	h.mu.Unlock()
	close(ch)
	h.logger.Debug("client unsubscribed", zap.String("env", environment))
}

// Broadcast sends an event to all clients subscribed to the given environment.
// Uses non-blocking send — slow clients that can't keep up are dropped.
func (h *Hub) Broadcast(environment string, event FlagEvent) {
	h.mu.RLock()
	clients := h.clients[environment]
	h.mu.RUnlock()

	for ch := range clients {
		select {
		case ch <- event:
		default:
			// Client channel full — skip this event for this client
			h.logger.Warn("client channel full, dropping event",
				zap.String("env", environment),
				zap.String("flag", event.FlagKey))
		}
	}
}

// ConnectionCount returns the number of active connections for an environment.
func (h *Hub) ConnectionCount(environment string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients[environment])
}

// AllConnectionCounts returns a map of environment → connection count.
func (h *Hub) AllConnectionCounts() map[string]int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make(map[string]int, len(h.clients))
	for env, clients := range h.clients {
		result[env] = len(clients)
	}
	return result
}
