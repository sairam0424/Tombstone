package integrations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/tombstone/marketplace/internal/httpclient"
)

// SlackApp handles interactive Slack app features:
//   - Inbound slash commands (/tombstone status <flag_key>)
//   - Inbound block actions (Kill Switch button, View Details button)
//   - Outbound Block Kit messages with interactive buttons
type SlackApp struct {
	botToken      string // SLACK_BOT_TOKEN (xoxb-...)
	signingSecret string // SLACK_SIGNING_SECRET
	apiURL        string // flag-api URL for fetching flag state
	flagAPIToken  string // FLAG_API_TOKEN — Bearer token for flag-api RBAC (flags:kill_switch)
	resilientHTTP *httpclient.ResilientClient
}

// NewSlackApp constructs a SlackApp with the given credentials and flag-api URL.
// flagAPIToken must carry the flags:kill_switch RBAC permission on flag-api.
// logger may be nil (defaults to a no-op logger), matching this package's other
// constructors (e.g. rollback.NewExecutor, syncer.NewSyncer).
func NewSlackApp(botToken, signingSecret, apiURL, flagAPIToken string, logger *zap.Logger) *SlackApp {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SlackApp{
		botToken:      botToken,
		signingSecret: signingSecret,
		apiURL:        apiURL,
		flagAPIToken:  flagAPIToken,
		resilientHTTP: httpclient.NewResilientClient(httpclient.DefaultConfig(), &http.Client{Timeout: 5 * time.Second}, logger),
	}
}

// SlashCommand represents an inbound Slack slash command payload.
type SlashCommand struct {
	Command     string `form:"command"`
	Text        string `form:"text"`
	UserID      string `form:"user_id"`
	UserName    string `form:"user_name"`
	ChannelID   string `form:"channel_id"`
	ChannelName string `form:"channel_name"`
	TeamID      string `form:"team_id"`
	ResponseURL string `form:"response_url"`
}

// BlockAction represents an inbound Slack block action (button click).
type BlockAction struct {
	ActionID    string         `json:"action_id"`
	BlockID     string         `json:"block_id"`
	Value       string         `json:"value"`
	UserID      string         `json:"-"` // set from payload envelope
	ChannelID   string         `json:"-"` // set from payload envelope
	ResponseURL string         `json:"-"` // set from payload envelope
	Payload     map[string]any `json:"-"` // raw envelope for metadata
}

// SlackMessage is a Slack Block Kit message payload.
type SlackMessage struct {
	ResponseType string  `json:"response_type,omitempty"` // "in_channel" | "ephemeral"
	Text         string  `json:"text,omitempty"`          // fallback text
	Blocks       []Block `json:"blocks,omitempty"`
}

// Block represents a single Slack Block Kit block.
type Block struct {
	Type      string   `json:"type"`
	Text      *TextObj `json:"text,omitempty"`
	Elements  []any    `json:"elements,omitempty"`
	BlockID   string   `json:"block_id,omitempty"`
	Accessory *Button  `json:"accessory,omitempty"`
}

