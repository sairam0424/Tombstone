package v1

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"go.uber.org/zap"
)

// EdgeSyncer writes flag snapshots to Cloudflare KV after every flag state change.
// This enables the @tombstone/edge SDK to read sub-1ms flag values on Cloudflare Workers.
// No-op when CLOUDFLARE_* env vars are unset — safe default for local development.
type EdgeSyncer struct {
	accountID   string
	namespaceID string
	apiToken    string
	httpClient  *http.Client
	logger      *zap.Logger
	enabled     bool
}

// NewEdgeSyncer creates an EdgeSyncer. Returns a disabled syncer if env vars are absent.
func NewEdgeSyncer(logger *zap.Logger) *EdgeSyncer {
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	namespaceID := os.Getenv("CLOUDFLARE_KV_NAMESPACE_ID")
	apiToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	enabled := accountID != "" && namespaceID != "" && apiToken != ""
	if enabled {
		logger.Info("edge sync enabled",
			zap.String("account", accountID),
			zap.String("namespace", namespaceID[:8]+"..."))
	}
	return &EdgeSyncer{
		accountID:   accountID,
		namespaceID: namespaceID,
		apiToken:    apiToken,
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		logger:      logger,
		enabled:     enabled,
	}
}

// SyncSnapshot writes the flag snapshot for an environment to Cloudflare KV.
// KV key format: snapshot:{environment}
// Called as a goroutine after every flag state change via go syncer.SyncSnapshot(...)
func (s *EdgeSyncer) SyncSnapshot(ctx context.Context, environment string, snapshot []byte) {
	if !s.enabled {
		return
	}
	key := fmt.Sprintf("snapshot:%s", environment)
	url := fmt.Sprintf(
		"https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s/values/%s",
		s.accountID, s.namespaceID, key,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(snapshot))
	if err != nil {
		s.logger.Warn("edge sync: failed to build request", zap.Error(err))
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.logger.Warn("edge sync: KV write failed", zap.Error(err), zap.String("env", environment))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		s.logger.Warn("edge sync: KV returned error status",
			zap.Int("status", resp.StatusCode), zap.String("env", environment))
		return
	}
	s.logger.Debug("edge sync: snapshot written to Cloudflare KV",
		zap.String("env", environment), zap.String("key", key))
}
