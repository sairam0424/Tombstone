package v1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tombstone/gateway/internal/hub"
	"go.uber.org/zap"
)

type SSEHandler struct {
	hub    *hub.Hub
	logger *zap.Logger
}

func NewSSEHandler(h *hub.Hub, logger *zap.Logger) *SSEHandler {
	return &SSEHandler{hub: h, logger: logger}
}

// Stream handles GET /api/v1/stream
// SSE endpoint for SDK clients to receive real-time flag updates.
func (h *SSEHandler) Stream(w http.ResponseWriter, r *http.Request) {
	environment := r.URL.Query().Get("environment")
	if environment == "" {
		environment = "production"
	}

	// Validate service token (basic check — full auth middleware on router)
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, `{"error":"missing Authorization header"}`, http.StatusUnauthorized)
		return
	}

	// Enforce per-environment SSE connection limit.
	if !checkSSEConnLimit(w, environment) {
		return
	}
	defer releaseSSEConn(environment)

	// SSE headers — must be set before WriteHeader
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send 200 and flush immediately so the client knows the connection is live
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Send initial "connected" event
	connectedData, _ := json.Marshal(map[string]any{
		"environment": environment,
		"ts":          time.Now().Unix(),
	})
	fmt.Fprintf(w, "event: connected\ndata: %s\n\n", connectedData)
	flusher.Flush()

	// Subscribe to hub for this environment
	ch := h.hub.Subscribe(environment)
	defer h.hub.Unsubscribe(environment, ch)

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	h.logger.Debug("SSE client connected", zap.String("env", environment))

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return // channel closed (Unsubscribe was called)
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			eventType := "flag_updated"
			if !event.Enabled && (event.Reason == "circuit_breaker" || event.Reason == "manual_kill_switch" || event.Reason == "slo_burn_rate") {
				eventType = "kill_switch"
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, payload)
			flusher.Flush()

		case <-heartbeat.C:
			fmt.Fprintf(w, "event: heartbeat\ndata: {\"ts\":%d}\n\n", time.Now().Unix())
			flusher.Flush()

		case <-r.Context().Done():
			// Client disconnected
			h.logger.Debug("SSE client disconnected", zap.String("env", environment))
			return
		}
	}
}
