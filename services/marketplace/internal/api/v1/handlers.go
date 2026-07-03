package v1

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/tombstone/marketplace/internal/httpclient"
	"github.com/tombstone/marketplace/internal/integrations"
	"github.com/tombstone/marketplace/internal/registry"
	"github.com/tombstone/marketplace/internal/webhook"
)

// Handler bundles the registry and dispatcher used by all route handlers.
type Handler struct {
	reg           *registry.Registry
	dispatcher    *webhook.Dispatcher
	logger        *zap.Logger
	slackApp      *integrations.SlackApp
	resilientHTTP *httpclient.ResilientClient
}

// NewHandler constructs a Handler.
func NewHandler(reg *registry.Registry, dispatcher *webhook.Dispatcher, logger *zap.Logger) *Handler {
	return &Handler{
		reg:           reg,
		dispatcher:    dispatcher,
		logger:        logger,
		resilientHTTP: httpclient.NewResilientClient(httpclient.DefaultConfig(), nil, logger),
	}
}

// SetSlackApp wires the SlackApp into the handler so Slack route methods are active.
func (h *Handler) SetSlackApp(app *integrations.SlackApp) {
	h.slackApp = app
}

// writeJSON serialises v to the response with the given status code.
func (h *Handler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("writeJSON encode error", zap.Error(err))
	}
}

// ListIntegrations handles GET /api/v1/marketplace
// Optional query param: ?category=<category> to filter results.
func (h *Handler) ListIntegrations(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	all := h.reg.List()

	if category == "" {
		h.writeJSON(w, http.StatusOK, all)
		return
	}

	filtered := make([]registry.Integration, 0, len(all))
	for _, i := range all {
		if i.Category == category {
			filtered = append(filtered, i)
		}
	}
	h.writeJSON(w, http.StatusOK, filtered)
}

// GetIntegration handles GET /api/v1/marketplace/{id}
func (h *Handler) GetIntegration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	i, ok := h.reg.Get(id)
	if !ok {
		h.writeJSON(w, http.StatusNotFound, map[string]string{"error": "integration not found"})
		return
	}
	h.writeJSON(w, http.StatusOK, i)
}

// InstallIntegration handles POST /api/v1/marketplace/{id}/install
// Body: {"webhook_url": "https://...", "config": {...}}
func (h *Handler) InstallIntegration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var body struct {
		WebhookURL string            `json:"webhook_url"`
		Config     map[string]string `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.WebhookURL == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "webhook_url is required"})
		return
	}

	if ok := h.reg.Install(id, body.WebhookURL, body.Config); !ok {
		h.writeJSON(w, http.StatusNotFound, map[string]string{"error": "integration not found"})
		return
	}

	i, _ := h.reg.Get(id)
	h.writeJSON(w, http.StatusOK, i)
}

// UninstallIntegration handles DELETE /api/v1/marketplace/{id}
func (h *Handler) UninstallIntegration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if ok := h.reg.Uninstall(id); !ok {
		h.writeJSON(w, http.StatusNotFound, map[string]string{"error": "integration not found"})
		return
	}
	i, _ := h.reg.Get(id)
	h.writeJSON(w, http.StatusOK, i)
}

// RegisterIntegration handles POST /api/v1/marketplace/register
// Allows third-party services to self-register an integration.
func (h *Handler) RegisterIntegration(w http.ResponseWriter, r *http.Request) {
	var i registry.Integration
	if err := json.NewDecoder(r.Body).Decode(&i); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if i.ID == "" || i.Name == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id and name are required"})
		return
	}

	if ok := h.reg.Register(i); !ok {
		h.writeJSON(w, http.StatusConflict, map[string]string{"error": "integration already exists"})
		return
	}
	h.writeJSON(w, http.StatusCreated, i)
}

// TriggerEvent handles POST /api/v1/marketplace/events
// Internal endpoint — called by flag-api on flag lifecycle changes.
// Body mirrors webhook.FlagEvent.
func (h *Handler) TriggerEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventType   registry.EventType `json:"event_type"`
		FlagKey     string             `json:"flag_key"`
		Environment string             `json:"environment"`
		Actor       string             `json:"actor"`
		Ts          int64              `json:"ts"`
		Metadata    map[string]any     `json:"metadata,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.EventType == "" || body.FlagKey == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "event_type and flag_key are required"})
		return
	}

	if body.Ts == 0 {
		body.Ts = time.Now().UnixMilli()
	}

	event := webhook.FlagEvent{
		EventType:   body.EventType,
		FlagKey:     body.FlagKey,
		Environment: body.Environment,
		Actor:       body.Actor,
		Ts:          body.Ts,
		Metadata:    body.Metadata,
	}

	h.dispatcher.Dispatch(r.Context(), event)
	h.writeJSON(w, http.StatusAccepted, map[string]string{"status": "dispatched"})
}
