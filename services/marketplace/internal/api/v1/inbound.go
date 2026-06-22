package v1

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
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

// datadogPayload is the inbound Datadog monitor-alert webhook payload.
// Datadog sends a subset of these fields; unknown fields are ignored.
type datadogPayload struct {
	AlertID string `json:"alert_id"`
	Title   string `json:"title"`
	// Severity maps to Datadog monitor notification priority: P1, P2, P3, P4.
	Severity string `json:"severity"`
	// URL is the Datadog monitor URL for deep-linking.
	URL string `json:"url"`
	// Body is the alert message body / description.
	Body string `json:"body"`
	// Tags is the list of monitor / event tags (e.g. "service:payments").
	Tags []string `json:"tags"`
	// TransitionedAt is the Unix timestamp (ms) when the alert transitioned.
	TransitionedAt int64 `json:"transitioned_at"`
}

// blastRadiusResult is the subset of the evaluator blast-radius response we care about.
type blastRadiusResult struct {
	FlagKey string `json:"flag_key"`
	// Status is one of: LOW, MEDIUM, HIGH, BLOCKED.
	Status string `json:"status"`
	// Score is 0-100.
	Score int `json:"score"`
}

// flagItem is a minimal flag object returned by flag-api list endpoint.
type flagItem struct {
	Key  string   `json:"key"`
	Tags []string `json:"tags"`
}

// inboundSummary is the JSON body returned by HandleDatadogInbound.
type inboundSummary struct {
	AlertID        string              `json:"alert_id"`
	Service        string              `json:"service"`
	FlagsEvaluated int                 `json:"flags_evaluated"`
	Triggered      []blastRadiusResult `json:"triggered"`
	KillSwitched   []string            `json:"kill_switched"`
	ActionsAt      string              `json:"actions_at"`
}

// isCriticalSeverity returns true when the Datadog priority is P1 or P2.
func isCriticalSeverity(s string) bool {
	upper := strings.ToUpper(strings.TrimSpace(s))
	return upper == "P1" || upper == "P2"
}

// extractService finds the first "service:<name>" tag and returns <name>.
func extractService(tags []string) string {
	for _, t := range tags {
		lower := strings.ToLower(t)
		if strings.HasPrefix(lower, "service:") {
			return strings.TrimPrefix(lower, "service:")
		}
	}
	return ""
}

// verifyDatadogSignature validates the DD-Signature header.
// Datadog computes HMAC-SHA256(secret, request_body) and sends the hex digest.
func verifyDatadogSignature(secret string, body []byte, header string) bool {
	if secret == "" || header == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(strings.TrimSpace(header))))
}

