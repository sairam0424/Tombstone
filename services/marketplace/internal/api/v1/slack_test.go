package v1_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	v1 "github.com/sairam0424/Tombstone/services/marketplace/internal/api/v1"
	"github.com/sairam0424/Tombstone/services/marketplace/internal/integrations"
	"github.com/sairam0424/Tombstone/services/marketplace/internal/registry"
	"github.com/sairam0424/Tombstone/services/marketplace/internal/webhook"
)

// newTestHandler builds a Handler wired with a real SlackApp (botToken/signingSecret
// can be empty for unit tests that bypass signature verification).
func newTestHandler(flagAPIURL string) *v1.Handler {
	reg := registry.NewRegistry(nil, zap.NewNop())
	dispatcher := webhook.NewDispatcher(reg, zap.NewNop())
	h := v1.NewHandler(reg, dispatcher, zap.NewNop())
	slackApp := integrations.NewSlackApp("", "", flagAPIURL)
	h.SetSlackApp(slackApp)
	return h
}

// TestHandleSlackCommands_NoSecret_HelpCommand verifies that when SLACK_SIGNING_SECRET
// is not set, a valid slash command form body returns HTTP 200 and a JSON body.
func TestHandleSlackCommands_NoSecret_HelpCommand(t *testing.T) {
	t.Setenv("SLACK_SIGNING_SECRET", "")

	h := newTestHandler("http://localhost:8081")

	body := "command=%2Ftombstone&text=&user_id=U123&user_name=alice&channel_id=C123&channel_name=general&team_id=T123&response_url=https%3A%2F%2Fhooks.slack.com%2Fx"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/slack/commands", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleSlackCommands(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleSlackCommands() status = %d; want %d", w.Code, http.StatusOK)
	}

	var msg map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &msg); err != nil {
		t.Fatalf("response body is not valid JSON: %v — body: %s", err, w.Body.String())
	}
}

// TestHandleSlackCommands_NoSecret_StatusCommand verifies that /tombstone status <key>
// falls through to SlackApp.HandleSlashCommand without a 401 when no signing secret is set.
func TestHandleSlackCommands_NoSecret_StatusCommand(t *testing.T) {
	t.Setenv("SLACK_SIGNING_SECRET", "")

	// Serve a minimal flag-api stub so statusCommand can receive a 200 response.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"checkout-v2","state":true,"rollout_pct":50,"environment":"production","owner":"payments-team"}`))
	}))
	defer stub.Close()

	h := newTestHandler(stub.URL)

	body := "command=%2Ftombstone&text=status+checkout-v2&user_id=U123&channel_id=C123&response_url=https%3A%2F%2Fhooks.slack.com%2Fx"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/slack/commands", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleSlackCommands(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleSlackCommands() status = %d; want %d", w.Code, http.StatusOK)
	}
}

// TestHandleSlackCommands_SignatureRequired_MissingHeaders verifies that when
// SLACK_SIGNING_SECRET is set but the request carries no signature headers, the
// handler returns HTTP 401.
func TestHandleSlackCommands_SignatureRequired_MissingHeaders(t *testing.T) {
	t.Setenv("SLACK_SIGNING_SECRET", "test-secret-value")

	h := newTestHandler("http://localhost:8081")

	body := "command=%2Ftombstone&text=status+checkout-v2&user_id=U123&channel_id=C123"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/slack/commands", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Deliberately omit X-Slack-Request-Timestamp and X-Slack-Signature.
	w := httptest.NewRecorder()

	h.HandleSlackCommands(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("HandleSlackCommands() without signature: status = %d; want %d", w.Code, http.StatusUnauthorized)
	}
}

// TestHandleSlackActions_InvalidPayload verifies that a malformed 'payload' JSON
// field returns HTTP 400.
func TestHandleSlackActions_InvalidPayload(t *testing.T) {
	t.Setenv("SLACK_SIGNING_SECRET", "")

	h := newTestHandler("http://localhost:8081")

	body := "payload=not-valid-json"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/slack/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleSlackActions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleSlackActions() with invalid JSON: status = %d; want %d", w.Code, http.StatusBadRequest)
	}
}

// TestHandleSlackActions_DismissAction verifies that a well-formed dismiss block
// action is accepted with HTTP 200 and does not return a server error.
// Note: the SlackApp.postResponse will fail because response_url is invalid,
// but HandleSlackActions itself should return 200 regardless of the post result.
func TestHandleSlackActions_DismissAction(t *testing.T) {
	t.Setenv("SLACK_SIGNING_SECRET", "")

	// Serve a response_url stub so postResponse does not error on an unreachable host.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	h := newTestHandler("http://localhost:8081")

	envelope := map[string]any{
		"type": "block_actions",
		"actions": []any{
			map[string]any{
				"action_id": "dismiss",
				"block_id":  "b1",
				"value":     "",
			},
		},
		"user":         map[string]any{"id": "U123"},
		"channel":      map[string]any{"id": "C123"},
		"response_url": stub.URL,
	}
	payloadBytes, _ := json.Marshal(envelope)

	formBody := "payload=" + strings.ReplaceAll(string(payloadBytes), "+", "%2B")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/slack/actions", strings.NewReader(formBody))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleSlackActions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleSlackActions() dismiss: status = %d; want %d", w.Code, http.StatusOK)
	}
}

// TestHandleSlackActions_EmptyActions verifies that a payload with no actions
// returns HTTP 400.
func TestHandleSlackActions_EmptyActions(t *testing.T) {
	t.Setenv("SLACK_SIGNING_SECRET", "")

	h := newTestHandler("http://localhost:8081")

	envelope := map[string]any{
		"type":         "block_actions",
		"actions":      []any{},
		"user":         map[string]any{"id": "U123"},
		"channel":      map[string]any{"id": "C123"},
		"response_url": "https://hooks.slack.com/x",
	}
	payloadBytes, _ := json.Marshal(envelope)
	formBody := "payload=" + strings.ReplaceAll(string(payloadBytes), "+", "%2B")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/slack/actions", strings.NewReader(formBody))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleSlackActions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleSlackActions() empty actions: status = %d; want %d", w.Code, http.StatusBadRequest)
	}
}
