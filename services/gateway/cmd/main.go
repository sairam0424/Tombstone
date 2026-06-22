package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	v1 "github.com/tombstone/gateway/internal/api/v1"
	"github.com/tombstone/gateway/internal/auth"
	"github.com/tombstone/gateway/internal/hub"
	"github.com/tombstone/gateway/internal/relay"
)

func buildAllowedOrigins() []string {
	env := os.Getenv("ALLOWED_ORIGINS")
	if env == "" {
		// Safe default for local development
		return []string{"http://localhost:3000", "http://localhost:8081", "http://127.0.0.1:3000"}
	}
	origins := strings.Split(env, ",")
	for i, o := range origins {
		origins[i] = strings.TrimSpace(o)
	}
	return origins
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		logger.Fatal("REDIS_URL environment variable is required")
	}
	flagAPIURL := os.Getenv("FLAG_API_URL")
	if flagAPIURL == "" {
		logger.Fatal("FLAG_API_URL environment variable is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		logger.Fatal("parse redis url", zap.Error(err))
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		logger.Fatal("ping redis", zap.Error(err))
	}

	h := hub.NewHub(logger)
	broadcaster := hub.NewBroadcaster(rdb, h, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go broadcaster.Run(ctx)

	sseH := v1.NewSSEHandler(h, logger)
	snapH := v1.NewSnapshotProxy(rdb, flagAPIURL, logger)

	// Read GATEWAY_AUTH_TOKEN once at startup and warn loudly if absent.
	authToken := os.Getenv("GATEWAY_AUTH_TOKEN")
	if authToken == "" {
		logger.Warn("GATEWAY_AUTH_TOKEN not set — all /api/v1 requests allowed (set for production)")
	}

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Recoverer)
	// Build CORS allowlist from environment. Default: localhost for dev.
	allowedOrigins := buildAllowedOrigins()
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: allowedOrigins,
		AllowedMethods: []string{"GET", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
	}))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		counts := h.AllConnectionCounts()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","connections":%v}`, counts)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(auth.ValidateSDKToken(authToken, logger))
		r.Get("/stream", sseH.Stream)
		r.Get("/snapshot", snapH.GetSnapshot)
	})

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  0, // SSE connections are long-lived
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.Info("gateway starting", zap.String("port", port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	// Start the embedded relay proxy when RELAY_ENABLED=true is set.
	// RELAY_TOKEN controls Bearer-token validation for local SDK clients that
	// connect to the relay port instead of the upstream gateway directly.
	if os.Getenv("RELAY_ENABLED") == "true" {
		relayCfg := buildRelayConfig(flagAPIURL)
		rp := relay.NewRelayProxy(relayCfg, logger)
		go func() {
			if err := rp.Start(ctx); err != nil {
				logger.Error("relay proxy stopped", zap.Error(err))
			}
		}()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	cancel() // stop broadcaster
	logger.Info("shutting down gateway")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}

// buildRelayConfig constructs a RelayConfig from environment variables.
//
// Recognised variables:
//
//	RELAY_TOKEN       — Bearer token that local SDK clients must supply.
//	                    Empty string disables token validation (dev/compat mode).
//	RELAY_PORT        — Local port for the relay HTTP server (default "8090").
//	RELAY_ENVIRONMENT — Default environment the relay tracks (default "production").
//	RELAY_SNAPSHOT_DIR — Optional directory for air-gapped snapshot persistence.
func buildRelayConfig(gatewayURL string) relay.RelayConfig {
	return relay.RelayConfig{
		GatewayURL:  gatewayURL,
		Token:       os.Getenv("RELAY_TOKEN"),
		Port:        os.Getenv("RELAY_PORT"),
		Environment: os.Getenv("RELAY_ENVIRONMENT"),
		SnapshotDir: os.Getenv("RELAY_SNAPSHOT_DIR"),
	}
}