// TextObj is a Slack Block Kit text object.
type TextObj struct {
	Type  string `json:"type"` // "mrkdwn" | "plain_text"
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

// Button is a Slack Block Kit interactive button element.
type Button struct {
	Type     string   `json:"type"` // always "button"
	Text     TextObj  `json:"text"`
	ActionID string   `json:"action_id"`
	Value    string   `json:"value,omitempty"`
	Style    string   `json:"style,omitempty"` // "primary" | "danger"
	Confirm  *Confirm `json:"confirm,omitempty"`
}

// Confirm is a Slack confirmation dialog for dangerous actions.
type Confirm struct {
	Title   TextObj `json:"title"`
	Text    TextObj `json:"text"`
	Confirm TextObj `json:"confirm"`
	Deny    TextObj `json:"deny"`
	Style   string  `json:"style,omitempty"` // "danger"
}

// HasSigningSecret reports whether a signing secret was provided at construction time.
// Use this as the guard condition for signature verification — it uses the same value
// as VerifySignature, preventing the split-brain where os.Getenv and s.signingSecret diverge.
func (s *SlackApp) HasSigningSecret() bool {
	return s.signingSecret != ""
}

// VerifySignature verifies a Slack request signature using timing-safe HMAC-SHA256.
// timestamp is the X-Slack-Request-Timestamp header value.
// body is the raw request body string.
// signature is the X-Slack-Signature header value (v0=<hex>).
// Returns false if the timestamp is stale (>5 minutes) or the HMAC does not match.
func (s *SlackApp) VerifySignature(timestamp, body, signature string) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	// Reject requests older than 5 minutes to prevent replay attacks.
	if time.Now().Unix()-ts > 300 {
		return false
	}

	baseString := fmt.Sprintf("v0:%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(s.signingSecret))
	mac.Write([]byte(baseString))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

// HandleSlashCommand processes /tombstone <subcommand> [args].
// Supported subcommands: status <key>, kill <key>, list [env], search <query>
func (s *SlackApp) HandleSlashCommand(cmd SlashCommand) (SlackMessage, error) {
	parts := strings.Fields(strings.TrimSpace(cmd.Text))
	if len(parts) == 0 {
		return s.helpMessage(), nil
	}

	sub := strings.ToLower(parts[0])
	args := parts[1:]

	switch sub {
	case "status":
		if len(args) == 0 {
			return errorMessage("Usage: /tombstone status <flag_key>"), nil
		}
		return s.statusCommand(args[0], "production")

	case "kill":
		if len(args) == 0 {
			return errorMessage("Usage: /tombstone kill <flag_key>"), nil
		}
		return s.killConfirmMessage(args[0]), nil

	case "list":
		env := "production"
		if len(args) > 0 {
			env = args[0]
		}
		return s.listCommand(env)

	case "search":
		if len(args) == 0 {
			return errorMessage("Usage: /tombstone search <query>"), nil
		}
		return s.searchCommand(strings.Join(args, " "))

	default:
		return s.helpMessage(), nil
	}
}

// HandleBlockAction processes button clicks from Block Kit messages.
// Supported action IDs: kill_switch_confirm, view_in_dashboard, dismiss
func (s *SlackApp) HandleBlockAction(action BlockAction) error {
	switch action.ActionID {
	case "kill_switch_confirm":
		return s.executeKillSwitch(action)
	case "view_in_dashboard":
		// No server-side work needed — URL encoded in button value.
		return nil
	case "dismiss":
		return s.postResponse(action.ResponseURL, SlackMessage{
			ResponseType: "ephemeral",
			Text:         "Dismissed.",
		})
	default:
		return fmt.Errorf("unknown action_id: %s", action.ActionID)
	}
}

// BuildFlagStatusMessage creates a Block Kit message showing flag state.
// flagData is a map containing at minimum: state (bool), rollout_pct (int),
// environment (string), owner (string).
func (s *SlackApp) BuildFlagStatusMessage(flagKey, env string, flagData map[string]interface{}) SlackMessage {
	state, _ := flagData["state"].(bool)
	rolloutPct, _ := flagData["rollout_pct"].(float64)
	owner, _ := flagData["owner"].(string)
	if owner == "" {
		owner = "unknown"
	}
	if env == "" {
		env = "production"
	}

	stateBadge := stateBadge(state, rolloutPct)
	headerText := fmt.Sprintf("%s  *%s*", stateBadge, flagKey)
	killSwitchStyle := "danger"
	if !state {
		killSwitchStyle = "primary"
	}

	dashboardURL := fmt.Sprintf("https://tombstone.io/flags/%s?env=%s",
		url.PathEscape(flagKey), url.QueryEscape(env))

	return SlackMessage{
		Text: fmt.Sprintf("Flag status: %s [%s]", flagKey, env),
		Blocks: []Block{
			{
				Type: "header",
				Text: &TextObj{
					Type: "plain_text",
					Text: fmt.Sprintf("Flag Status: %s", flagKey),
				},
			},
			{
				Type: "section",
				Text: &TextObj{
					Type: "mrkdwn",
					Text: headerText,
				},
			},
			{
				Type: "section",
				Text: &TextObj{
					Type: "mrkdwn",
					Text: fmt.Sprintf("*Environment:* %s\n*Rollout:* %.0f%%\n*Owner:* %s",
						env, rolloutPct, owner),
				},
			},
			{
				Type: "actions",
				Elements: []any{
					Button{
						Type: "button",
						Text: TextObj{
							Type:  "plain_text",
							Text:  "Kill Switch",
							Emoji: true,
						},
						ActionID: "kill_switch_confirm",
						Value:    fmt.Sprintf("%s|%s", flagKey, env),
						Style:    killSwitchStyle,
						Confirm: &Confirm{
							Title: TextObj{
								Type: "plain_text",
								Text: "Confirm Kill Switch",
							},
							Text: TextObj{
								Type: "mrkdwn",
								Text: fmt.Sprintf("Are you sure you want to kill *%s* in *%s*? This disables the flag immediately.", flagKey, env),
							},
							Confirm: TextObj{
								Type:  "plain_text",
								Text:  "Kill it",
								Emoji: true,
							},
							Deny: TextObj{
								Type:  "plain_text",
								Text:  "Cancel",
								Emoji: true,
							},
							Style: "danger",
						},
					},
					Button{
						Type: "button",
						Text: TextObj{
							Type:  "plain_text",
							Text:  "View in Dashboard",
							Emoji: true,
						},
						ActionID: "view_in_dashboard",
						Value:    dashboardURL,
					},
					Button{
						Type: "button",
						Text: TextObj{
							Type:  "plain_text",
							Text:  "Dismiss",
							Emoji: true,
						},
						ActionID: "dismiss",
					},
				},
			},
			{
				Type: "divider",
			},
		},
	}
}

// PostMessage sends a Block Kit message to a Slack channel via chat.postMessage.
func (s *SlackApp) PostMessage(ctx context.Context, channelID string, msg SlackMessage) error {
	type postBody struct {
		Channel string  `json:"channel"`
		Text    string  `json:"text,omitempty"`
		Blocks  []Block `json:"blocks,omitempty"`
	}

	body := postBody{
		Channel: channelID,
		Text:    msg.Text,
		Blocks:  msg.Blocks,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("slack: marshal post body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://slack.com/api/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("slack: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.botToken)

	resp, err := s.resilientHTTP.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("slack: post message: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("slack: decode response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("slack: API error: %s", result.Error)
	}
	return nil
}

// --- internal helpers ---

// statusCommand fetches flag state from flag-api and returns a Block Kit message.
func (s *SlackApp) statusCommand(flagKey, env string) (SlackMessage, error) {
	flagData, err := s.fetchFlagData(flagKey, env)
	if err != nil {
		return errorMessage(fmt.Sprintf("Could not fetch flag *%s*: %s", flagKey, err.Error())), nil
	}
	return s.BuildFlagStatusMessage(flagKey, env, flagData), nil
}

// killConfirmMessage returns an ephemeral confirmation prompt before killing a flag.
func (s *SlackApp) killConfirmMessage(flagKey string) SlackMessage {
	return SlackMessage{
		ResponseType: "ephemeral",
		Text:         fmt.Sprintf("Kill switch confirmation for *%s*", flagKey),
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObj{
					Type: "mrkdwn",
					Text: fmt.Sprintf(":warning: You are about to kill *%s* in production. Use the Kill Switch button below to confirm.", flagKey),
				},
			},
			{
				Type: "actions",
				Elements: []any{
					Button{
						Type: "button",
						Text: TextObj{
							Type:  "plain_text",
							Text:  "Confirm Kill Switch",
							Emoji: true,
						},
						ActionID: "kill_switch_confirm",
						Value:    fmt.Sprintf("%s|production", flagKey),
						Style:    "danger",
					},
					Button{
						Type: "button",
						Text: TextObj{
							Type:  "plain_text",
							Text:  "Cancel",
							Emoji: true,
						},
						ActionID: "dismiss",
					},
				},
			},
		},
	}
}

// listCommand returns a message listing flags for the given environment.
func (s *SlackApp) listCommand(env string) (SlackMessage, error) {
	apiURL := fmt.Sprintf("%s/api/v1/flags?environment=%s&limit=10", s.apiURL, url.QueryEscape(env))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, apiURL, nil) //nolint:noctx
	if err != nil {
		return errorMessage("Could not build flag-api request: " + err.Error()), nil
	}
	resp, err := s.resilientHTTP.Do(req.Context(), req)
	if err != nil {
		return errorMessage("Could not reach flag-api: " + err.Error()), nil
	}
	defer resp.Body.Close()

	var result struct {
		Flags []struct {
			Key     string `json:"key"`
			State   bool   `json:"state"`
			Rollout int    `json:"rollout_pct"`
		} `json:"flags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return errorMessage("Could not parse flag list response."), nil
	}

	if len(result.Flags) == 0 {
		return SlackMessage{
			ResponseType: "ephemeral",
			Text:         fmt.Sprintf("No flags found in *%s*.", env),
		}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Flags in %s* (showing up to 10):\n", env))
	for _, f := range result.Flags {
		badge := stateBadge(f.State, float64(f.Rollout))
		sb.WriteString(fmt.Sprintf("%s `%s` — %d%% rollout\n", badge, f.Key, f.Rollout))
	}

	return SlackMessage{
		ResponseType: "in_channel",
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObj{
					Type: "mrkdwn",
					Text: sb.String(),
				},
			},
		},
	}, nil
}

// searchCommand searches flags matching the query string.
func (s *SlackApp) searchCommand(query string) (SlackMessage, error) {
	apiURL := fmt.Sprintf("%s/api/v1/flags/search?q=%s", s.apiURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, apiURL, nil) //nolint:noctx
	if err != nil {
		return errorMessage("Could not build flag-api request: " + err.Error()), nil
	}
	resp, err := s.resilientHTTP.Do(req.Context(), req)
	if err != nil {
		return errorMessage("Could not reach flag-api: " + err.Error()), nil
	}
	defer resp.Body.Close()

	var result struct {
		Results []struct {
			Key   string `json:"key"`
			State bool   `json:"state"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return errorMessage("Could not parse search response."), nil
	}

	if len(result.Results) == 0 {
		return SlackMessage{
			ResponseType: "ephemeral",
			Text:         fmt.Sprintf("No flags found matching *%s*.", query),
		}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Search results for \"%s\"*:\n", query))
	for _, f := range result.Results {
		badge := stateBadge(f.State, 0)
		sb.WriteString(fmt.Sprintf("%s `%s`\n", badge, f.Key))
	}

	return SlackMessage{
		ResponseType: "in_channel",
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObj{
					Type: "mrkdwn",
					Text: sb.String(),
				},
			},
		},
	}, nil
}

