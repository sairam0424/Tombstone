package v1

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"

	"go.uber.org/zap"

	"github.com/sairam0424/Tombstone/services/marketplace/internal/integrations"
)

// HandleSlackCommands handles POST /api/v1/marketplace/slack/commands
// Receives Slack slash command payloads (application/x-www-form-urlencoded).
func (h *Handler) HandleSlackCommands(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	// Signature verification — only enforce when SLACK_SIGNING_SECRET is set.
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	signature := r.Header.Get("X-Slack-Signature")
	slackSecret := os.Getenv("SLACK_SIGNING_SECRET")
	if slackSecret != "" && !h.slackApp.VerifySignature(timestamp, string(rawBody), signature) {
		h.logger.Warn("slack commands: signature verification failed",
			zap.String("timestamp", timestamp))
		h.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid signature"})
		return
	}

	// Parse URL-encoded form body.
	form, err := url.ParseQuery(string(rawBody))
	if err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form data"})
		return
	}

	cmd := integrations.SlashCommand{
		Command:     form.Get("command"),
		Text:        form.Get("text"),
		UserID:      form.Get("user_id"),
		UserName:    form.Get("user_name"),
		ChannelID:   form.Get("channel_id"),
		ChannelName: form.Get("channel_name"),
		TeamID:      form.Get("team_id"),
		ResponseURL: form.Get("response_url"),
	}

	msg, err := h.slackApp.HandleSlashCommand(cmd)
	if err != nil {
		h.logger.Warn("slack command handler error", zap.Error(err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	h.writeJSON(w, http.StatusOK, msg)
}

// HandleSlackActions handles POST /api/v1/marketplace/slack/actions
// Receives Slack block action payloads (JSON in 'payload' form field).
func (h *Handler) HandleSlackActions(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	// Signature verification — only enforce when SLACK_SIGNING_SECRET is set.
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	signature := r.Header.Get("X-Slack-Signature")
	slackSecret := os.Getenv("SLACK_SIGNING_SECRET")
	if slackSecret != "" && !h.slackApp.VerifySignature(timestamp, string(rawBody), signature) {
		h.logger.Warn("slack actions: signature verification failed",
			zap.String("timestamp", timestamp))
		h.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid signature"})
		return
	}

	// Block actions arrive as URL-encoded body with a 'payload' field containing JSON.
	form, err := url.ParseQuery(string(rawBody))
	if err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form data"})
		return
	}

	// Parse the outer envelope to extract action details.
	var envelope struct {
		Type    string `json:"type"`
		Actions []struct {
			ActionID string `json:"action_id"`
			BlockID  string `json:"block_id"`
			Value    string `json:"value"`
		} `json:"actions"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		ResponseURL string `json:"response_url"`
	}
	if err := json.Unmarshal([]byte(form.Get("payload")), &envelope); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload JSON"})
		return
	}

	if len(envelope.Actions) == 0 {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no actions in payload"})
		return
	}

	first := envelope.Actions[0]
	action := integrations.BlockAction{
		ActionID:    first.ActionID,
		BlockID:     first.BlockID,
		Value:       first.Value,
		UserID:      envelope.User.ID,
		ChannelID:   envelope.Channel.ID,
		ResponseURL: envelope.ResponseURL,
	}

	if err := h.slackApp.HandleBlockAction(action); err != nil {
		h.logger.Warn("slack action handler error", zap.Error(err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Slack requires HTTP 200 for acknowledged block actions.
	w.WriteHeader(http.StatusOK)
}
