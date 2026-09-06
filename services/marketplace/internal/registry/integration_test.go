package registry

import (
	"testing"
)

// TestNewRegistry_SeedsFirstPartyIntegrations verifies that NewRegistry seeds
// the built-in first-party catalog without requiring Redis.
func TestNewRegistry_SeedsFirstPartyIntegrations(t *testing.T) {
	reg := NewRegistry(nil, nil)

	all := reg.List()
	if len(all) == 0 {
		t.Fatal("NewRegistry must seed at least one first-party integration; got none")
	}

	// Verify every seeded entry has a non-empty ID and Name.
	for _, i := range all {
		if i.ID == "" {
			t.Errorf("seeded integration has empty ID: %+v", i)
		}
		if i.Name == "" {
			t.Errorf("seeded integration %q has empty Name", i.ID)
		}
		if i.Status != StatusAvailable {
			t.Errorf("seeded integration %q initial status = %q, want %q", i.ID, i.Status, StatusAvailable)
		}
	}
}

// TestNewRegistry_KnownFirstPartyIDs verifies that the well-known first-party
// integrations (slack, datadog, pagerduty) are present after construction.
func TestNewRegistry_KnownFirstPartyIDs(t *testing.T) {
	reg := NewRegistry(nil, nil)

	for _, id := range []string{"slack", "datadog", "pagerduty"} {
		if _, ok := reg.Get(id); !ok {
			t.Errorf("expected first-party integration %q to be present after NewRegistry", id)
		}
	}
}

// TestRegistry_Register adds a third-party integration and verifies it is retrievable.
func TestRegistry_Register(t *testing.T) {
	reg := NewRegistry(nil, nil)

	custom := Integration{
		ID:       "custom-tool",
		Name:     "Custom Tool",
		Category: "testing",
		Events:   []EventType{EventFlagEnabled},
		Status:   StatusAvailable,
	}

	ok := reg.Register(custom)
	if !ok {
		t.Fatal("Register() returned false for a new integration; expected true")
	}

	got, found := reg.Get("custom-tool")
	if !found {
		t.Fatal("Get() returned false after Register(); integration not found")
	}
	if got.ID != "custom-tool" {
		t.Errorf("Get().ID = %q, want %q", got.ID, "custom-tool")
	}
	// Register must force IsFirstParty = false on third-party entries.
	if got.IsFirstParty {
		t.Error("Register() must set IsFirstParty = false on registered integrations")
	}
}

// TestRegistry_Register_DuplicateBlocked verifies that registering an ID that
// already exists (first-party or third-party) returns false without overwriting.
func TestRegistry_Register_DuplicateBlocked(t *testing.T) {
	reg := NewRegistry(nil, nil)

	// "slack" is already seeded as a first-party integration.
	duplicate := Integration{
		ID:     "slack",
		Name:   "Imposter Slack",
		Events: []EventType{EventFlagEnabled},
		Status: StatusAvailable,
	}

	ok := reg.Register(duplicate)
	if ok {
		t.Error("Register() returned true for a duplicate ID; expected false")
	}

	// Original must be untouched.
	existing, _ := reg.Get("slack")
	if existing.Name != "Slack" {
		t.Errorf("original Name = %q after duplicate Register(); want %q", existing.Name, "Slack")
	}
}

// TestRegistry_GetAll verifies that List() returns all integrations (first-party +
// any third-party additions) without omissions.
func TestRegistry_GetAll(t *testing.T) {
	reg := NewRegistry(nil, nil)
	beforeCount := len(reg.List())

	reg.Register(Integration{
		ID:     "extra-1",
		Name:   "Extra 1",
		Events: []EventType{EventFlagArchived},
		Status: StatusAvailable,
	})
	reg.Register(Integration{
		ID:     "extra-2",
		Name:   "Extra 2",
		Events: []EventType{EventFlagArchived},
		Status: StatusAvailable,
	})

	all := reg.List()
	wantCount := beforeCount + 2
	if len(all) != wantCount {
		t.Errorf("List() len = %d after adding 2; want %d", len(all), wantCount)
	}
}

// TestRegistry_Install sets a webhook URL and transitions status to installed.
func TestRegistry_Install(t *testing.T) {
	reg := NewRegistry(nil, nil)

	webhookURL := "https://hooks.example.com/tombstone"
	cfg := map[string]string{"token": "abc123"}

	ok := reg.Install("slack", webhookURL, cfg)
	if !ok {
		t.Fatal("Install() returned false for known integration 'slack'")
	}

	got, _ := reg.Get("slack")
	if got.WebhookURL != webhookURL {
		t.Errorf("WebhookURL = %q after Install(); want %q", got.WebhookURL, webhookURL)
	}
	if got.Status != StatusInstalled {
		t.Errorf("Status = %q after Install(); want %q", got.Status, StatusInstalled)
	}
	if got.Config["token"] != "abc123" {
		t.Errorf("Config[token] = %q after Install(); want %q", got.Config["token"], "abc123")
	}
	// Name and other metadata must be preserved.
	if got.Name != "Slack" {
		t.Errorf("Name = %q after Install(); want %q", got.Name, "Slack")
	}
}

