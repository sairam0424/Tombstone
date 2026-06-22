package v1

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.uber.org/zap"

	"github.com/tombstone/marketplace/internal/integrations"
)

// InboundHandler handles inbound requests from Slack (slash commands + block actions).
type InboundHandler struct {
	slack  *integrations.SlackApp
	logger *zap.Logger
}

// NewInboundHandler constructs an InboundHandler.
func NewInboundHandler(slack *integrations.SlackApp, logger *zap.Logger) *InboundHandler {
	return &InboundHandler{
		slack:  slack,
		logger: logger,
	}
}

// HandleSlashCommand handles POST /api/v1/marketplace/slack/commands.
//
// Slack slash command requests are URL-encoded form bodies. This handler:
//  1. Reads the full body for signature verification.
//  2. Verifies the HMAC-SHA256 Slack signature (timing-safe).
//  3. Parses the slash command fields.
//  4. Responds with 200 + acknowledgement JSON immediately.
//  5. Dispatches complex work to a goroutine that posts a delayed_response.
func (h *InboundHandler) HandleSlashCommand(w http.ResponseWriter, r *http.Request) {
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	signature := r.Header.Get("X-Slack-Signature")

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.Error("slack/commands: read body", zap.Error(err))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if !h.slack.VerifySignature(timestamp, string(rawBody), signature) {
		h.logger.Warn("slack/commands: invalid signature",
			zap.String("timestamp", timestamp),
			zap.String("signature", signature),
		)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Parse URL-encoded form body.
	values, err := url.ParseQuery(string(rawBody))
	if err != nil {
		http.Error(w, "bad form body", http.StatusBadRequest)
		return
	}

	cmd := integrations.SlashCommand{
		Command:     values.Get("command"),
		Text:        values.Get("text"),
		UserID:      values.Get("user_id"),
		UserName:    values.Get("user_name"),
		ChannelID:   values.Get("channel_id"),
		ChannelName: values.Get("channel_name"),
		TeamID:      values.Get("team_id"),
		ResponseURL: values.Get("response_url"),
	}

	// Determine if we need async processing (list/search may be slow).
	fields := strings.Fields(strings.TrimSpace(cmd.Text))
	needsAsync := len(fields) > 0 && (strings.ToLower(fields[0]) == "list" || strings.ToLower(fields[0]) == "search")

	if needsAsync && cmd.ResponseURL != "" {
		// Ack immediately; process in background and post delayed_response.
		h.writeJSON(w, http.StatusOK, integrations.SlackMessage{
			ResponseType: "ephemeral",
			Text:         ":hourglass_flowing_sand: Working on it...",
		})

		go func() {
			msg, handleErr := h.slack.HandleSlashCommand(cmd)
			if handleErr != nil {
				h.logger.Error("slack/commands: handle async", zap.Error(handleErr))
				return
			}
			msg.ResponseType = "in_channel"
			if postErr := postDelayedResponse(cmd.ResponseURL, msg); postErr != nil {
				h.logger.Error("slack/commands: delayed response", zap.Error(postErr))
			}
		}()
		return
	}

	// Synchronous path — respond within Slack's 3 s window.
	msg, err := h.slack.HandleSlashCommand(cmd)
	if err != nil {
		h.logger.Error("slack/commands: handle", zap.Error(err))
		h.writeJSON(w, http.StatusOK, integrations.SlackMessage{
			ResponseType: "ephemeral",
			Text:         ":x: An error occurred. Please try again.",
		})
		return
	}

	h.writeJSON(w, http.StatusOK, msg)
}

// HandleBlockAction handles POST /api/v1/marketplace/slack/actions.
//
// Slack sends block action payloads as a URL-encoded form field named "payload"
// containing a JSON string. This handler:
//  1. Reads the full body for signature verification.
//  2. Verifies the HMAC-SHA256 Slack signature.
//  3. Parses the block action payload.
//  4. Returns 200 immediately (Slack requires a response within 3 s).
//  5. Processes the action in a background goroutine.
func (h *InboundHandler) HandleBlockAction(w http.ResponseWriter, r *http.Request) {
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	signature := r.Header.Get("X-Slack-Signature")

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.Error("slack/actions: read body", zap.Error(err))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if !h.slack.VerifySignature(timestamp, string(rawBody), signature) {
		h.logger.Warn("slack/actions: invalid signature",
			zap.String("timestamp", timestamp),
			zap.String("signature", signature),
		)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Slack sends block actions as: payload=<url-encoded JSON>
	values, err := url.ParseQuery(string(rawBody))
	if err != nil {
		http.Error(w, "bad form body", http.StatusBadRequest)
		return
	}

	payloadJSON := values.Get("payload")
	if payloadJSON == "" {
		http.Error(w, "missing payload field", http.StatusBadRequest)
		return
	}

	// Decode the envelope to extract action details.
	var envelope struct {
		Type    string `json:"type"`
		User    struct {
			ID string `json:"id"`
		} `json:"user"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		ResponseURL string `json:"response_url"`
		Actions     []struct {
			ActionID string `json:"action_id"`
			BlockID  string `json:"block_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}

	if err := json.Unmarshal([]byte(payloadJSON), &envelope); err != nil {
		h.logger.Error("slack/actions: decode payload", zap.Error(err))
		http.Error(w, "invalid payload JSON", http.StatusBadRequest)
		return
	}

	// Ack immediately — Slack requires a 200 within 3 s.
	w.WriteHeader(http.StatusOK)

	// Process each action in the background.
	for _, a := range envelope.Actions {
		action := integrations.BlockAction{
			ActionID:    a.ActionID,
			BlockID:     a.BlockID,
			Value:       a.Value,
			UserID:      envelope.User.ID,
			ChannelID:   envelope.Channel.ID,
			ResponseURL: envelope.ResponseURL,
		}

		go func(act integrations.BlockAction) {
			if handleErr := h.slack.HandleBlockAction(act); handleErr != nil {
				h.logger.Error("slack/actions: handle action",
					zap.String("action_id", act.ActionID),
					zap.Error(handleErr),
				)
			}
		}(action)
	}
}

// writeJSON serialises v as JSON to the response with the given status code.
func (h *InboundHandler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("inbound: writeJSON encode error", zap.Error(err))
	}
}

// postDelayedResponse sends a delayed_response message to a Slack response_url.
func postDelayedResponse(responseURL string, msg integrations.SlackMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	resp, err := http.Post(responseURL, "application/json", bytes.NewReader(payload)) //nolint:noctx
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
