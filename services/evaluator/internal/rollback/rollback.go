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
