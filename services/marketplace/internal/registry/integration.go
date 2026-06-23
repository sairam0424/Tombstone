package registry

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// redisKey is the Redis hash that stores all persisted integrations.
const redisKey = "tombstone:marketplace:integrations"

// IntegrationStatus represents the installation state of an integration.
type IntegrationStatus string

const (
	StatusAvailable IntegrationStatus = "available"
	StatusInstalled IntegrationStatus = "installed"
	StatusDisabled  IntegrationStatus = "disabled"
)

// EventType represents flag lifecycle events dispatched to webhooks.
type EventType string

const (
	EventFlagCreated       EventType = "flag.created"
	EventFlagEnabled       EventType = "flag.enabled"
	EventFlagDisabled      EventType = "flag.disabled"
	EventFlagKillSwitch    EventType = "flag.kill_switch"
	EventFlagRollback      EventType = "flag.rollback"
	EventFlagStaleDetected EventType = "flag.stale_detected"
	EventFlagArchived      EventType = "flag.archived"
)

// Integration describes a third-party or first-party webhook integration.
type Integration struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Category     string            `json:"category"`
	IconURL      string            `json:"icon_url"`
	WebhookURL   string            `json:"webhook_url,omitempty"`
	Events       []EventType       `json:"events"`
	Status       IntegrationStatus `json:"status"`
	Config       map[string]string `json:"config,omitempty"`
	IsFirstParty bool              `json:"is_first_party"`
	// Bidirectional indicates the integration supports inbound webhook delivery
	// in addition to the standard outbound (Tombstone -> third-party) flow.
	Bidirectional bool `json:"bidirectional,omitempty"`
	// InboundEndpoint is the Tombstone-hosted path that accepts inbound payloads
	// from the third-party service (populated only when Bidirectional is true).
	InboundEndpoint string `json:"inbound_endpoint,omitempty"`
}

// firstPartyIntegrations defines the built-in catalog of integrations.
var firstPartyIntegrations = []Integration{
	{
		ID:           "slack",
		Name:         "Slack",
		Description:  "Send flag change notifications to Slack channels.",
		Category:     "notifications",
		IconURL:      "https://assets.tombstone.io/integrations/slack.svg",
		Events:       []EventType{EventFlagEnabled, EventFlagDisabled, EventFlagKillSwitch, EventFlagRollback},
		Status:       StatusAvailable,
		IsFirstParty: true,
	},
	{
		ID:          "datadog",
		Name:        "Datadog",
		Description: "Annotate Datadog dashboards with flag change events and receive monitor alerts to auto-trigger blast radius checks and kill switches.",
		Category:    "observability",
		IconURL:     "https://assets.tombstone.io/integrations/datadog.svg",
		Events:      []EventType{EventFlagCreated, EventFlagEnabled, EventFlagDisabled, EventFlagKillSwitch, EventFlagRollback, EventFlagArchived},
		Status:      StatusAvailable,
		IsFirstParty:    true,
		Bidirectional:   true,
		InboundEndpoint: "/api/v1/marketplace/inbound/datadog",
	},
	{
		ID:           "pagerduty",
		Name:         "PagerDuty",
		Description:  "Trigger PagerDuty incidents on kill-switch or rollback events.",
		Category:     "incident-management",
		IconURL:      "https://assets.tombstone.io/integrations/pagerduty.svg",
		Events:       []EventType{EventFlagKillSwitch, EventFlagRollback},
		Status:       StatusAvailable,
		IsFirstParty: true,
	},
	{
		ID:           "opsgenie",
		Name:         "OpsGenie",
		Description:  "Create OpsGenie alerts when flags trigger blast-radius events.",
		Category:     "incident-management",
		IconURL:      "https://assets.tombstone.io/integrations/opsgenie.svg",
		Events:       []EventType{EventFlagKillSwitch, EventFlagRollback, EventFlagStaleDetected},
		Status:       StatusAvailable,
		IsFirstParty: true,
	},
	{
		ID:           "jira",
		Name:         "Jira",
		Description:  "Auto-create Jira issues for stale or archived flags.",
		Category:     "project-management",
		IconURL:      "https://assets.tombstone.io/integrations/jira.svg",
		Events:       []EventType{EventFlagStaleDetected, EventFlagArchived},
		Status:       StatusAvailable,
		IsFirstParty: true,
	},
	{
		ID:           "linear",
		Name:         "Linear",
		Description:  "Create Linear issues for flag cleanup tasks.",
		Category:     "project-management",
		IconURL:      "https://assets.tombstone.io/integrations/linear.svg",
		Events:       []EventType{EventFlagStaleDetected, EventFlagArchived},
		Status:       StatusAvailable,
		IsFirstParty: true,
	},
	{
		ID:           "opentelemetry",
		Name:         "OpenTelemetry",
		Description:  "Emit flag evaluation traces and metrics via OTLP.",
		Category:     "observability",
		IconURL:      "https://assets.tombstone.io/integrations/opentelemetry.svg",
		Events:       []EventType{EventFlagCreated, EventFlagEnabled, EventFlagDisabled, EventFlagKillSwitch, EventFlagRollback, EventFlagStaleDetected, EventFlagArchived},
		Status:       StatusAvailable,
		IsFirstParty: true,
	},
}

