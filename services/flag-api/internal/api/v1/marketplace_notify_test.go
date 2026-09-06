package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/db"
	"github.com/tombstone/flag-api/internal/secrets"
)

// receivedMarketplaceEvent is the subset of notifyMarketplace's real POST
// body a test cares about.
type receivedMarketplaceEvent struct {
	EventType   string         `json:"event_type"`
	FlagKey     string         `json:"flag_key"`
	Environment string         `json:"environment"`
	Actor       string         `json:"actor"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// newFakeMarketplace returns a test server that decodes every POST
// /api/v1/marketplace/events body onto the returned channel, and the
// channel a test should read from -- notifyMarketplace dispatches on its
// own goroutine (see its doc comment), so a test must wait for delivery
// rather than asserting synchronously right after the call returns.
func newFakeMarketplace(t *testing.T) (*httptest.Server, chan receivedMarketplaceEvent) {
	t.Helper()
	events := make(chan receivedMarketplaceEvent, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/marketplace/events" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var e receivedMarketplaceEvent
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Errorf("decode event body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		events <- e
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	return srv, events
}

func awaitMarketplaceEvent(t *testing.T, events chan receivedMarketplaceEvent) receivedMarketplaceEvent {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notifyMarketplace's async delivery")
		return receivedMarketplaceEvent{}
	}
}

func assertNoMarketplaceEvent(t *testing.T, events chan receivedMarketplaceEvent) {
	t.Helper()
	select {
	case e := <-events:
		t.Fatalf("expected no marketplace event, got %+v", e)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestNotifyMarketplace_NoOpWithoutURL proves notifyMarketplace is a
// permanent no-op (never even attempts an HTTP call) when marketplaceURL
// is unset -- MARKETPLACE_URL's own fail-open contract.
func TestNotifyMarketplace_NoOpWithoutURL(t *testing.T) {
	srv, events := newFakeMarketplace(t)
	_ = srv // deliberately never referenced by marketplaceURL below

	h := &FlagHandler{logger: zap.NewNop(), marketplaceURL: "", marketplaceHTTPClient: &http.Client{}}
	h.notifyMarketplace(marketplaceEventFlagCreated, "some-flag", "", "actor", nil)

	assertNoMarketplaceEvent(t, events)
}

// TestNotifyMarketplace_PostsRealEventShape proves the real wire shape
// notifyMarketplace sends matches what marketplace's TriggerEvent handler
// (services/marketplace/internal/api/v1/handlers.go) actually decodes.
func TestNotifyMarketplace_PostsRealEventShape(t *testing.T) {
	srv, events := newFakeMarketplace(t)

	h := &FlagHandler{logger: zap.NewNop(), marketplaceURL: srv.URL, marketplaceHTTPClient: &http.Client{}}
	h.notifyMarketplace(marketplaceEventFlagKillSwitch, "checkout-v2", "production", "sre-oncall",
		map[string]any{"reason": "circuit_breaker"})

	e := awaitMarketplaceEvent(t, events)
	if e.EventType != marketplaceEventFlagKillSwitch {
		t.Errorf("EventType = %q, want %q", e.EventType, marketplaceEventFlagKillSwitch)
	}
	if e.FlagKey != "checkout-v2" || e.Environment != "production" || e.Actor != "sre-oncall" {
		t.Errorf("unexpected event fields: %+v", e)
	}
	if e.Metadata["reason"] != "circuit_breaker" {
		t.Errorf("Metadata[reason] = %v, want circuit_breaker", e.Metadata["reason"])
	}
}

// TestFlagLifecycleNotifiesMarketplace is the end-to-end regression test:
// before this PR, marketplace's fully-built dispatcher/registry had no
// real trigger anywhere in flag-api, so every configured integration
// silently never fired from a real flag change. Runs against a real
// Postgres in the flag-api-migrations CI job, skips locally otherwise,
// matching TestRollbackStep's own convention.
func TestFlagLifecycleNotifiesMarketplace(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed marketplace-notify test")
	}

	database, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := db.Migrate(ctx, database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	projectID := createTestProject(ctx, t, database, "marketplace-notify-test")

	logger := zap.NewNop()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	auditKey, err := secrets.NewAuditKey("marketplace-notify-test-key-0000000", "")
	if err != nil {
		t.Fatalf("audit key: %v", err)
	}
	auditW := audit.NewWriter(database, auditKey)

	srv, events := newFakeMarketplace(t)
	flagH := NewFlagHandler(database, rdb, logger, nil, auditW, nil, srv.URL)

	const flagKey = "notify-test-flag"

	t.Run("CreateFlag fires flag.created", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags", map[string]any{
			"key": flagKey, "name": "Notify Test", "flag_type": "BOOLEAN",
		}, projectID, nil)
		w := httptest.NewRecorder()
		flagH.CreateFlag(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateFlag status = %d, body: %s", w.Code, w.Body.String())
		}
		e := awaitMarketplaceEvent(t, events)
		if e.EventType != marketplaceEventFlagCreated || e.FlagKey != flagKey {
			t.Errorf("unexpected event: %+v", e)
		}
	})

	t.Run("UpdateEnvironment enabling fires flag.enabled, re-set to enabled=true is a no-op", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodPatch, "/api/v1/flags/"+flagKey+"/environments/production",
			map[string]any{"enabled": true, "rollout_pct": 50}, projectID, map[string]string{"key": flagKey, "env": "production"})
		w := httptest.NewRecorder()
		flagH.UpdateEnvironment(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("UpdateEnvironment status = %d, body: %s", w.Code, w.Body.String())
		}
		e := awaitMarketplaceEvent(t, events)
		if e.EventType != marketplaceEventFlagEnabled || e.FlagKey != flagKey {
			t.Errorf("unexpected event: %+v", e)
		}

		// A second update that keeps enabled=true (only rollout_pct moves)
		// must NOT fire another flag.enabled -- nothing about "enabled"
		// actually transitioned.
		req2 := newTenancyRequest(t, http.MethodPatch, "/api/v1/flags/"+flagKey+"/environments/production",
			map[string]any{"enabled": true, "rollout_pct": 75}, projectID, map[string]string{"key": flagKey, "env": "production"})
		w2 := httptest.NewRecorder()
		flagH.UpdateEnvironment(w2, req2)
		if w2.Code != http.StatusOK {
			t.Fatalf("second UpdateEnvironment status = %d, body: %s", w2.Code, w2.Body.String())
		}
		assertNoMarketplaceEvent(t, events)
	})

	t.Run("KillSwitch fires flag.kill_switch", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/"+flagKey+"/kill",
			map[string]any{"environment": "production", "reason": "test"}, projectID, map[string]string{"key": flagKey})
		w := httptest.NewRecorder()
		flagH.KillSwitch(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("KillSwitch status = %d, body: %s", w.Code, w.Body.String())
		}
		e := awaitMarketplaceEvent(t, events)
		if e.EventType != marketplaceEventFlagKillSwitch || e.FlagKey != flagKey {
			t.Errorf("unexpected event: %+v", e)
		}
	})

	t.Run("ArchiveFlag fires flag.archived exactly once, not once per environment", func(t *testing.T) {
		req := newTenancyRequest(t, http.MethodDelete, "/api/v1/flags/"+flagKey, nil, projectID, map[string]string{"key": flagKey})
		w := httptest.NewRecorder()
		flagH.ArchiveFlag(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("ArchiveFlag status = %d, body: %s", w.Code, w.Body.String())
		}
		e := awaitMarketplaceEvent(t, events)
		if e.EventType != marketplaceEventFlagArchived || e.FlagKey != flagKey {
			t.Errorf("unexpected event: %+v", e)
		}
		// This flag has 3 default environments (development/staging/
		// production) -- if notifyMarketplace were called inside the
		// per-environment loop (matching the intelligence-eviction call
		// right below it) rather than once before it, this would see 2
		// more archived events here.
		assertNoMarketplaceEvent(t, events)
	})
}
