package webhook

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/tombstone/marketplace/internal/registry"
)

// TestDeliver_FlakyEndpoint_RetriesUntilSuccess simulates a receiving webhook
// endpoint that fails on its first two attempts and succeeds on the third —
// exactly the scenario retry was added to handle. It asserts:
//   - the delivery eventually succeeds via the retry path (attempts == 3,
//     final call observed a 2xx),
//   - the SAME Idempotency-Key header value is sent on every attempt for the
//     same logical event (a fresh random key per retry would defeat the
//     entire purpose of the header).
func TestDeliver_FlakyEndpoint_RetriesUntilSuccess(t *testing.T) {
	var attempts int32
	var mu sync.Mutex
	var seenKeys []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)

		mu.Lock()
		seenKeys = append(seenKeys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()

		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := registry.NewRegistry(nil, zap.NewNop())
	d := NewDispatcher(reg, zap.NewNop())

	integration := registry.Integration{ID: "flaky-integration", WebhookURL: srv.URL}
	event := FlagEvent{
		EventType:   registry.EventFlagKillSwitch,
		FlagKey:     "checkout-v2",
		Environment: "production",
		Actor:       "test-actor",
		Ts:          1_700_000_000_000,
	}

	// Call deliver synchronously (bypassing Dispatch's goroutine fan-out) so
	// the assertions below run only after delivery has fully completed.
	d.deliver(t.Context(), integration, event)

	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("expected exactly 3 attempts (2 failures + 1 success), got %d", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenKeys) != 3 {
		t.Fatalf("expected 3 recorded Idempotency-Key headers, got %d", len(seenKeys))
	}
	for _, k := range seenKeys {
		if k == "" {
			t.Fatal("Idempotency-Key header was empty on at least one attempt")
		}
	}
	first := seenKeys[0]
	for i, k := range seenKeys {
		if k != first {
			t.Fatalf("attempt %d sent a different Idempotency-Key (%q) than attempt 0 (%q) — "+
				"retries of the same logical delivery must reuse the same key", i, k, first)
		}
	}
}

// TestIdempotencyKey_SameEventSameIntegration_IsDeterministic verifies the
// key is stable across repeated calls for the same (event, integration)
// pair — i.e. it is derived from the event/integration content, not
// generated fresh each call.
func TestIdempotencyKey_SameEventSameIntegration_IsDeterministic(t *testing.T) {
	event := FlagEvent{
		EventType:   registry.EventFlagKillSwitch,
		FlagKey:     "checkout-v2",
		Environment: "production",
		Actor:       "alice",
		Ts:          1_700_000_000_000,
	}

	k1 := idempotencyKey(event, "pagerduty")
	k2 := idempotencyKey(event, "pagerduty")
	if k1 != k2 {
		t.Fatalf("idempotencyKey is not deterministic: got %q then %q for identical input", k1, k2)
	}
	if k1 == "" {
		t.Fatal("idempotencyKey returned an empty string")
	}
}

// TestIdempotencyKey_DifferentLogicalEvents_ProduceDifferentKeys verifies
// that two distinct logical events (different flag key, different
// environment) delivered to the same integration get different keys, so
// unrelated events are never accidentally collapsed by a receiver's dedup
// logic.
func TestIdempotencyKey_DifferentLogicalEvents_ProduceDifferentKeys(t *testing.T) {
	base := FlagEvent{
		EventType:   registry.EventFlagKillSwitch,
		FlagKey:     "checkout-v2",
		Environment: "production",
		Actor:       "alice",
		Ts:          1_700_000_000_000,
	}

	differentFlag := base
	differentFlag.FlagKey = "checkout-v3"

	differentEnv := base
	differentEnv.Environment = "staging"

	baseKey := idempotencyKey(base, "pagerduty")
	if k := idempotencyKey(differentFlag, "pagerduty"); k == baseKey {
		t.Fatalf("events with different FlagKey produced the same idempotency key: %q", k)
	}
	if k := idempotencyKey(differentEnv, "pagerduty"); k == baseKey {
		t.Fatalf("events with different Environment produced the same idempotency key: %q", k)
	}
}

// TestIdempotencyKey_SameEventDifferentIntegration_ProducesDifferentKeys
// verifies that the same logical event fanned out to two different
// integrations gets a distinct key per integration — each integration's
// delivery is dedup-scoped independently.
func TestIdempotencyKey_SameEventDifferentIntegration_ProducesDifferentKeys(t *testing.T) {
	event := FlagEvent{
		EventType:   registry.EventFlagKillSwitch,
		FlagKey:     "checkout-v2",
		Environment: "production",
		Actor:       "alice",
		Ts:          1_700_000_000_000,
	}

	pagerdutyKey := idempotencyKey(event, "pagerduty")
	opsgenieKey := idempotencyKey(event, "opsgenie")
	if pagerdutyKey == opsgenieKey {
		t.Fatalf("same event delivered to two different integrations produced the same key: %q", pagerdutyKey)
	}
}

// TestClientFor_ReturnsSameInstancePerIntegration_DifferentAcrossIntegrations
// verifies the per-integration circuit-breaker design: repeated calls for
// the same integration ID return the identical *httpclient.ResilientClient
// (so breaker state accumulates across deliveries), while different
// integration IDs get distinct clients (so one integration's outage cannot
// trip the breaker for a different, healthy integration).
func TestClientFor_ReturnsSameInstancePerIntegration_DifferentAcrossIntegrations(t *testing.T) {
	reg := registry.NewRegistry(nil, zap.NewNop())
	d := NewDispatcher(reg, zap.NewNop())

	a1 := d.clientFor("pagerduty")
	a2 := d.clientFor("pagerduty")
	if a1 != a2 {
		t.Fatal("clientFor returned different instances for the same integration ID across calls")
	}

	b1 := d.clientFor("opsgenie")
	if a1 == b1 {
		t.Fatal("clientFor returned the same instance for two different integration IDs")
	}
}
