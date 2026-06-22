package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tombstone/gateway/internal/hub"
	"go.uber.org/zap"
)

const (
	defaultPort      = "8090"
	heartbeatPeriod  = 30 * time.Second
	sseReconnectWait = 2 * time.Second
	sseMaxBackoff    = 60 * time.Second
	snapshotTimeout  = 10 * time.Second
)

// RelayConfig holds all configuration for a RelayProxy instance.
type RelayConfig struct {
	// GatewayURL is the upstream Tombstone gateway base URL (e.g. "http://gateway:8080").
	GatewayURL string

	// RedisURL is the Redis connection string for the local hub broadcaster.
	// Optional: if empty the relay runs hub-only (no broadcaster).
	RedisURL string

	// SnapshotDir is the directory where environment snapshots are persisted to
	// disk for air-gapped / offline deployments.
	SnapshotDir string

	// Port is the local HTTP port the relay proxy listens on.
	// Defaults to "8090" when empty.
	Port string

	// Token is the Bearer token used to authenticate with the upstream gateway.
	Token string

	// Environment is the primary environment this relay is tracking.
	// Used when the caller does not supply an ?environment= query param.
	Environment string
}

// effectivePort returns the port to bind, applying the default when unset.
func (c *RelayConfig) effectivePort() string {
	if c.Port == "" {
		return defaultPort
	}
	return c.Port
}

// RelayProxy opens a single SSE connection to the upstream Tombstone gateway,
// maintains a local in-memory cache backed by the hub, and serves multiple
// SDK instances locally. When the control plane is unreachable the relay
// continues serving from its cache (and disk fallback if SnapshotDir is set).
type RelayProxy struct {
	config RelayConfig

	// localHub fans out upstream events to all locally-connected SDK clients.
	localHub *hub.Hub

	// cache stores the latest raw snapshot JSON per environment.
	cacheMu sync.RWMutex
	cache   map[string][]byte

	// connected is true while the upstream SSE stream is healthy.
	connected bool
	connMu    sync.RWMutex

	logger *zap.Logger
}

// NewRelayProxy constructs a RelayProxy. The proxy is idle until Start is called.
func NewRelayProxy(config RelayConfig, logger *zap.Logger) *RelayProxy {
	return &RelayProxy{
		config:   config,
		localHub: hub.NewHub(logger),
		cache:    make(map[string][]byte),
		logger:   logger,
	}
}

// Start initialises the relay:
//  1. Fetches the initial snapshot from the upstream gateway.
//  2. Opens a persistent SSE stream to the upstream gateway, forwarding events
//     into the local hub.
//  3. Starts a local HTTP server on config.Port serving /health,
//     /api/v1/stream (local SSE), and /api/v1/snapshot (cached).
//
// Start blocks until ctx is cancelled.
func (rp *RelayProxy) Start(ctx context.Context) error {
	env := rp.config.Environment
	if env == "" {
		env = "production"
	}

	// Fetch initial snapshot so the cache is warm before serving traffic.
	if snap, err := rp.fetchUpstreamSnapshot(ctx, env); err != nil {
		rp.logger.Warn("initial snapshot fetch failed; will serve stale/disk fallback",
			zap.Error(err), zap.String("env", env))
	} else {
		rp.setCache(env, snap)
		if rp.config.SnapshotDir != "" {
			if err := rp.PersistSnapshot(env, snap); err != nil {
				rp.logger.Warn("persist initial snapshot", zap.Error(err))
			}
		}
	}

	// Stream upstream SSE events into the local hub in the background.
	go rp.runUpstreamStream(ctx)

	// Build local HTTP server.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", rp.ServeHealth)
	mux.HandleFunc("/api/v1/stream", rp.ServeLocalStream)
	mux.HandleFunc("/api/v1/snapshot", rp.ServeLocalSnapshot)

	srv := &http.Server{
		Addr:         ":" + rp.config.effectivePort(),
		Handler:      mux,
		ReadTimeout:  0, // SSE connections are long-lived
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	// Shutdown server when context is cancelled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if rp.config.Token == "" {
		rp.logger.Warn("RELAY_TOKEN not set: relay is running without Bearer token validation; " +
			"any process that can reach the relay port will receive the full flag snapshot — " +
			"set RELAY_TOKEN in production to restrict access")
	}

	rp.logger.Info("relay proxy starting",
		zap.String("port", rp.config.effectivePort()),
		zap.String("upstream", rp.config.GatewayURL),
		zap.Bool("token_auth_enabled", rp.config.Token != ""))

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("relay local server: %w", err)
	}
	return nil
}

