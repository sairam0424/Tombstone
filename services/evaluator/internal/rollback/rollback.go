package rollback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/evaluator/internal/httpclient"
	"github.com/tombstone/evaluator/internal/tlsutil"
)

// RollbackRequest describes an automatic or manual rollback action.
type RollbackRequest struct {
	FlagKey     string  `json:"flag_key"`
	Environment string  `json:"environment"`
	Reason      string  `json:"reason"`
	ErrorRate   float64 `json:"error_rate"`
	TriggeredBy string  `json:"triggered_by"`
}

// Executor calls the flag-api kill switch and publishes a Redis event.
type Executor struct {
	flagAPIURL    string
	rdb           *redis.Client
	resilientHTTP *httpclient.ResilientClient
	logger        *zap.Logger
	flagAPIToken  string
}

func NewExecutor(flagAPIURL, flagAPIToken string, rdb *redis.Client, logger *zap.Logger) *Executor {
	httpClient := &http.Client{Timeout: 5 * time.Second}
	if os.Getenv("MTLS_ENABLED") == "true" {
		certsDir := os.Getenv("CERTS_DIR")
		if certsDir == "" {
			certsDir = "/certs"
		}
		tlsCfg, err := tlsutil.LoadClientTLSConfig(certsDir)
		if err != nil {
			logger.Warn("mTLS client cert load failed — falling back to plain HTTP", zap.Error(err))
		} else {
			httpClient.Transport = &http.Transport{TLSClientConfig: tlsCfg}
			logger.Info("evaluator -> flag-api mTLS enabled")
		}
	}

	// Kill-switch calls fire during an active incident (error rate already
	// elevated) — deviate from httpclient.DefaultConfig() toward fewer
	// retries and a shorter circuit-breaker cooldown than a periodic sync
	// caller: we want to fail fast and let the caller's own retry/alerting
	// take over rather than blocking the rollback path for multiple seconds,
	// and a 15s open-duration (vs the 30s default) means we start probing
	// flag-api again sooner once it recovers mid-incident.
	cfg := httpclient.ResilientClientConfig{
		MaxRetries:       2,
		InitialDelay:     100 * time.Millisecond,
		MaxDelay:         1 * time.Second,
		Timeout:          5 * time.Second,
		FailureThreshold: 3,
		OpenDuration:     15 * time.Second,
	}

	return &Executor{
		flagAPIURL:    flagAPIURL,
		flagAPIToken:  flagAPIToken,
		rdb:           rdb,
		resilientHTTP: httpclient.NewResilientClient(cfg, httpClient, logger),
		logger:        logger,
	}
}

// Execute disables the flag in flag-api and publishes a kill_switch SSE event.
func (e *Executor) Execute(ctx context.Context, req RollbackRequest) error {
	e.logger.Warn("executing automatic rollback",
		zap.String("flag", req.FlagKey),
		zap.String("env", req.Environment),
		zap.Float64("error_rate", req.ErrorRate),
		zap.String("reason", req.Reason))

	// 1. Call flag-api kill switch
	killBody, _ := json.Marshal(map[string]string{
		"environment": req.Environment,
		"reason":      req.Reason,
	})
	killReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/flags/%s/kill", e.flagAPIURL, req.FlagKey),
		bytes.NewReader(killBody))
	if err != nil {
		return fmt.Errorf("build kill request: %w", err)
	}
	killReq.Header.Set("Authorization", "Bearer "+e.flagAPIToken)
	killReq.Header.Set("Content-Type", "application/json")

	resp, err := e.resilientHTTP.Do(ctx, killReq)
	if err != nil {
		return fmt.Errorf("kill switch call failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("kill switch returned HTTP %d", resp.StatusCode)
	}

	// 2. Publish kill_switch event directly to Redis (belt-and-suspenders alongside flag-api)
	event := map[string]interface{}{
		"flag_key":    req.FlagKey,
		"enabled":     false,
		"rollout_pct": 0,
		"reason":      req.Reason,
		"ts":          time.Now().Unix(),
		"environment": req.Environment,
	}
	payload, _ := json.Marshal(event)
	channel := fmt.Sprintf("stream:%s:updates", req.Environment)
	if pubErr := e.rdb.Publish(ctx, channel, payload).Err(); pubErr != nil {
		e.logger.Warn("redis publish on rollback failed", zap.Error(pubErr))
	}

	e.logger.Info("rollback executed successfully",
		zap.String("flag", req.FlagKey), zap.String("env", req.Environment))
	return nil
}

