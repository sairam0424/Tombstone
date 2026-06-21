package v1

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type SnapshotProxy struct {
	rdb        *redis.Client
	flagAPIURL string
	logger     *zap.Logger
}

func NewSnapshotProxy(rdb *redis.Client, flagAPIURL string, logger *zap.Logger) *SnapshotProxy {
	return &SnapshotProxy{rdb: rdb, flagAPIURL: flagAPIURL, logger: logger}
}

// GetSnapshot handles GET /api/v1/snapshot?environment={env}
// Caches the flag-api snapshot in Redis for 60 seconds to reduce load on flag-api.
func (s *SnapshotProxy) GetSnapshot(w http.ResponseWriter, r *http.Request) {
	env := r.URL.Query().Get("environment")
	if env == "" {
		env = "production"
	}

	cacheKey := "env:" + env + ":snapshot"

	// Try Redis cache first
	cached, err := s.rdb.Get(r.Context(), cacheKey).Bytes()
	if err == nil && len(cached) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cached)
		return
	}

	// Fetch from flag-api
	upstream := s.flagAPIURL + "/api/v1/environments/snapshot?environment=" + env
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		s.logger.Error("build snapshot request", zap.Error(err))
		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusBadGateway)
		return
	}
	// Forward auth header
	req.Header.Set("Authorization", r.Header.Get("Authorization"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.logger.Error("fetch snapshot", zap.Error(err))
		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, `{"error":"read failed"}`, http.StatusInternalServerError)
		return
	}

	if resp.StatusCode == http.StatusOK {
		// Cache for 60 seconds
		_ = s.rdb.Set(r.Context(), cacheKey, body, 60*time.Second).Err()
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
