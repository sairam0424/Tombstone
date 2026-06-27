package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/sairam0424/Tombstone/services/marketplace/internal/registry"
)

// FlagEvent carries the payload dispatched to integration webhooks.
type FlagEvent struct {
	EventType   registry.EventType     `json:"event_type"`
	FlagKey     string                 `json:"flag_key"`
	Environment string                 `json:"environment"`
	Actor       string                 `json:"actor"`
	Ts          int64                  `json:"ts"`
	Metadata    map[string]any         `json:"metadata,omitempty"`
}

// Dispatcher fans out FlagEvents to all subscribed, installed webhook integrations.
type Dispatcher struct {
	registry   *registry.Registry
	httpClient *http.Client
	logger     *zap.Logger
}

// NewDispatcher constructs a Dispatcher with a sensible default HTTP client.
func NewDispatcher(reg *registry.Registry, logger *zap.Logger) *Dispatcher {
	return &Dispatcher{
		registry: reg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}
}

// Dispatch looks up installed webhooks for the event type and fires each
// in its own goroutine so delivery is non-blocking.
func (d *Dispatcher) Dispatch(ctx context.Context, event FlagEvent) {
	integrations := d.registry.InstalledWebhooks(event.EventType)
	for _, i := range integrations {
		go d.deliver(ctx, i, event)
	}
}

// deliver marshals the event payload and POSTs it to the integration's webhook URL.
func (d *Dispatcher) deliver(ctx context.Context, i registry.Integration, event FlagEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		d.logger.Error("webhook: marshal failed",
			zap.String("integration", i.ID),
			zap.Error(err),
		)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		d.logger.Error("webhook: build request failed",
			zap.String("integration", i.ID),
			zap.String("url", i.WebhookURL),
			zap.Error(err),
		)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tombstone-Event", string(event.EventType))
	req.Header.Set("X-Tombstone-Integration", i.ID)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		d.logger.Error("webhook: delivery failed",
			zap.String("integration", i.ID),
			zap.String("url", i.WebhookURL),
			zap.Error(err),
		)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		d.logger.Info("webhook: delivered",
			zap.String("integration", i.ID),
			zap.String("event", string(event.EventType)),
			zap.Int("status", resp.StatusCode),
		)
	} else {
		d.logger.Warn("webhook: non-2xx response",
			zap.String("integration", i.ID),
			zap.String("event", string(event.EventType)),
			zap.Int("status", resp.StatusCode),
			zap.String("url", i.WebhookURL),
			zap.Error(fmt.Errorf("unexpected status %d", resp.StatusCode)),
		)
	}
}
