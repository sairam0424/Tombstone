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
	"net/url"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/tombstone/marketplace/internal/httpclient"
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

// blastRadiusResult is the subset of the evaluator blast-radius response
// (services/evaluator/internal/blast.BlastRadiusResponse) this handler
// cares about. There is no numeric 0-100 "score" anywhere in the real
// response -- blast.Calculator only ever produces the LOW/MEDIUM/HIGH/
// BLOCKED tier string, so a fabricated Score field (present before this fix)
// is dropped rather than mapped from something that doesn't exist.
type blastRadiusResult struct {
	FlagKey string `json:"flag_key"`
	// Status is one of blast.RiskScore's values: LOW, MEDIUM, HIGH, BLOCKED.
	Status string `json:"status"`
}

// evaluatorBlastRadiusResponse mirrors blast.BlastRadiusResponse's real,
// nested JSON shape ({"flag_key": ..., "result": {"risk_score": ...}}) --
// decoding straight into the old flat blastRadiusResult (as this handler
// did before this fix) silently produced an all-zero-value result on every
// call, since none of its field names matched anything in the real
// response.
type evaluatorBlastRadiusResponse struct {
	FlagKey string `json:"flag_key"`
	Result  struct {
		RiskScore string `json:"risk_score"`
	} `json:"result"`
}

// flagItem is the subset of flag-api's Flag object (see
// services/flag-api/internal/api/v1/flags.go) this handler cares about.
// OwnerID, not a nonexistent "tags" concept, is the real field: flags has
// no tags column at all (confirmed against schema.sql), so the "tag"
// query param fetchFlagsByService previously sent was silently ignored by
// flag-api's ListFlags, which supports no filtering and always returns
// every flag in the project regardless. owner_id is the same real-schema
// proxy for "which service owns this flag" that blast.Calculator's own
// AffectedServices field uses, for the identical reason: there is no
// service-registry table.
type flagItem struct {
	Key     string `json:"key"`
	OwnerID string `json:"owner_id"`
}

// flagListResponse mirrors flag-api's real ListFlags response shape
// ({"flags": [...], "total": N}) -- decoding straight into a bare
// []flagItem (as this handler did before this fix) always failed with a
// JSON type-mismatch error, since the real response is an object, not an
// array, meaning this handler could never get past this step at all.
type flagListResponse struct {
	Flags []flagItem `json:"flags"`
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
	flags, err := fetchFlagsByService(ctx, h.resilientHTTP, flagAPIBase, h.flagAPIToken, service)
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
	var triggered []blastRadiusResult
	for _, f := range flags {
		result, err := fetchBlastRadius(ctx, h.resilientHTTP, evaluatorBase, f.Key)
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
				if err := h.postKillSwitch(ctx, flagAPIBase, t.FlagKey, payload.AlertID); err != nil {
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

// fetchFlagsByService queries flag-api for every flag in the caller's
// project, then filters to those owned by service (see flagItem's doc
// comment for why owner_id, not a query-param filter, does the filtering --
// flag-api's ListFlags takes no filter params at all). Requires a bearer
// token because /api/v1/flags sits behind flag-api's Authenticate
// middleware; the previous unauthenticated request always got a 401 here,
// before this handler could reach the blast-radius step at all.
func fetchFlagsByService(ctx context.Context, resilientHTTP *httpclient.ResilientClient, flagAPIBase, flagAPIToken, service string) ([]flagItem, error) {
	reqURL := fmt.Sprintf("%s/api/v1/flags", flagAPIBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+flagAPIToken)

	resp, err := resilientHTTP.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("flag-api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("flag-api returned %d", resp.StatusCode)
	}

	var decoded flagListResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode flags: %w", err)
	}
	if service == "" {
		return decoded.Flags, nil
	}
	matched := make([]flagItem, 0, len(decoded.Flags))
	for _, f := range decoded.Flags {
		if f.OwnerID == service {
			matched = append(matched, f)
		}
	}
	return matched, nil
}

// fetchBlastRadius calls the real evaluator blast-radius endpoint for a
// single flag: GET /api/v1/blast-radius?flag_key=... (query parameters --
// services/evaluator/internal/blast.HandleBlastRadius never registered a
// /{flag_key} path-segment variant, so the previous URL 404'd on every
// call). project_id and rollout_pct are deliberately omitted: evaluator's
// own handler already defaults project_id to the same single-project-
// deployment default blast.Calculator itself uses, and defaults
// rollout_pct to 100 -- the right question during incident triage is "if
// this flag were fully live, is it BLOCKED-risk", the more conservative
// worst case, not merely its current (possibly partial) exposure.
func fetchBlastRadius(ctx context.Context, resilientHTTP *httpclient.ResilientClient, evaluatorBase, flagKey string) (blastRadiusResult, error) {
	reqURL := fmt.Sprintf("%s/api/v1/blast-radius?flag_key=%s", evaluatorBase, url.QueryEscape(flagKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return blastRadiusResult{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := resilientHTTP.Do(ctx, req)
	if err != nil {
		return blastRadiusResult{}, fmt.Errorf("evaluator request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return blastRadiusResult{}, fmt.Errorf("evaluator returned %d", resp.StatusCode)
	}

	var decoded evaluatorBlastRadiusResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return blastRadiusResult{}, fmt.Errorf("decode blast radius: %w", err)
	}
	return blastRadiusResult{FlagKey: flagKey, Status: decoded.Result.RiskScore}, nil
}

// postKillSwitch sends a kill-switch command to flag-api for the given flag.
// Endpoint: POST /api/v1/flags/{flag_key}/kill-switch
// The Authorization Bearer header is set from h.flagAPIToken so flag-api does
// not reject the request with HTTP 401.
func (h *Handler) postKillSwitch(ctx context.Context, flagAPIBase, flagKey, alertID string) error {
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
	req.Header.Set("Authorization", "Bearer "+h.flagAPIToken)

	resp, err := h.resilientHTTP.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("kill-switch request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("kill-switch returned %d", resp.StatusCode)
	}
	return nil
}
