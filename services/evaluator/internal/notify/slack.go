// Package notify posts best-effort Slack notifications for evaluator
// events. EVAL-2: the circuit breaker's auto-rollback path (rollback.go)
// has always disabled the flag and published a Redis event, but never
// notified anyone -- a human on-call learns a flag was auto-rolled-back
// only by noticing the dashboard or the audit log, not proactively.
//
// This mirrors services/intelligence/app/integrations/slack.py's
// SlackNotifier.notify_rollback message shape exactly (same color, title,
// text layout, and "View in Dashboard" button) -- that Python notifier is
// real, working code, just never instantiated by anything in the repo
// (confirmed via grep: zero imports of SlackNotifier anywhere). Ported to
// Go here, rather than adding a cross-service HTTP hop through
// marketplace's TriggerEvent/Dispatcher (which remain unwired and out of
// scope for this slice), because the evaluator is where the circuit
// breaker's own trip event already fires.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"go.uber.org/zap"
)

// isAbsoluteURL reports whether s is a well-formed, scheme-and-host URL
// (e.g. "https://dashboard.example.com/flags/x"), as opposed to a bare
// relative path (e.g. "/flags/x") or an empty string. Only http/https are
// treated as usable -- Slack renders a button URL as a plain hyperlink,
// so any other scheme (or none) has no defined behavior in a Slack client.
func isAbsoluteURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || !u.IsAbs() {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// SlackNotifier posts to a Slack Incoming Webhook. Best-effort only: a
// failed or unconfigured webhook logs a warning and returns, it never
// blocks or retries the caller -- matches this codebase's existing
// fail-open telemetry/Rekor-submission philosophy for non-critical,
// third-party notification paths.
type SlackNotifier struct {
	webhookURL string
	httpClient *http.Client
	logger     *zap.Logger
}

// NewSlackNotifier constructs a notifier for the given webhook URL. An
// empty webhookURL is valid -- every notify call becomes a logged no-op,
// matching the Python SlackNotifier's own "not configured" behavior
// rather than requiring every deployment to set SLACK_WEBHOOK_URL.
func NewSlackNotifier(webhookURL string, logger *zap.Logger) *SlackNotifier {
	return &SlackNotifier{
		webhookURL: webhookURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		logger:     logger,
	}
}

// slackAttachment/slackAction/slackPayload mirror exactly the JSON shape
// services/intelligence/app/integrations/slack.py's notify_rollback builds
// -- same field names, same nesting, so this is a faithful Go port, not a
// reinterpretation.
type slackAction struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	URL   string `json:"url"`
	Style string `json:"style"`
}

type slackAttachment struct {
	Color    string        `json:"color"`
	Fallback string        `json:"fallback"`
	Title    string        `json:"title"`
	Text     string        `json:"text"`
	MrkdwnIn []string      `json:"mrkdwn_in"`
	Actions  []slackAction `json:"actions,omitempty"`
}

type slackPayload struct {
	Attachments []slackAttachment `json:"attachments"`
}

// NotifyRollback posts a red attachment announcing an auto-rollback event.
//
// errorRate is a FRACTION in [0,1] (matching circuit.Breaker.ErrorRate's
// own return convention), converted to a 0-100 percentage for display --
// NOT the same scale as the Python notify_rollback's own docstring, which
// documents its error_rate parameter as already 0-100. Passing this
// function's errorRate straight through unconverted would silently
// display "0.1%" for what is actually a 10% error rate.
func (n *SlackNotifier) NotifyRollback(
	ctx context.Context,
	flagKey, environment string,
	errorRate float64,
	triggeredBy, rollbackURL string,
) bool {
	if n.webhookURL == "" {
		n.logger.Warn("SLACK_WEBHOOK_URL not configured; skipping rollback notification",
			zap.String("flag", flagKey), zap.String("env", environment))
		return false
	}

	attachment := slackAttachment{
		Color: "#CC0000",
		Fallback: fmt.Sprintf(
			"Tombstone Auto-Rollback: %s disabled in %s", flagKey, environment,
		),
		Title: "Tombstone Auto-Rollback",
		Text: fmt.Sprintf(
			"Flag *%s* has been automatically disabled in *%s*.\n"+
				"Error rate: *%.1f%%*\nTriggered by: %s",
			flagKey, environment, errorRate*100, triggeredBy,
		),
		MrkdwnIn: []string{"text"},
	}
	// Slack's button "url" has no documented support for relative paths --
	// a client has no base to resolve one against, so a non-absolute
	// rollbackURL (e.g. DASHBOARD_URL unset upstream, producing a bare
	// "/flags/{key}") would ship a non-functional "View in Dashboard"
	// button while NotifyRollback still reports success (Slack's webhook
	// endpoint accepts any string in "url", it doesn't validate it's a
	// working link). Omitting the button entirely when there's no usable
	// destination is honest; a broken CTA on the alert whose whole point
	// is a one-click path to the incident is not.
	if isAbsoluteURL(rollbackURL) {
		attachment.Actions = []slackAction{
			{Type: "button", Text: "View in Dashboard", URL: rollbackURL, Style: "danger"},
		}
	}

	payload := slackPayload{Attachments: []slackAttachment{attachment}}

	body, err := json.Marshal(payload)
	if err != nil {
		n.logger.Warn("marshal slack rollback payload failed", zap.Error(err))
		return false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		n.logger.Warn("build slack webhook request failed", zap.Error(err))
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		n.logger.Warn("slack webhook request failed", zap.Error(err),
			zap.String("flag", flagKey), zap.String("env", environment))
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		n.logger.Warn("slack webhook returned an error status",
			zap.Int("status", resp.StatusCode),
			zap.String("flag", flagKey), zap.String("env", environment))
		return false
	}
	return true
}
