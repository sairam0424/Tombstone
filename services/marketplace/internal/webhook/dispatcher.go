package webhook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/tombstone/marketplace/internal/httpclient"
	"github.com/tombstone/marketplace/internal/registry"
)

// FlagEvent carries the payload dispatched to integration webhooks.
type FlagEvent struct {
	EventType   registry.EventType `json:"event_type"`
	FlagKey     string             `json:"flag_key"`
	Environment string             `json:"environment"`
	Actor       string             `json:"actor"`
	Ts          int64              `json:"ts"`
	Metadata    map[string]any     `json:"metadata,omitempty"`
}

// resilientClientConfig returns the retry/circuit-breaker tuning used for all
// outbound webhook deliveries. Webhook receivers are arbitrary third-party
// endpoints (PagerDuty, Jira, OpsGenie, Linear, ...) which are frequently
// slower to respond than intra-cluster service calls, so the per-attempt
// timeout is raised from httpclient.DefaultConfig()'s 10s to 15s — matching
// the 15s timeout this service's other outbound integration callers already
// use (see internal/api/v1/inbound.go's flag-api/evaluator calls). Retry
// count, backoff, and circuit-breaker thresholds keep the defaults: 3 retries
// with 200ms-5s exponential backoff, circuit opens after 5 consecutive
// failures and stays open for 30s.
func resilientClientConfig() httpclient.ResilientClientConfig {
	cfg := httpclient.DefaultConfig()
	cfg.Timeout = 15 * time.Second
	return cfg
}

// Dispatcher fans out FlagEvents to all subscribed, installed webhook integrations.
type Dispatcher struct {
	registry *registry.Registry
	logger   *zap.Logger

	// resilientClients holds one ResilientClient — and therefore one
	// circuit breaker — per integration ID. Integrations are independent
	// third-party services with unrelated failure domains: if PagerDuty is
	// down, that should trip PagerDuty's breaker and start failing fast for
	// PagerDuty, but it must NOT ALSO fail fast for a perfectly healthy
	// Jira or Linear endpoint. A single breaker shared across every
	// integration would do exactly that — one bad actor poisons delivery to
	// every other integration — so each integration gets its own client,
	// created lazily on first delivery and cached so its breaker/retry
	// state accumulates correctly across the Dispatcher's lifetime.
	clientsMu        sync.Mutex
	resilientClients map[string]*httpclient.ResilientClient
}

// NewDispatcher constructs a Dispatcher with a sensible default HTTP client.
func NewDispatcher(reg *registry.Registry, logger *zap.Logger) *Dispatcher {
	return &Dispatcher{
		registry:         reg,
		logger:           logger,
		resilientClients: make(map[string]*httpclient.ResilientClient),
	}
}

// clientFor returns the per-integration ResilientClient, creating and
// caching one on first use. See the resilientClients field doc for why this
// is scoped per-integration rather than shared across the whole Dispatcher.
func (d *Dispatcher) clientFor(integrationID string) *httpclient.ResilientClient {
	d.clientsMu.Lock()
	defer d.clientsMu.Unlock()

	if c, ok := d.resilientClients[integrationID]; ok {
		return c
	}
	c := httpclient.NewResilientClient(resilientClientConfig(), nil, d.logger)
	d.resilientClients[integrationID] = c
	return c
}

// Dispatch looks up installed webhooks for the event type and fires each
// in its own goroutine so delivery is non-blocking.
//
// deliver's goroutines outlive Dispatch's own return by design (that's the
// entire point of "non-blocking" above) -- but an http.Server request's
// ctx is canceled the instant its handler returns (see http.Request.
// Context()'s own docs), which previously aborted every delivery attempt
// with context.Canceled before a real network round-trip could complete,
// since TriggerEvent (services/marketplace/internal/api/v1/handlers.go)
// passes r.Context() straight through to here and returns right after
// (found by adversarial review of the flag-api notifyMarketplace PR --
// the first real caller to ever exercise this path end-to-end; nothing
// had ever actually driven a live request through it before). context.
// WithoutCancel detaches from that lifetime while preserving any trace/
// span VALUES already on ctx (this handler runs inside otelhttp.
// NewHandler's instrumented chain), so cross-service tracing still
// correlates. This is safe to leave unbounded by a new explicit timeout:
// each attempt is already bounded by clientFor's own httpClient.Timeout
// (10-15s, set directly on the http.Client, independent of ctx's
// deadline -- see resilient_client.go's NewResilientClient), and the
// retry loop itself terminates on MaxRetries, not on ctx cancellation.
func (d *Dispatcher) Dispatch(ctx context.Context, event FlagEvent) {
	deliverCtx := context.WithoutCancel(ctx)
	integrations := d.registry.InstalledWebhooks(event.EventType)
	for _, i := range integrations {
		go d.deliver(deliverCtx, i, event)
	}
}

// idempotencyKey derives a deterministic, Stripe-style Idempotency-Key for a
// (event, integration) pair: the sha256 hex digest of the fields that
// uniquely identify this logical delivery. FlagEvent carries no dedicated
// event-ID field, so EventType+FlagKey+Environment+Actor+Ts — combined with
// the integration ID, since the same event fans out to multiple integrations
// and each delivery is a distinct logical unit — stands in for one; that
// combination is unique per logical event+integration pair for all practical
// purposes. Because the key is derived from the FlagEvent value itself
// rather than generated fresh per HTTP attempt, every retry of the SAME
// delivery produces the SAME key. That determinism is the entire point: it
// lets receivers that honor this convention (popularized by Stripe, and
// already supported by many webhook receivers) deduplicate retried
// deliveries instead of creating duplicate PagerDuty incidents, Jira
// tickets, etc.
func idempotencyKey(event FlagEvent, integrationID string) string {
	material := fmt.Sprintf("%s|%s|%s|%s|%d|%s",
		event.EventType, event.FlagKey, event.Environment, event.Actor, event.Ts, integrationID)
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
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
	// Idempotency-Key: a Stripe-style convention many webhook receivers
	// already honor specifically to deduplicate retried deliveries. It is
	// deterministic per (event, integration) pair — see idempotencyKey's
	// doc comment — so it stays IDENTICAL across every retry attempt of
	// this same delivery, which is what lets the receiving end collapse
	// retries into a single logical incident/ticket instead of creating
	// duplicates.
	req.Header.Set("Idempotency-Key", idempotencyKey(event, i.ID))

	resp, err := d.clientFor(i.ID).Do(ctx, req)
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