// runUpstreamStream dials the upstream SSE endpoint and re-broadcasts every
// received event into the local hub. Reconnects with capped exponential
// backoff so a transient upstream outage does not kill the relay.
func (rp *RelayProxy) runUpstreamStream(ctx context.Context) {
	backoff := sseReconnectWait
	for {
		if ctx.Err() != nil {
			return
		}
		err := rp.connectUpstreamSSE(ctx)
		rp.setConnected(false)

		if ctx.Err() != nil {
			return
		}
		rp.logger.Warn("upstream SSE disconnected, reconnecting",
			zap.Error(err), zap.Duration("backoff", backoff))

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		if backoff < sseMaxBackoff {
			backoff *= 2
			if backoff > sseMaxBackoff {
				backoff = sseMaxBackoff
			}
		}
	}
}

// connectUpstreamSSE opens a single SSE session to the upstream gateway and
// reads events until the connection drops or ctx is cancelled.
func (rp *RelayProxy) connectUpstreamSSE(ctx context.Context) error {
	env := rp.config.Environment
	if env == "" {
		env = "production"
	}

	url := rp.config.GatewayURL + "/api/v1/stream?environment=" + env

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build upstream SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if rp.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+rp.config.Token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("dial upstream SSE: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream SSE returned HTTP %d", resp.StatusCode)
	}

	rp.setConnected(true)
	rp.logger.Info("upstream SSE connected", zap.String("url", url))

	return rp.readSSEStream(ctx, env, resp.Body)
}

// readSSEStream parses the SSE wire format line by line and forwards flag events
// into the local hub. It returns when the body is exhausted or ctx is done.
func (rp *RelayProxy) readSSEStream(ctx context.Context, env string, body io.Reader) error {
	scanner := bufio.NewScanner(body)

	var eventType string
	var dataBuf bytes.Buffer

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))

		case strings.HasPrefix(line, "data:"):
			dataLine := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			dataBuf.WriteString(dataLine)

		case line == "":
			// Blank line marks end of an SSE event.
			if dataBuf.Len() == 0 {
				eventType = ""
				continue
			}

			rawData := dataBuf.Bytes()
			rp.handleUpstreamSSEEvent(env, eventType, rawData)

			eventType = ""
			dataBuf.Reset()
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("SSE scanner: %w", err)
	}
	return io.EOF
}

// handleUpstreamSSEEvent decodes a received SSE event and fans it out to local
// hub subscribers. Snapshot and kill-switch events also refresh the cache.
func (rp *RelayProxy) handleUpstreamSSEEvent(env, eventType string, data []byte) {
	switch eventType {
	case "connected", "heartbeat":
		// No hub broadcast needed for meta events.
		rp.logger.Debug("upstream meta event", zap.String("event", eventType))
		return

	case "snapshot":
		// Full snapshot refresh — update cache and persist.
		rp.setCache(env, data)
		if rp.config.SnapshotDir != "" {
			if err := rp.PersistSnapshot(env, data); err != nil {
				rp.logger.Warn("persist snapshot update", zap.Error(err))
			}
		}
		return
	}

	// For flag_updated, kill_switch, and any other event types: unmarshal and
	// broadcast to local hub clients.
	var event hub.FlagEvent
	if err := json.Unmarshal(data, &event); err != nil {
		rp.logger.Warn("unmarshal upstream event",
			zap.String("type", eventType), zap.Error(err))
		return
	}

	// Use the environment from the event payload when present; fall back to the
	// stream's environment so subscribers always receive a non-empty value.
	broadcastEnv := event.Environment
	if broadcastEnv == "" {
		broadcastEnv = env
		event.Environment = env
	}

	rp.localHub.Broadcast(broadcastEnv, event)
}

