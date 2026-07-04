package v1

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/gateway/internal/hub"
)

// DLQHandler serves the internal dead-letter-queue inspection and replay
// routes for poison Redis Streams messages. See internal/hub/dlq.go for the
// reclaim-sweep logic that populates these DLQ streams.
type DLQHandler struct {
	rdb    *redis.Client
	logger *zap.Logger
}

func NewDLQHandler(rdb *redis.Client, logger *zap.Logger) *DLQHandler {
	return &DLQHandler{rdb: rdb, logger: logger}
}

// dlqEntry is the JSON shape of one dead-lettered stream message.
type dlqEntry struct {
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields"`
}

// dlqListResponse is the JSON shape returned by GET /internal/dlq/{environment}.
type dlqListResponse struct {
	Environment string     `json:"environment"`
	StreamKey   string     `json:"stream_key"`
	Entries     []dlqEntry `json:"entries"`
}

// ListDLQ handles GET /internal/dlq/{environment}.
// XRANGEs the environment's "<stream>:dlq" stream in full and returns every
// entry currently parked there for human inspection.
func (h *DLQHandler) ListDLQ(w http.ResponseWriter, r *http.Request) {
	environment := chi.URLParam(r, "environment")
	if environment == "" {
		http.Error(w, `{"error":"environment path parameter is required"}`, http.StatusBadRequest)
		return
	}

	dlqKey := hub.DLQStreamKey(hub.StreamKey(environment))

	msgs, err := h.rdb.XRange(r.Context(), dlqKey, "-", "+").Result()
	if err != nil {
		h.logger.Error("dlq: xrange failed", zap.String("dlq", dlqKey), zap.Error(err))
		http.Error(w, `{"error":"failed to read dlq"}`, http.StatusInternalServerError)
		return
	}

	entries := make([]dlqEntry, 0, len(msgs))
	for _, m := range msgs {
		fields := make(map[string]string, len(m.Values))
		for k, v := range m.Values {
			if s, ok := v.(string); ok {
				fields[k] = s
			}
		}
		entries = append(entries, dlqEntry{ID: m.ID, Fields: fields})
	}

	resp := dlqListResponse{Environment: environment, StreamKey: dlqKey, Entries: entries}
	writeJSON(w, http.StatusOK, resp)
}

// dlqReplayResponse is the JSON shape returned by POST /internal/dlq/{environment}/replay.
type dlqReplayResponse struct {
	Environment string `json:"environment"`
	Replayed    int    `json:"replayed"`
}

// ReplayDLQ handles POST /internal/dlq/{environment}/replay.
//
// This is a MANUAL, human-triggered operation — deliberately NOT automatic,
// unlike the ClickHouse writer's 60s auto-replayer
// (services/intelligence/app/telemetry/clickhouse_writer.py's _dlq_replayer).
// The ClickHouse DLQ exists because of transient warehouse-side failures
// (network blips, momentary overload) where blind retry is the right
// default. A flag-event message that failed unmarshalling
// maxDeliveryAttempts times, by contrast, has already been read and rejected
// by every consumer identity that touched it across the full
// reclaimIdleThreshold window each time — that pattern looks like a genuine
// schema/version mismatch between producer and consumer, not a blip.
// Blindly replaying it back onto the primary stream on a timer would just
// requeue the same failure forever and mask the underlying incompatibility.
// A human should look at the entry, understand why it failed, and decide
// whether replay is even the right fix before triggering it.
func (h *DLQHandler) ReplayDLQ(w http.ResponseWriter, r *http.Request) {
	environment := chi.URLParam(r, "environment")
	if environment == "" {
		http.Error(w, `{"error":"environment path parameter is required"}`, http.StatusBadRequest)
		return
	}

	streamKey := hub.StreamKey(environment)
	dlqKey := hub.DLQStreamKey(streamKey)
	ctx := r.Context()

	msgs, err := h.rdb.XRange(ctx, dlqKey, "-", "+").Result()
	if err != nil {
		h.logger.Error("dlq: xrange failed during replay", zap.String("dlq", dlqKey), zap.Error(err))
		http.Error(w, `{"error":"failed to read dlq"}`, http.StatusInternalServerError)
		return
	}

	replayed := 0
	for _, m := range msgs {
		if err := h.rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: streamKey,
			MaxLen: 10_000, // matches scheduler.go's publishToStream trim convention
			Approx: true,
			Values: m.Values,
		}).Err(); err != nil {
			h.logger.Error("dlq: replay xadd failed",
				zap.String("stream", streamKey), zap.String("dlq_id", m.ID), zap.Error(err))
			continue
		}
		// Only remove from the DLQ once it's safely back on the primary
		// stream — an XAdd failure leaves the entry in the DLQ for the next
		// replay attempt rather than losing it.
		if err := h.rdb.XDel(ctx, dlqKey, m.ID).Err(); err != nil {
			h.logger.Warn("dlq: failed to delete replayed entry from dlq",
				zap.String("dlq", dlqKey), zap.String("dlq_id", m.ID), zap.Error(err))
		}
		replayed++
	}

	writeJSON(w, http.StatusOK, dlqReplayResponse{Environment: environment, Replayed: replayed})
}

// writeJSON marshals v as the JSON response body with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