// isAuthorizedKillSwitchUser returns true when the Slack user ID is allowed to
// execute kill switches. Authorization is controlled by the SLACK_KILL_SWITCH_ALLOWED_USERS
// env var — a comma-separated list of Slack user IDs (e.g. "U0123ABCD,U9876ZXYW").
// When the env var is not set, all users are denied (fail-closed).
func (s *SlackApp) isAuthorizedKillSwitchUser(slackUserID string) bool {
	allowed := os.Getenv("SLACK_KILL_SWITCH_ALLOWED_USERS")
	if allowed == "" {
		return false // fail-closed: no explicit allowlist → nobody allowed
	}
	for _, uid := range strings.Split(allowed, ",") {
		if strings.TrimSpace(uid) == slackUserID {
			return true
		}
	}
	return false
}

// executeKillSwitch calls flag-api to disable a flag and posts confirmation.
// Requires action.UserID to be in SLACK_KILL_SWITCH_ALLOWED_USERS — fail-closed.
func (s *SlackApp) executeKillSwitch(action BlockAction) error {
	if !s.isAuthorizedKillSwitchUser(action.UserID) {
		return s.postResponse(action.ResponseURL, SlackMessage{
			ResponseType: "ephemeral",
			Text:         "⛔ You are not authorized to execute kill switches. Contact a Tombstone admin.",
		})
	}

	parts := strings.SplitN(action.Value, "|", 2)
	if len(parts) != 2 {
		return fmt.Errorf("kill_switch_confirm: malformed value %q", action.Value)
	}
	flagKey, env := parts[0], parts[1]

	type killBody struct {
		Actor string `json:"actor"`
	}
	body := killBody{Actor: action.UserID}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("kill switch: marshal body: %w", err)
	}

	apiURL := fmt.Sprintf("%s/api/v1/flags/%s/kill?environment=%s",
		s.apiURL, url.PathEscape(flagKey), url.QueryEscape(env))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("kill switch: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.flagAPIToken)

	resp, err := s.resilientHTTP.Do(req.Context(), req)
	if err != nil {
		return fmt.Errorf("kill switch: request failed: %w", err)
	}
	defer resp.Body.Close()

	var confirmMsg SlackMessage
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		confirmMsg = SlackMessage{
			ResponseType: "in_channel",
			Blocks: []Block{
				{
					Type: "section",
					Text: &TextObj{
						Type: "mrkdwn",
						Text: fmt.Sprintf(":skull_and_crossbones: Kill switch activated for *%s* in *%s* by <@%s>.",
							flagKey, env, action.UserID),
					},
				},
			},
		}
	} else {
		rawBody, _ := io.ReadAll(resp.Body)
		confirmMsg = errorMessage(fmt.Sprintf("Kill switch failed for *%s*: HTTP %d — %s", flagKey, resp.StatusCode, string(rawBody)))
	}

	return s.postResponse(action.ResponseURL, confirmMsg)
}