// TestRegistry_Install_UnknownID verifies Install() returns false for a
// non-existent integration ID.
func TestRegistry_Install_UnknownID(t *testing.T) {
	reg := NewRegistry(nil, nil)

	ok := reg.Install("does-not-exist", "https://example.com", nil)
	if ok {
		t.Error("Install() returned true for unknown ID; expected false")
	}
}

// TestRegistry_Uninstall clears WebhookURL / Config and resets status to available.
func TestRegistry_Uninstall(t *testing.T) {
	reg := NewRegistry(nil, nil)

	// Install first so there is something to uninstall.
	reg.Install("slack", "https://hooks.example.com/tombstone", map[string]string{"token": "tok"})

	ok := reg.Uninstall("slack")
	if !ok {
		t.Fatal("Uninstall() returned false for known integration 'slack'")
	}

	got, _ := reg.Get("slack")
	if got.WebhookURL != "" {
		t.Errorf("WebhookURL = %q after Uninstall(); want empty string", got.WebhookURL)
	}
	if got.Status != StatusAvailable {
		t.Errorf("Status = %q after Uninstall(); want %q", got.Status, StatusAvailable)
	}
	if got.Config != nil {
		t.Errorf("Config = %v after Uninstall(); want nil", got.Config)
	}
	// Name must survive the uninstall.
	if got.Name != "Slack" {
		t.Errorf("Name = %q after Uninstall(); want %q", got.Name, "Slack")
	}
}

// TestRegistry_Uninstall_UnknownID verifies Uninstall() returns false for a
// non-existent integration ID.
func TestRegistry_Uninstall_UnknownID(t *testing.T) {
	reg := NewRegistry(nil, nil)

	ok := reg.Uninstall("does-not-exist")
	if ok {
		t.Error("Uninstall() returned true for unknown ID; expected false")
	}
}

// TestRegistry_MarkBidirectional sets Bidirectional=true and records the inbound endpoint.
func TestRegistry_MarkBidirectional(t *testing.T) {
	reg := NewRegistry(nil, nil)

	// Register a plain integration without bidirectional support.
	reg.Register(Integration{
		ID:     "my-tool",
		Name:   "My Tool",
		Events: []EventType{EventFlagEnabled},
		Status: StatusAvailable,
	})

	endpoints := []string{"/api/v1/marketplace/inbound/my-tool", "/api/v1/marketplace/inbound/my-tool/alt"}
	ok := reg.MarkBidirectional("my-tool", endpoints)
	if !ok {
		t.Fatal("MarkBidirectional() returned false for known integration 'my-tool'")
	}

	got, _ := reg.Get("my-tool")
	if !got.Bidirectional {
		t.Error("Bidirectional = false after MarkBidirectional(); want true")
	}
	// Only the first endpoint should be stored in InboundEndpoint.
	if got.InboundEndpoint != endpoints[0] {
		t.Errorf("InboundEndpoint = %q; want %q", got.InboundEndpoint, endpoints[0])
	}
}

// TestRegistry_MarkBidirectional_UnknownID verifies MarkBidirectional() returns
// false for a non-existent integration ID.
func TestRegistry_MarkBidirectional_UnknownID(t *testing.T) {
	reg := NewRegistry(nil, nil)

	ok := reg.MarkBidirectional("ghost", []string{"/inbound/ghost"})
	if ok {
		t.Error("MarkBidirectional() returned true for unknown ID; expected false")
	}
}

// TestRegistry_MarkBidirectional_EmptyEndpoints verifies that passing an empty
// slice sets Bidirectional=true but leaves InboundEndpoint empty.
func TestRegistry_MarkBidirectional_EmptyEndpoints(t *testing.T) {
	reg := NewRegistry(nil, nil)

	reg.Register(Integration{
		ID:     "minimal",
		Name:   "Minimal",
		Events: []EventType{EventFlagEnabled},
		Status: StatusAvailable,
	})

	ok := reg.MarkBidirectional("minimal", nil)
	if !ok {
		t.Fatal("MarkBidirectional() returned false for known integration with nil endpoints")
	}

	got, _ := reg.Get("minimal")
	if !got.Bidirectional {
		t.Error("Bidirectional = false after MarkBidirectional(nil); want true")
	}
	if got.InboundEndpoint != "" {
		t.Errorf("InboundEndpoint = %q after nil endpoints; want empty string", got.InboundEndpoint)
	}
}