// Registry holds all registered integrations (first-party + third-party).
// The in-memory map is the read cache; Redis is the durable store.
type Registry struct {
	mu           sync.RWMutex
	integrations map[string]Integration
	rdb          *redis.Client
	logger       *zap.Logger
}

// NewRegistry creates a Registry seeded with all first-party integrations.
// rdb may be nil — the registry will operate in ephemeral in-memory mode.
func NewRegistry(rdb *redis.Client, logger *zap.Logger) *Registry {
	r := &Registry{
		integrations: make(map[string]Integration, len(firstPartyIntegrations)),
		rdb:          rdb,
		logger:       logger,
	}
	for _, i := range firstPartyIntegrations {
		r.integrations[i.ID] = i
	}
	return r
}

// LoadFromRedis reads all persisted integrations from Redis and merges them
// into the in-memory map. Entries that fail to unmarshal are logged and skipped.
// Call this once at startup, before serving traffic.
func (r *Registry) LoadFromRedis(ctx context.Context) {
	if r.rdb == nil {
		return
	}

	entries, err := r.rdb.HGetAll(ctx, redisKey).Result()
	if err != nil {
		r.logger.Warn("registry: failed to load from Redis",
			zap.String("key", redisKey),
			zap.Error(err),
		)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	loaded := 0
	for id, raw := range entries {
		var i Integration
		if err := json.Unmarshal([]byte(raw), &i); err != nil {
			r.logger.Warn("registry: skipping malformed entry from Redis",
				zap.String("id", id),
				zap.Error(err),
			)
			continue
		}
		r.integrations[i.ID] = i
		loaded++
	}

	r.logger.Info("registry: loaded integrations from Redis",
		zap.Int("count", loaded),
	)
}

// List returns a snapshot of all integrations.
func (r *Registry) List() []Integration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Integration, 0, len(r.integrations))
	for _, i := range r.integrations {
		out = append(out, i)
	}
	return out
}

// Get returns a single integration by ID, and whether it was found.
func (r *Registry) Get(id string) (Integration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	i, ok := r.integrations[id]
	return i, ok
}

// Install sets an integration to installed with the provided webhookURL and config.
// Returns false if the integration does not exist.
func (r *Registry) Install(id, webhookURL string, config map[string]string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.integrations[id]
	if !ok {
		return false
	}

	// Immutable struct update — create a new Integration value.
	updated := Integration{
		ID:              existing.ID,
		Name:            existing.Name,
		Description:     existing.Description,
		Category:        existing.Category,
		IconURL:         existing.IconURL,
		WebhookURL:      webhookURL,
		Events:          existing.Events,
		Status:          StatusInstalled,
		Config:          config,
		IsFirstParty:    existing.IsFirstParty,
		Bidirectional:   existing.Bidirectional,
		InboundEndpoint: existing.InboundEndpoint,
	}
	r.integrations[id] = updated
	r.persistToRedis(id, updated)
	return true
}

// Uninstall resets an integration back to available and clears webhook/config.
// Returns false if the integration does not exist.
func (r *Registry) Uninstall(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.integrations[id]
	if !ok {
		return false
	}

	updated := Integration{
		ID:              existing.ID,
		Name:            existing.Name,
		Description:     existing.Description,
		Category:        existing.Category,
		IconURL:         existing.IconURL,
		WebhookURL:      "",
		Events:          existing.Events,
		Status:          StatusAvailable,
		Config:          nil,
		IsFirstParty:    existing.IsFirstParty,
		Bidirectional:   existing.Bidirectional,
		InboundEndpoint: existing.InboundEndpoint,
	}
	r.integrations[id] = updated
	r.deleteFromRedis(id)
	return true
}

// Register adds a third-party integration to the registry.
// Returns false if an integration with that ID already exists.
func (r *Registry) Register(i Integration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.integrations[i.ID]; exists {
		return false
	}
	i.IsFirstParty = false
	r.integrations[i.ID] = i
	r.persistToRedis(i.ID, i)
	return true
}

// InstalledWebhooks returns all integrations that are installed and subscribed to the given event.
func (r *Registry) InstalledWebhooks(event EventType) []Integration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Integration
	for _, i := range r.integrations {
		if i.Status != StatusInstalled {
			continue
		}
		for _, e := range i.Events {
			if e == event {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// persistToRedis marshals integration i and writes it to the Redis hash.
// Must be called with r.mu held (write lock).
func (r *Registry) persistToRedis(id string, i Integration) {
	if r.rdb == nil {
		return
	}

	raw, err := json.Marshal(i)
	if err != nil {
		r.logger.Error("registry: failed to marshal integration for Redis",
			zap.String("id", id),
			zap.Error(err),
		)
		return
	}

	if err := r.rdb.HSet(context.Background(), redisKey, id, string(raw)).Err(); err != nil {
		r.logger.Error("registry: failed to persist integration to Redis",
			zap.String("id", id),
			zap.Error(err),
		)
	}
}

// deleteFromRedis removes an integration entry from the Redis hash.
// Must be called with r.mu held (write lock).
func (r *Registry) deleteFromRedis(id string) {
	if r.rdb == nil {
		return
	}

	if err := r.rdb.HDel(context.Background(), redisKey, id).Err(); err != nil {
		r.logger.Error("registry: failed to remove integration from Redis",
			zap.String("id", id),
			zap.Error(err),
		)
	}
}