// fetchFlagData calls flag-api to retrieve current flag state.
func (s *SlackApp) fetchFlagData(flagKey, env string) (map[string]interface{}, error) {
	apiURL := fmt.Sprintf("%s/api/v1/flags/%s?environment=%s",
		s.apiURL, url.PathEscape(flagKey), url.QueryEscape(env))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, apiURL, nil) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("build get flag request: %w", err)
	}
	resp, err := s.resilientHTTP.Do(req.Context(), req)
	if err != nil {
		return nil, fmt.Errorf("get flag: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("flag %q not found", flagKey)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("flag-api returned HTTP %d", resp.StatusCode)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode flag data: %w", err)
	}
	return data, nil
}

// postResponse sends a delayed response to Slack via a response_url.
func (s *SlackApp) postResponse(responseURL string, msg SlackMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("post response: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, responseURL, bytes.NewReader(payload)) //nolint:noctx
	if err != nil {
		return fmt.Errorf("post response: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.resilientHTTP.Do(req.Context(), req)
	if err != nil {
		return fmt.Errorf("post response: HTTP post: %w", err)
	}
	defer resp.Body.Close()
	return nil
}

// helpMessage returns the /tombstone command reference.
func (s *SlackApp) helpMessage() SlackMessage {
	return SlackMessage{
		ResponseType: "ephemeral",
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObj{
					Type: "mrkdwn",
					Text: "*Tombstone Slack Commands*\n" +
						"`/tombstone status <flag_key>` — Show flag state + kill switch button\n" +
						"`/tombstone kill <flag_key>` — Activate kill switch (with confirmation)\n" +
						"`/tombstone list [env]` — List flags in an environment (default: production)\n" +
						"`/tombstone search <query>` — Search flags by name or description",
				},
			},
		},
	}
}

// errorMessage returns an ephemeral error message block.
func errorMessage(text string) SlackMessage {
	return SlackMessage{
		ResponseType: "ephemeral",
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObj{
					Type: "mrkdwn",
					Text: ":x: " + text,
				},
			},
		},
	}
}

// stateBadge returns a coloured emoji badge based on flag state and rollout.
func stateBadge(state bool, rolloutPct float64) string {
	if !state {
		return ":red_circle:"
	}
	if rolloutPct < 100 {
		return ":yellow_circle:"
	}
	return ":large_green_circle:"
}
