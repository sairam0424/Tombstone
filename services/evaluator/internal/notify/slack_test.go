package notify

import (
	"context"
	"encoding/json"
	"io"
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

	var gotRawBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		gotContentType = r.Header.Get("Content-Type")
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("read request body: %v", readErr)
		}
		gotRawBody = body
		if err := json.Unmarshal(body, &gotPayload); err != nil {
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

	// Decoding gotRawBody into slackPayload above (the exact same struct
	// NotifyRollback used to encode it) round-trips perfectly even if a
	// json tag were silently renamed (e.g. "mrkdwn_in" -> "mrkdwnIn") --
	// that regression wouldn't touch either side of the struct<->struct
	// round trip. Slack's real Incoming Webhook API only understands the
	// literal field names below, so assert against the raw wire bytes
	// directly, independent of this package's own struct definitions.
	var gotRaw map[string]any
	if err := json.Unmarshal(gotRawBody, &gotRaw); err != nil {
		t.Fatalf("decode raw body as generic JSON: %v", err)
	}
	rawAttachments, ok := gotRaw["attachments"].([]any)
	if !ok || len(rawAttachments) != 1 {
		t.Fatalf("raw JSON \"attachments\" = %#v, want a single-element array", gotRaw["attachments"])
	}
	rawAtt, ok := rawAttachments[0].(map[string]any)
	if !ok {
		t.Fatalf("raw JSON attachments[0] = %#v, want an object", rawAttachments[0])
	}
	for _, key := range []string{"color", "fallback", "title", "text", "mrkdwn_in", "actions"} {
		if _, present := rawAtt[key]; !present {
			t.Errorf("raw JSON attachment is missing key %q — Slack's webhook API only recognizes this exact field name", key)
		}
	}
	rawActions, ok := rawAtt["actions"].([]any)
	if !ok || len(rawActions) != 1 {
		t.Fatalf("raw JSON attachment \"actions\" = %#v, want a single-element array", rawAtt["actions"])
	}
	rawAction, ok := rawActions[0].(map[string]any)
	if !ok {
		t.Fatalf("raw JSON actions[0] = %#v, want an object", rawActions[0])
	}
	for _, key := range []string{"type", "text", "url", "style"} {
		if _, present := rawAction[key]; !present {
			t.Errorf("raw JSON action is missing key %q — Slack's webhook API only recognizes this exact field name", key)
		}
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

// TestNotifyRollback_MalformedWebhookURLReturnsFalse covers the
// http.NewRequestWithContext error branch (slack.go), distinct from the
// "unconfigured" (empty string, returns before ever building a request) and
// "unreachable" (syntactically valid URL, fails later at httpClient.Do)
// cases above -- a control character makes url.Parse itself reject the
// webhook URL, so request construction fails before any network I/O.
// Verified empirically: net/url rejects "\x00" in a URL with "invalid
// control character in URL".
func TestNotifyRollback_MalformedWebhookURLReturnsFalse(t *testing.T) {
	n := NewSlackNotifier("http://example.com/\x00", zap.NewNop())
	ok := n.NotifyRollback(context.Background(), "my-flag", "production", 0.05, "circuit_breaker", "https://dashboard.example.com/flags/my-flag")
	if ok {
		t.Error("NotifyRollback returned true for a malformed webhook URL, want false")
	}
}

// TestNotifyRollback_RelativeRollbackURLOmitsTheButton is the regression
// test for a real finding from adversarial review of PR #219: when
// DASHBOARD_URL is unset, cmd/main.go's shouldNotifySlack builds a bare
// relative rollbackURL like "/flags/my-flag". Slack's button "url" field
// has no documented support for relative paths -- shipping one would
// render a non-functional "View in Dashboard" CTA while NotifyRollback
// still returns true (Slack's webhook endpoint doesn't validate the URL
// is a working link). The fix omits the button entirely rather than
// shipping a broken one.
func TestNotifyRollback_RelativeRollbackURLOmitsTheButton(t *testing.T) {
	var gotPayload slackPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewSlackNotifier(srv.URL, zap.NewNop())
	ok := n.NotifyRollback(context.Background(), "my-flag", "production", 0.05, "circuit_breaker", "/flags/my-flag")
	if !ok {
		t.Fatal("NotifyRollback returned false, want true -- a missing button must not fail the whole alert")
	}
	if len(gotPayload.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(gotPayload.Attachments))
	}
	if actions := gotPayload.Attachments[0].Actions; len(actions) != 0 {
		t.Errorf("actions = %+v, want none for a relative rollbackURL (button would be non-functional in a Slack client)", actions)
	}
}

// TestNotifyRollback_EmptyRollbackURLOmitsTheButton covers the other
// no-usable-destination case: an empty string (e.g. DASHBOARD_URL unset
// AND some future caller stops building even a relative fallback).
func TestNotifyRollback_EmptyRollbackURLOmitsTheButton(t *testing.T) {
	var gotPayload slackPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewSlackNotifier(srv.URL, zap.NewNop())
	n.NotifyRollback(context.Background(), "my-flag", "production", 0.05, "circuit_breaker", "")
	if actions := gotPayload.Attachments[0].Actions; len(actions) != 0 {
		t.Errorf("actions = %+v, want none for an empty rollbackURL", actions)
	}
}

// TestIsAbsoluteURL pins the exact boundary NotifyRollback relies on to
// decide whether a rollbackURL is safe to render as a Slack button.
func TestIsAbsoluteURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://dashboard.example.com/flags/my-flag", true},
		{"http://localhost:3000/flags/my-flag", true},
		{"/flags/my-flag", false},
		{"", false},
		{"flags/my-flag", false},
		{"javascript:alert(1)", false},
		{"ftp://example.com/flags/my-flag", false},
	}
	for _, tc := range tests {
		if got := isAbsoluteURL(tc.url); got != tc.want {
			t.Errorf("isAbsoluteURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}
