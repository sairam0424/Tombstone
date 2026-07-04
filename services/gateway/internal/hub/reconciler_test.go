package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fakeFlagAPI serves a canned snapshot JSON body for a single environment,
// swappable between calls via a pointer so tests can simulate drift between
// polls without a real flag-api.
func fakeFlagAPI(t *testing.T, body *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("expected Authorization: Bearer test-token, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(*body))
	}))
}

func snapshotJSON(t *testing.T, snap snapshotResponse) string {
	t.Helper()
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return string(b)
}

// TestReconciler_IdenticalSnapshots_DoesNotBroadcast verifies that when two
// consecutive polls return byte-identical snapshots, no drift is detected
// and nothing is broadcast to SSE clients.
func TestReconciler_IdenticalSnapshots_DoesNotBroadcast(t *testing.T) {
	const env = "production"
	snap := snapshotResponse{
		Environment: env,
		Flags: []snapshotFlag{
			{FlagKey: "checkout-v2", Environment: env, Enabled: true, RolloutPct: 50, UpdatedAt: 1000},
		},
		Hash: "abc123",
		Ts:   1000,
	}
	body := snapshotJSON(t, snap)
	srv := fakeFlagAPI(t, &body)
	defer srv.Close()

	h := NewHub(zap.NewNop())
	r := NewReconciler(h, srv.URL, "test-token", zap.NewNop())

	// Subscribe a client so we can observe whether anything is broadcast.
	ch := h.Subscribe(env, "client-1")
	defer h.Unsubscribe(env, "client-1", ch)

	ctx := context.Background()

	// First poll: no previous snapshot recorded yet — must not broadcast.
	r.reconcileOne(ctx, env)
	assertNoFrame(t, ch, 100*time.Millisecond, "first poll (no prior snapshot)")

	// Second poll with an identical body — must not broadcast either.
	r.reconcileOne(ctx, env)
	assertNoFrame(t, ch, 100*time.Millisecond, "second identical poll")
}

// TestReconciler_DriftedSnapshot_Broadcasts verifies that when a flag's
// state differs from the hub's last-known snapshot, the reconciler
// rebroadcasts it via hub.Broadcast so connected SSE clients converge.
func TestReconciler_DriftedSnapshot_Broadcasts(t *testing.T) {
	const env = "production"
	first := snapshotResponse{
		Environment: env,
		Flags: []snapshotFlag{
			{FlagKey: "checkout-v2", Environment: env, Enabled: false, RolloutPct: 0, UpdatedAt: 1000},
		},
		Hash: "hash-1",
		Ts:   1000,
	}
	firstBody := snapshotJSON(t, first)
	body := firstBody
	srv := fakeFlagAPI(t, &body)
	defer srv.Close()

	h := NewHub(zap.NewNop())
	r := NewReconciler(h, srv.URL, "test-token", zap.NewNop())

	ch := h.Subscribe(env, "client-1")
	defer h.Unsubscribe(env, "client-1", ch)

	ctx := context.Background()

	// First poll establishes the baseline — no broadcast expected.
	r.reconcileOne(ctx, env)
	assertNoFrame(t, ch, 100*time.Millisecond, "baseline poll")

	// Simulate a missed Redis event: flag-api's true state has since changed
	// (enabled=true, rollout=100) but the hub never heard about it via pub/sub
	// or the stream consumer.
	second := snapshotResponse{
		Environment: env,
		Flags: []snapshotFlag{
			{FlagKey: "checkout-v2", Environment: env, Enabled: true, RolloutPct: 100, UpdatedAt: 2000},
		},
		Hash: "hash-2",
		Ts:   2000,
	}
	body = snapshotJSON(t, second)

	r.reconcileOne(ctx, env)

	select {
	case frame := <-ch:
		s := string(frame)
		if !strings.Contains(s, `"checkout-v2"`) {
			t.Errorf("expected broadcast frame to reference the drifted flag, got: %s", s)
		}
		if !strings.Contains(s, `"enabled":true`) {
			t.Errorf("expected broadcast frame to carry the NEW enabled=true state, got: %s", s)
		}
	case <-time.After(time.Second):
		t.Fatal("expected reconciler to broadcast on detected drift, got nothing")
	}
}

// TestReconciler_NewFlagAppearsInSnapshot_Broadcasts verifies that a flag
// present in the new snapshot but absent from the previous one (e.g. created
// while its CreateFlag event was lost) is treated as drift and broadcast.
func TestReconciler_NewFlagAppearsInSnapshot_Broadcasts(t *testing.T) {
	const env = "production"
	first := snapshotResponse{
		Environment: env,
		Flags:       []snapshotFlag{},
		Hash:        "empty",
		Ts:          1000,
	}
	body := snapshotJSON(t, first)
	srv := fakeFlagAPI(t, &body)
	defer srv.Close()

	h := NewHub(zap.NewNop())
	r := NewReconciler(h, srv.URL, "test-token", zap.NewNop())

	ch := h.Subscribe(env, "client-1")
	defer h.Unsubscribe(env, "client-1", ch)

	ctx := context.Background()
	r.reconcileOne(ctx, env) // baseline, empty
	assertNoFrame(t, ch, 100*time.Millisecond, "baseline poll")

	second := snapshotResponse{
		Environment: env,
		Flags: []snapshotFlag{
			{FlagKey: "new-flag", Environment: env, Enabled: true, RolloutPct: 10, UpdatedAt: 2000},
		},
		Hash: "with-new-flag",
		Ts:   2000,
	}
	body = snapshotJSON(t, second)

	r.reconcileOne(ctx, env)

	select {
	case frame := <-ch:
		if !strings.Contains(string(frame), `"new-flag"`) {
			t.Errorf("expected broadcast for newly-appeared flag, got: %s", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("expected reconciler to broadcast for a flag missing from the prior snapshot")
	}
}

func assertNoFrame(t *testing.T, ch chan []byte, wait time.Duration, when string) {
	t.Helper()
	select {
	case frame := <-ch:
		t.Fatalf("%s: expected no broadcast, but got frame: %s", when, frame)
	case <-time.After(wait):
		// expected — no broadcast
	}
}