// SetRolloutPct calls flag-api's EVAL-4 graduated rollback-step endpoint
// (POST /flags/{key}/rollback-step) to reduce a flag's exposure to pct
// WITHOUT fully disabling it (enabled stays true unless pct==0) -- the
// stepped-ladder counterpart to Execute's binary kill switch above.
//
// Unlike Execute, a 409 response is treated as success, not an error:
// flag-api's own atomic compare-and-swap write refuses a step whose target
// no longer reflects the live state (a concurrent, more-aggressive step
// already won), which means the safety property this call exists to
// establish -- exposure is now at most pct -- already holds by the time
// this call returns 409, just achieved by someone else.
func (e *Executor) SetRolloutPct(ctx context.Context, flagKey, environment string, pct int, reason string) error {
	if pct < 0 || pct > 100 {
		return fmt.Errorf("rollout_pct must be between 0 and 100, got %d", pct)
	}

	e.logger.Warn("executing rollback step",
		zap.String("flag", flagKey), zap.String("env", environment),
		zap.Int("rollout_pct", pct), zap.String("reason", reason))

	body, _ := json.Marshal(map[string]any{
		"environment": environment,
		"rollout_pct": pct,
		"reason":      reason,
	})
	stepReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/flags/%s/rollback-step", e.flagAPIURL, flagKey),
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build rollback-step request: %w", err)
	}
	stepReq.Header.Set("Authorization", "Bearer "+e.flagAPIToken)
	stepReq.Header.Set("Content-Type", "application/json")

	resp, err := e.resilientHTTP.Do(ctx, stepReq)
	if err != nil {
		return fmt.Errorf("rollback-step call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		e.logger.Info("rollback-step superseded by a concurrent, more-aggressive step",
			zap.String("flag", flagKey), zap.String("env", environment), zap.Int("requested_pct", pct))
		return nil
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("rollback-step returned HTTP %d", resp.StatusCode)
	}

	// Belt-and-suspenders alongside flag-api's own SSE broadcast, matching
	// Execute's identical pattern above.
	event := map[string]interface{}{
		"flag_key":    flagKey,
		"enabled":     pct > 0,
		"rollout_pct": pct,
		"reason":      reason,
		"ts":          time.Now().Unix(),
		"environment": environment,
	}
	payload, _ := json.Marshal(event)
	channel := fmt.Sprintf("stream:%s:updates", environment)
	if pubErr := e.rdb.Publish(ctx, channel, payload).Err(); pubErr != nil {
		e.logger.Warn("redis publish on rollback-step failed", zap.Error(pubErr))
	}

	e.logger.Info("rollback-step executed successfully",
		zap.String("flag", flagKey), zap.String("env", environment), zap.Int("rollout_pct", pct))
	return nil
}

// IncreaseRolloutPct calls flag-api's EVAL-4 graduated recovery-step
// endpoint (POST /flags/{key}/recovery-step) to raise a flag's exposure to
// pct -- the HALF_OPEN recovery ladder's ascent counterpart to
// SetRolloutPct's descent above. A separate flag-api endpoint exists
// specifically because RollbackStep/rollback-step can never increase
// exposure (see that handler's own doc comment); recovery-step is its
// mirror image, which can never decrease.
//
// Unlike SetRolloutPct, a 409 here is treated as an ERROR, not success:
// on the descent side, a stale target superseded by a MORE aggressive
// concurrent step down still satisfies the caller's own goal ("exposure is
// now at most pct"). On the ascent side, a 409 is genuinely ambiguous --
// it could mean a more-aggressive concurrent recovery already won (this
// call's goal is still satisfied), OR it could mean a fresh incident
// dropped exposure back down while this call was in flight (this call's
// goal is NOT satisfied, and treating it as success would mask a real
// revert). Retrying is the safe default for an ambiguous signal: Flush's
// next tick re-reads Redis-tracked position and recomputes the correct
// target from wherever the ladder actually is by then.
func (e *Executor) IncreaseRolloutPct(ctx context.Context, flagKey, environment string, pct int, reason string) error {
	if pct < 0 || pct > 100 {
		return fmt.Errorf("rollout_pct must be between 0 and 100, got %d", pct)
	}

	e.logger.Info("executing recovery step",
		zap.String("flag", flagKey), zap.String("env", environment),
		zap.Int("rollout_pct", pct), zap.String("reason", reason))

	body, _ := json.Marshal(map[string]any{
		"environment": environment,
		"rollout_pct": pct,
		"reason":      reason,
	})
	stepReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/flags/%s/recovery-step", e.flagAPIURL, flagKey),
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build recovery-step request: %w", err)
	}
	stepReq.Header.Set("Authorization", "Bearer "+e.flagAPIToken)
	stepReq.Header.Set("Content-Type", "application/json")

	resp, err := e.resilientHTTP.Do(ctx, stepReq)
	if err != nil {
		return fmt.Errorf("recovery-step call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("recovery-step returned HTTP %d", resp.StatusCode)
	}

	// Belt-and-suspenders alongside flag-api's own SSE broadcast, matching
	// SetRolloutPct/Execute's identical pattern above.
	event := map[string]interface{}{
		"flag_key":    flagKey,
		"enabled":     pct > 0,
		"rollout_pct": pct,
		"reason":      reason,
		"ts":          time.Now().Unix(),
		"environment": environment,
	}
	payload, _ := json.Marshal(event)
	channel := fmt.Sprintf("stream:%s:updates", environment)
	if pubErr := e.rdb.Publish(ctx, channel, payload).Err(); pubErr != nil {
		e.logger.Warn("redis publish on recovery-step failed", zap.Error(pubErr))
	}

	e.logger.Info("recovery-step executed successfully",
		zap.String("flag", flagKey), zap.String("env", environment), zap.Int("rollout_pct", pct))
	return nil
}
