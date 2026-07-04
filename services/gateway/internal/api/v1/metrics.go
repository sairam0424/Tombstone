package v1

import (
	"encoding/json"
	"net/http"

	"github.com/tombstone/gateway/internal/hub"
	"go.uber.org/zap"
)

// GatewayMetricsHandler serves GET /api/v1/gateway/metrics.
// Returns per-environment SSE connection and event delivery statistics,
// useful for capacity planning and operational monitoring.
type GatewayMetricsHandler struct {
	hub    *hub.Hub
	logger *zap.Logger
}

func NewGatewayMetricsHandler(h *hub.Hub, logger *zap.Logger) *GatewayMetricsHandler {
	return &GatewayMetricsHandler{hub: h, logger: logger}
}

// metricsResponse is the JSON shape returned by the metrics endpoint.
type metricsResponse struct {
	Environments map[string]hub.EnvStats `json:"environments"`
}

// GetMetrics handles GET /api/v1/gateway/metrics.
// Response shape:
//
//	{
//	  "environments": {
//	    "production":  { "active_connections": 42, "total_events_sent": 1000, "total_dropped": 3 },
//	    "staging":     { "active_connections": 5,  "total_events_sent": 200,  "total_dropped": 0 }
//	  }
//	}
func (m *GatewayMetricsHandler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	resp := metricsResponse{
		Environments: m.hub.AllStats(),
	}

	body, err := json.Marshal(resp)
	if err != nil {
		m.logger.Error("marshal gateway metrics", zap.Error(err))
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
