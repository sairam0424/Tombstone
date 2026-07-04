package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	v1 "github.com/tombstone/gateway/internal/api/v1"
	"github.com/tombstone/gateway/internal/health"
	"github.com/tombstone/gateway/internal/hub"
	"github.com/tombstone/gateway/internal/telemetry"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	initCtx := context.Background()

	// Initialise OpenTelemetry. OTLP_ENDPOINT is optional — noop when unset.
	shutdownTracer, err := telemetry.InitTracer(initCtx, "gateway")
	if err != nil {
		logger.Fatal("init tracer", zap.Error(err))
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracer(shutdownCtx)
	}()

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

	// Seed consumer groups for known environments. New environments auto-register
	// on first XREADGROUP call (MKSTREAM). Start one stream reader per environment.
	knownEnvs := []string{"development", "staging", "production"}
	hub.CreateConsumerGroups(ctx, rdb, knownEnvs, logger)
	for _, env := range knownEnvs {
		env := env // capture loop variable
		go broadcaster.RunStreamConsumer(ctx, env)
	}
	logger.Info("Redis Streams consumers started", zap.Strings("environments", knownEnvs))

	// Reclaim sweep: every 15s (roughly matching reclaimIdleThreshold's
	// scale), scan each environment's PEL for entries a poison-message
	// unmarshal failure left pending and either XCLAIM-retry them or
	// dead-letter them once they exceed maxDeliveryAttempts. See dlq.go.
	go runReclaimLoop(ctx, broadcaster, knownEnvs, logger)

	sseH := v1.NewSSEHandler(h, logger)
	snapH := v1.NewSnapshotProxy(rdb, flagAPIURL, logger)
	metricsH := v1.NewGatewayMetricsHandler(h, logger)
	dlqH := v1.NewDLQHandler(rdb, logger)

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
	}))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		counts := h.AllConnectionCounts()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","connections":%v}`, counts)
	})

	// Gateway has no Postgres dependency; readiness is gated on Redis only.
	healthChecker := &health.Checker{RDB: rdb}
	r.Get("/readyz", healthChecker.Readyz)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/stream", sseH.Stream)
		r.Get("/snapshot", snapH.GetSnapshot)
		r.Route("/gateway", func(r chi.Router) {
			r.Get("/metrics", metricsH.GetMetrics)
		})
	})

	// Internal DLQ inspection/replay routes — not part of the public SDK
	// surface. Deliberately unauthenticated at this layer today (same trust
	// boundary as /health); add auth middleware here before exposing this
	// beyond an internal network.
	r.Route("/internal/dlq/{environment}", func(r chi.Router) {
		r.Get("/", dlqH.ListDLQ)
		r.Post("/replay", dlqH.ReplayDLQ)
	})

	srv := &http.Server{
		Addr: ":" + port,
		// Wrap the router with otelhttp for automatic HTTP trace spans.
		Handler:      otelhttp.NewHandler(r, "gateway"),
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

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	cancel() // stop broadcaster
	logger.Info("shutting down gateway")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}

// reclaimTickInterval controls how often ReclaimStalePending sweeps each
// environment's PEL. 15s gives ~2 sweeps per reclaimIdleThreshold window
// (30s) — frequent enough that a poison message doesn't sit unclaimed for
// long, without turning the sweep into a hot loop.
const reclaimTickInterval = 15 * time.Second

// runReclaimLoop periodically calls ReclaimStalePending for every known
// environment's primary stream until ctx is cancelled. Runs alongside
// RunStreamConsumer's per-environment goroutines, not instead of them.
func runReclaimLoop(ctx context.Context, b *hub.Broadcaster, environments []string, logger *zap.Logger) {
	ticker := time.NewTicker(reclaimTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, env := range environments {
				streamKey := hub.StreamKey(env)
				if err := b.ReclaimStalePending(ctx, streamKey); err != nil {
					logger.Warn("reclaim sweep failed",
						zap.String("stream", streamKey), zap.Error(err))
				}
			}
		}
	}
}
