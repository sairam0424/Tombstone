package v1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
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
//
// The hub now pre-serializes SSE frames once per event (not once per client),
// so this handler simply drains the buffered channel and writes raw bytes.
// The channel type is chan []byte — each value is a complete SSE frame ready
// to be written verbatim to the response writer.
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

	// Use the chi request ID as the stable client identifier so log lines are
	// correlated. Falls back to a timestamp-based ID if the middleware is absent.
	clientID := chiMiddleware.GetReqID(r.Context())
	if clientID == "" {
		clientID = fmt.Sprintf("client-%d", time.Now().UnixNano())
	}

	// GW-2: Subscribe BEFORE computing the replay window below, not after.
	// Any event broadcast in the gap between "replay window computed" and
	// "live delivery begins" is then guaranteed to reach this client via
	// EITHER this channel OR the XRANGE replay (delivered twice) rather than
	// falling through a gap between the two (missed entirely). SSE's
	// id:/Last-Event-ID contract is at-least-once by design, so an
	// occasional duplicate flag_updated delivery here is within spec, not a
	// bug — clients must already treat re-applying the same flag state as a
	// no-op.
	ch := h.hub.Subscribe(environment, clientID)
	defer h.hub.Unsubscribe(environment, clientID, ch)

	// A reconnecting EventSource client automatically sends back the id: of
	// the last frame it received via this header — honor it to replay only
	// what was missed (XRANGE, O(delta)) instead of forcing a full resync on
	// every transient disconnect. A fresh (non-reconnecting) client sends no
	// such header, so this is a pure addition for clients that opt in by
	// tracking it — see GW-2's scope note: no SDK does yet, so this has no
	// client-visible effect until one is updated to.
	if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
		frames := h.hub.ReplayOrSnapshot(r.Context(), environment, lastEventID)
		for _, frame := range frames {
			_, _ = w.Write(frame)
		}
		if len(frames) > 0 {
			flusher.Flush()
		}
	}

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	h.logger.Debug("SSE client connected",
		zap.String("env", environment),
		zap.String("client", clientID))

	for {
		select {
		case frame, ok := <-ch:
			if !ok {
				return // channel closed by Unsubscribe
			}
			// frame is a pre-serialized SSE wire-format payload, write verbatim.
			_, _ = w.Write(frame)
			flusher.Flush()

		case <-heartbeat.C:
			fmt.Fprintf(w, "event: heartbeat\ndata: {\"ts\":%d}\n\n", time.Now().Unix())
			flusher.Flush()

		case <-r.Context().Done():
			// Client disconnected
			h.logger.Debug("SSE client disconnected",
				zap.String("env", environment),
				zap.String("client", clientID))
			return
		}
	}
}