// TestRegistry_InstalledWebhooks verifies that only installed integrations
// subscribed to the queried event are returned.
func TestRegistry_InstalledWebhooks(t *testing.T) {
	reg := NewRegistry(nil, nil)

	// Install slack (subscribed to EventFlagEnabled).
	reg.Install("slack", "https://hooks.example.com/slack", nil)

	// pagerduty is NOT installed.

	// Register and install a custom integration subscribed to a different event only.
	reg.Register(Integration{
		ID:     "archive-tool",
		Name:   "Archive Tool",
		Events: []EventType{EventFlagArchived},
		Status: StatusAvailable,
	})
	reg.Install("archive-tool", "https://hooks.example.com/archive", nil)

	hits := reg.InstalledWebhooks(EventFlagEnabled)

	// Slack must appear (installed + subscribed to EventFlagEnabled).
	found := false
	for _, i := range hits {
		if i.ID == "slack" {
			found = true
		}
		if i.ID == "archive-tool" {
			t.Error("InstalledWebhooks(EventFlagEnabled) returned archive-tool; it is not subscribed to that event")
		}
		if i.Status != StatusInstalled {
			t.Errorf("InstalledWebhooks returned non-installed integration %q", i.ID)
		}
	}
	if !found {
		t.Error("InstalledWebhooks(EventFlagEnabled) did not include 'slack'")
	}
}

// TestEventFlagRecovery_IsDistinctFromEventFlagRollback closes a gap found
// by adversarial review: every catalog integration that subscribes to
// EventFlagRecovery already separately subscribes to EventFlagRollback
// too, so a behavioral InstalledWebhooks-based test alone (as
// TestRegistry_InstalledWebhooks_EventFlagRecovery below does) would still
// pass even if EventFlagRecovery were accidentally aliased to
// EventFlagRollback's exact value -- verified empirically: that exact
// aliasing was introduced by hand and the InstalledWebhooks test still
// passed. A direct value assertion is the only thing that actually proves
// the const values are distinct.
func TestEventFlagRecovery_IsDistinctFromEventFlagRollback(t *testing.T) {
	if EventFlagRecovery == EventFlagRollback {
		t.Fatal("EventFlagRecovery must not be aliased to EventFlagRollback -- they represent different real events")
	}
	if EventFlagRecovery != "flag.recovery" {
		t.Errorf("EventFlagRecovery = %q, want %q", EventFlagRecovery, "flag.recovery")
	}
}

// TestRegistry_InstalledWebhooks_EventFlagRecovery verifies the new
// EventFlagRecovery event type (EVAL-4's HALF_OPEN recovery ladder, the
// mirror image of EventFlagRollback) actually reaches slack once
// installed -- a typo in the const value or a missed catalog entry would
// compile fine but silently return zero results forever. Distinctness
// from EventFlagRollback is checked separately above, since every
// integration that lists EventFlagRecovery also lists EventFlagRollback,
// which would mask an aliasing bug from this test alone.
func TestRegistry_InstalledWebhooks_EventFlagRecovery(t *testing.T) {
	reg := NewRegistry(nil, nil)
	reg.Install("slack", "https://hooks.example.com/slack", nil)

	hits := reg.InstalledWebhooks(EventFlagRecovery)
	found := false
	for _, i := range hits {
		if i.ID == "slack" {
			found = true
		}
	}
	if !found {
		t.Error("InstalledWebhooks(EventFlagRecovery) did not include 'slack' -- check slack's Events list and the EventFlagRecovery const value")
	}
}

// TestRegistry_InstalledWebhooks_NoneInstalled verifies an empty slice is
// returned when no integrations are installed for the requested event.
func TestRegistry_InstalledWebhooks_NoneInstalled(t *testing.T) {
	reg := NewRegistry(nil, nil)

	// No installs — all integrations are in StatusAvailable.
	hits := reg.InstalledWebhooks(EventFlagKillSwitch)
	if len(hits) != 0 {
		t.Errorf("InstalledWebhooks returned %d results with nothing installed; want 0", len(hits))
	}
}

// TestRegistry_DatadogBidirectional verifies that the seeded datadog integration
// already has Bidirectional=true and a non-empty InboundEndpoint (defined in
// firstPartyIntegrations).
func TestRegistry_DatadogBidirectional(t *testing.T) {
	reg := NewRegistry(nil, nil)

	dd, ok := reg.Get("datadog")
	if !ok {
		t.Fatal("datadog first-party integration not found in registry")
	}
	if !dd.Bidirectional {
		t.Error("datadog seeded integration should have Bidirectional=true")
	}
	if dd.InboundEndpoint == "" {
		t.Error("datadog seeded integration should have a non-empty InboundEndpoint")
	}
}