// HandleDatadogInbound processes POST /api/v1/marketplace/inbound/datadog.
//
// Flow:
//  1. Verify DD-Signature HMAC-SHA256 using DD_WEBHOOK_SECRET.
//  2. Parse payload; extract service name from tags.
//  3. Query flag-api for flags tagged with that service.
//  4. Call evaluator blast-radius for each flag.
//  5. If any flag is BLOCKED and alert is P1/P2: POST kill switch to flag-api.
//  6. Return JSON summary of actions taken.
func (h *Handler) HandleDatadogInbound(w http.ResponseWriter, r *http.Request) {
	// 1. Read body (needed for HMAC verification before we can parse it).
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
		return
	}

	// 2. Verify HMAC-SHA256 signature.
	secret := os.Getenv("DD_WEBHOOK_SECRET")
	sig := r.Header.Get("DD-Signature")
	if !verifyDatadogSignature(secret, rawBody, sig) {
		h.logger.Warn("datadog inbound: signature verification failed",
			zap.String("remote_addr", r.RemoteAddr),
		)
		h.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid signature"})
		return
	}

	// 3. Parse Datadog payload.
	var payload datadogPayload
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON payload"})
		return
	}

	service := extractService(payload.Tags)
	h.logger.Info("datadog inbound: alert received",
		zap.String("alert_id", payload.AlertID),
		zap.String("severity", payload.Severity),
		zap.String("service", service),
	)

	// 4. Resolve upstream service addresses.
	flagAPIBase := os.Getenv("FLAG_API_URL")
	if flagAPIBase == "" {
		flagAPIBase = "http://flag-api:8081"
	}
	evaluatorBase := os.Getenv("EVALUATOR_URL")
	if evaluatorBase == "" {
		evaluatorBase = "http://evaluator:8082"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// 5. Query flag-api for flags associated with the service.
	flags, err := fetchFlagsByService(ctx, flagAPIBase, service)
	if err != nil {
		h.logger.Error("datadog inbound: failed to fetch flags",
			zap.String("service", service),
			zap.Error(err),
		)
		// Return 200 with empty summary rather than failing — webhook must not retry on our errors.
		h.writeJSON(w, http.StatusOK, inboundSummary{
			AlertID:   payload.AlertID,
			Service:   service,
			ActionsAt: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	// 6. Evaluate blast radius for each flag.
	client := &http.Client{Timeout: 15 * time.Second}
	var triggered []blastRadiusResult
	for _, f := range flags {
		result, err := fetchBlastRadius(ctx, client, evaluatorBase, f.Key)
		if err != nil {
			h.logger.Warn("datadog inbound: blast radius check failed",
				zap.String("flag", f.Key),
				zap.Error(err),
			)
			continue
		}
		if result.Status == "HIGH" || result.Status == "BLOCKED" {
			triggered = append(triggered, result)
		}
	}

	// 7. Auto-kill-switch the single highest-risk BLOCKED flag if alert is P1/P2.
	var killSwitched []string
	if isCriticalSeverity(payload.Severity) {
		for _, t := range triggered {
			if t.Status == "BLOCKED" {
				if err := postKillSwitch(ctx, client, flagAPIBase, t.FlagKey, payload.AlertID); err != nil {
					h.logger.Error("datadog inbound: kill switch failed",
						zap.String("flag", t.FlagKey),
						zap.Error(err),
					)
				} else {
					killSwitched = append(killSwitched, t.FlagKey)
					h.logger.Info("datadog inbound: kill switch triggered",
						zap.String("flag", t.FlagKey),
						zap.String("alert_id", payload.AlertID),
					)
					// Only kill the first BLOCKED flag to prevent cascading outages.
					break
				}
			}
		}
	}

	summary := inboundSummary{
		AlertID:        payload.AlertID,
		Service:        service,
		FlagsEvaluated: len(flags),
		Triggered:      triggered,
		KillSwitched:   killSwitched,
		ActionsAt:      time.Now().UTC().Format(time.RFC3339),
	}
	h.writeJSON(w, http.StatusOK, summary)
}

// fetchFlagsByService queries flag-api for flags tagged with the given service name.
// Endpoint: GET /api/v1/flags?tag=service:<name>
func fetchFlagsByService(ctx context.Context, flagAPIBase, service string) ([]flagItem, error) {
	url := fmt.Sprintf("%s/api/v1/flags", flagAPIBase)
	if service != "" {
		url = fmt.Sprintf("%s?tag=service:%s", url, service)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flag-api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("flag-api returned %d", resp.StatusCode)
	}

	var items []flagItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode flags: %w", err)
	}
	return items, nil
}

// fetchBlastRadius calls the evaluator blast-radius endpoint for a single flag.
// Endpoint: GET /api/v1/blast-radius/{flag_key}
func fetchBlastRadius(ctx context.Context, client *http.Client, evaluatorBase, flagKey string) (blastRadiusResult, error) {
	url := fmt.Sprintf("%s/api/v1/blast-radius/%s", evaluatorBase, flagKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return blastRadiusResult{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return blastRadiusResult{}, fmt.Errorf("evaluator request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return blastRadiusResult{}, fmt.Errorf("evaluator returned %d", resp.StatusCode)
	}

	var result blastRadiusResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return blastRadiusResult{}, fmt.Errorf("decode blast radius: %w", err)
	}
	result.FlagKey = flagKey
	return result, nil
}

// postKillSwitch sends a kill-switch command to flag-api for the given flag.
// Endpoint: POST /api/v1/flags/{flag_key}/kill-switch
func postKillSwitch(ctx context.Context, client *http.Client, flagAPIBase, flagKey, alertID string) error {
	body := map[string]string{
		"reason": fmt.Sprintf("auto-kill: Datadog alert %s triggered blast-radius BLOCKED", alertID),
		"actor":  "tombstone-marketplace-inbound",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal kill-switch body: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/flags/%s/kill-switch", flagAPIBase, flagKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("kill-switch request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("kill-switch returned %d", resp.StatusCode)
	}
	return nil
}
