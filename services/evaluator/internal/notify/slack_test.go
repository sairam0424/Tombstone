package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
)

// TestNotifyRollback_PostsExpectedPayload verifies the wire shape sent to
// Slack matches services/intelligence/app/integrations/slack.py's
// notify_rollback exactly (color, title, text layout, action button) --
// this Go port's entire reason for existing is to be a faithful mirror of
// that message, not a reinterpretation.
func TestNotifyRollback_PostsExpectedPayload(t *testing.T) {
	var called atomic.Int32
	var gotContentType string
	var gotPayload slackPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewSlackNotifier(srv.URL, zap.NewNop())
	ok := n.NotifyRollback(context.Background(), "my-flag", "production", 0.12, "circuit_breaker", "https://dashboard.example.com/flags/my-flag")

	if !ok {
		t.Fatal("NotifyRollback returned false, want true on 2xx response")
	}
	if called.Load() != 1 {
		t.Fatalf("webhook called %d times, want 1", called.Load())
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}

	if len(gotPayload.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(gotPayload.Attachments))
	}
	att := gotPayload.Attachments[0]
	if att.Color != "#CC0000" {
		t.Errorf("color = %q, want #CC0000", att.Color)
	}
	if att.Title != "Tombstone Auto-Rollback" {
		t.Errorf("title = %q, want %q", att.Title, "Tombstone Auto-Rollback")
	}
	wantFallback := "Tombstone Auto-Rollback: my-flag disabled in production"
	if att.Fallback != wantFallback {
		t.Errorf("fallback = %q, want %q", att.Fallback, wantFallback)
	}
	// errorRate is a FRACTION (0.12 == 12%) -- the Go docstring explicitly
	// calls out that this differs from the Python docstring's own
	// documented 0-100 scale for the same parameter name. A regression
	// here (displaying "0.1%" instead of "12.0%") would silently
	// understate every incident's severity by ~100x.
	wantText := "Flag *my-flag* has been automatically disabled in *production*.\n" +
		"Error rate: *12.0%*\nTriggered by: circuit_breaker"
	if att.Text != wantText {
		t.Errorf("text = %q, want %q", att.Text, wantText)
	}
	if len(att.Actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(att.Actions))
	}
	action := att.Actions[0]
	if action.URL != "https://dashboard.example.com/flags/my-flag" {
		t.Errorf("action URL = %q, want the rollback URL passed in", action.URL)
	}
	if action.Style != "danger" {
		t.Errorf("action style = %q, want danger", action.Style)
	}
}

// TestNotifyRollback_UnconfiguredWebhookIsANoOp verifies an empty
// webhookURL is treated as valid config (matching the Python
// SlackNotifier's own "not configured" behavior) -- it must return false
// without attempting any network call, not panic or block.
func TestNotifyRollback_UnconfiguredWebhookIsANoOp(t *testing.T) {
	n := NewSlackNotifier("", zap.NewNop())
	ok := n.NotifyRollback(context.Background(), "my-flag", "production", 0.05, "circuit_breaker", "https://dashboard.example.com/flags/my-flag")
	if ok {
		t.Error("NotifyRollback returned true with no webhook configured, want false")
	}
}

// TestNotifyRollback_NonSuccessStatusReturnsFalse verifies a non-2xx
// response from Slack is treated as failure -- best-effort, not fatal to
// the caller (the circuit-breaker rollback path itself must never block
// or fail because Slack is down).
func TestNotifyRollback_NonSuccessStatusReturnsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewSlackNotifier(srv.URL, zap.NewNop())
	ok := n.NotifyRollback(context.Background(), "my-flag", "production", 0.05, "circuit_breaker", "https://dashboard.example.com/flags/my-flag")
	if ok {
		t.Error("NotifyRollback returned true on HTTP 500, want false")
	}
}

// TestNotifyRollback_UnreachableWebhookReturnsFalse verifies a network
// error (webhook host down/unreachable) is swallowed as a false return,
// never a panic -- matches this codebase's existing fail-open convention
// for best-effort third-party notification paths (e.g. Rekor submission).
func TestNotifyRollback_UnreachableWebhookReturnsFalse(t *testing.T) {
	n := NewSlackNotifier("http://127.0.0.1:0", zap.NewNop())
	ok := n.NotifyRollback(context.Background(), "my-flag", "production", 0.05, "circuit_breaker", "https://dashboard.example.com/flags/my-flag")
	if ok {
		t.Error("NotifyRollback returned true against an unreachable webhook, want false")
	}
}