// ServeHealth handles GET /health.
func (rp *RelayProxy) ServeHealth(w http.ResponseWriter, r *http.Request) {
	rp.connMu.RLock()
	connected := rp.connected
	rp.connMu.RUnlock()

	counts := rp.localHub.AllConnectionCounts()
	status := "ok"
	if !connected {
		status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	_ = enc.Encode(map[string]any{
		"status":            status,
		"upstream_connected": connected,
		"connections":       counts,
	})
}

// ServeLocalStream handles GET /api/v1/stream on the relay's local HTTP server.
// It mirrors the exact SSE protocol that the upstream gateway uses so existing
// SDK clients need no changes — they simply point to the relay address instead.
func (rp *RelayProxy) ServeLocalStream(w http.ResponseWriter, r *http.Request) {
	environment := r.URL.Query().Get("environment")
	if environment == "" {
		environment = rp.config.Environment
	}
	if environment == "" {
		environment = "production"
	}

	// Validate the Bearer token supplied by the local SDK client.
	// When config.Token is set every request must present a matching token.
	// When config.Token is empty the relay runs in open/dev mode (no enforcement).
	authHeader := r.Header.Get("Authorization")
	if rp.config.Token != "" {
		// Strict mode: token must be present and must match exactly.
		if !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, `{"error":"missing Authorization header"}`, http.StatusUnauthorized)
			return
		}
		provided := strings.TrimPrefix(authHeader, "Bearer ")
		if provided != rp.config.Token {
			http.Error(w, `{"error":"invalid Bearer token"}`, http.StatusUnauthorized)
			return
		}
	} else {
		// Dev/compat mode: no token configured — still require the header format
		// so SDK clients are aware they should pass auth, but do not reject them.
		if authHeader != "" && !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, `{"error":"malformed Authorization header; expected Bearer scheme"}`, http.StatusUnauthorized)
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	connectedData, _ := json.Marshal(map[string]any{
		"environment": environment,
		"relay":       true,
		"ts":          time.Now().Unix(),
	})
	fmt.Fprintf(w, "event: connected\ndata: %s\n\n", connectedData)
	flusher.Flush()

	ch := rp.localHub.Subscribe(environment)
	defer rp.localHub.Unsubscribe(environment, ch)

	heartbeat := time.NewTicker(heartbeatPeriod)
	defer heartbeat.Stop()

	rp.logger.Debug("local SSE client connected", zap.String("env", environment))

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			eventType := "flag_updated"
			if !event.Enabled && (event.Reason == "circuit_breaker" ||
				event.Reason == "manual_kill_switch" ||
				event.Reason == "slo_burn_rate") {
				eventType = "kill_switch"
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, payload)
			flusher.Flush()

		case <-heartbeat.C:
			fmt.Fprintf(w, "event: heartbeat\ndata: {\"ts\":%d}\n\n", time.Now().Unix())
			flusher.Flush()

		case <-r.Context().Done():
			rp.logger.Debug("local SSE client disconnected", zap.String("env", environment))
			return
		}
	}
}

// ServeLocalSnapshot handles GET /api/v1/snapshot on the relay's local HTTP
// server. It returns the cached snapshot JSON. If the in-memory cache is empty
// (e.g. the relay just restarted before receiving a snapshot) it falls back to
// reading from SnapshotDir on disk.
func (rp *RelayProxy) ServeLocalSnapshot(w http.ResponseWriter, r *http.Request) {
	env := r.URL.Query().Get("environment")
	if env == "" {
		env = rp.config.Environment
	}
	if env == "" {
		env = "production"
	}

	data := rp.getCache(env)

	if len(data) == 0 && rp.config.SnapshotDir != "" {
		// Try disk fallback for air-gapped / just-restarted scenarios.
		diskData, err := rp.loadSnapshotFromDisk(env)
		if err != nil {
			rp.logger.Warn("disk snapshot fallback failed",
				zap.String("env", env), zap.Error(err))
		} else {
			data = diskData
			// Warm in-memory cache from disk.
			rp.setCache(env, data)
		}
	}

	if len(data) == 0 {
		http.Error(w, `{"error":"snapshot not yet available"}`, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Relay-Cache", "HIT")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// PersistSnapshot writes snapshot data to SnapshotDir/<environment>.json so
// the relay can survive a full process restart in air-gapped deployments.
func (rp *RelayProxy) PersistSnapshot(environment string, data []byte) error {
	if rp.config.SnapshotDir == "" {
		return nil
	}
	if err := os.MkdirAll(rp.config.SnapshotDir, 0o755); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}
	path := filepath.Join(rp.config.SnapshotDir, environment+".json")
	// Write atomically: temp file + rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp snapshot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename snapshot: %w", err)
	}
	rp.logger.Debug("snapshot persisted", zap.String("path", path))
	return nil
}

// fetchUpstreamSnapshot performs an HTTP GET to the upstream gateway's snapshot
// endpoint and returns the raw JSON body.
func (rp *RelayProxy) fetchUpstreamSnapshot(ctx context.Context, environment string) ([]byte, error) {
	url := rp.config.GatewayURL + "/api/v1/snapshot?environment=" + environment

	reqCtx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build snapshot request: %w", err)
	}
	if rp.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+rp.config.Token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch upstream snapshot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream snapshot returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read snapshot body: %w", err)
	}
	return body, nil
}

// --------------------------------------------------------------------------
// Internal cache helpers
// --------------------------------------------------------------------------

func (rp *RelayProxy) setCache(env string, data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	rp.cacheMu.Lock()
	rp.cache[env] = cp
	rp.cacheMu.Unlock()
}

func (rp *RelayProxy) getCache(env string) []byte {
	rp.cacheMu.RLock()
	data := rp.cache[env]
	rp.cacheMu.RUnlock()
	if data == nil {
		return nil
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp
}

func (rp *RelayProxy) setConnected(v bool) {
	rp.connMu.Lock()
	rp.connected = v
	rp.connMu.Unlock()
}

func (rp *RelayProxy) loadSnapshotFromDisk(env string) ([]byte, error) {
	path := filepath.Join(rp.config.SnapshotDir, env+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read disk snapshot %s: %w", path, err)
	}
	return data, nil
}
