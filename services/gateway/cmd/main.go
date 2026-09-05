package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
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
	// FLAG_API_TOKEN authenticates the snapshot reconciler's calls to flag-api.
	// Optional: the reconciler is belt-and-suspenders insurance against the
	// dual-write gap, not a primary delivery path, so its absence only
	// disables the reconciler rather than failing gateway startup.
	flagAPIToken := os.Getenv("FLAG_API_TOKEN")
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
	h.SetRedis(rdb) // GW-2: enables ReplayOrSnapshot's catch-up-on-reconnect
	broadcaster := hub.NewBroadcaster(rdb, h, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go broadcaster.Run(ctx)

	// Seed THIS REPLICA'S OWN consumer group (GW-1) for known environments.
	// New environments auto-register on first XREADGROUP call (MKSTREAM).
	// Start one stream reader per environment.
	knownEnvs := []string{"development", "staging", "production"}
	hub.CreateConsumerGroups(ctx, rdb, knownEnvs, broadcaster.Group(), logger)
	var streamConsumersWG sync.WaitGroup
	for _, env := range knownEnvs {
		env := env // capture loop variable
		streamConsumersWG.Add(1)
		go func() {
			defer streamConsumersWG.Done()
			broadcaster.RunStreamConsumer(ctx, env)
		}()
	}
	logger.Info("Redis Streams consumers started",
		zap.Strings("environments", knownEnvs), zap.String("group", broadcaster.Group()))

	// Snapshot reconciler: low-frequency (5 min) belt-and-suspenders poll of
	// flag-api's snapshot per environment, to recover from the dual-write gap
	// (Postgres commit succeeds, Redis publish/XADD is lost). Runs ALONGSIDE
	// broadcaster.Run/RunStreamConsumer above — never instead of them.
	if flagAPIToken != "" {
		reconciler := hub.NewReconciler(h, flagAPIURL, flagAPIToken, logger)
		go reconciler.Run(ctx, knownEnvs)
	} else {
		logger.Warn("FLAG_API_TOKEN not set — snapshot reconciler disabled")
	}

	// Reclaim sweep: every 15s (roughly matching reclaimIdleThreshold's
	// scale), scan each environment's PEL for entries a poison-message
	// unmarshal failure left pending and either XCLAIM-retry them or
	// dead-letter them once they exceed maxDeliveryAttempts. See dlq.go.
	go runReclaimLoop(ctx, broadcaster, knownEnvs, logger)

	// GW-1: idle-group GC, a much slower sweep than reclaim above (this is
	// about abandoned GROUPS, not stuck MESSAGES) — the backstop for a
	// replica that died without running the graceful-shutdown destroy
	// below. Any live replica can run this against any environment's
	// stream; it is not scoped to this replica's own group.
	go runGroupGCLoop(ctx, rdb, knownEnvs, logger)

	sseH := v1.NewSSEHandler(h, logger)
	snapH := v1.NewSnapshotProxy(rdb, flagAPIURL, logger)
	metricsH := v1.NewGatewayMetricsHandler(h, logger)
	dlqH := v1.NewDLQHandler(rdb, logger)

	// OBS-1 (gateway rollout): pull-based, no OTLP_ENDPOINT/collector
	// needed — unlike InitTracer, there's no "unset means no-op" branch.
	// Mirrors flag-api's own OBS-1 slice exactly (same middleware, same
	// metric names/labels) so both services' RED metrics are directly
	// comparable in one dashboard. Note for /api/v1/stream specifically:
	// SSE connections are long-lived, so this middleware's count/duration
	// for that route only records once a connection FINALLY closes, not
	// when it opens — "requests in flight" isn't something this RED
	// middleware surfaces for streaming routes; AllConnectionCounts
	// (/health, /api/v1/gateway/metrics) is the existing mechanism for
	// that, and stays unchanged by this rollout.
	meter, metricsHandler, err := telemetry.InitMeter("gateway")
	if err != nil {
		logger.Fatal("init meter", zap.Error(err))
	}
	httpMetrics, err := telemetry.HTTPMetrics(meter)
	if err != nil {
		logger.Fatal("init http metrics middleware", zap.Error(err))
	}

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Recoverer)
	r.Use(httpMetrics)
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

	// OBS-1: Prometheus scrape endpoint — public, no auth middleware,
	// matching /health and /readyz above. Network-level access control
	// (not app-level auth) is the expected boundary for scrape endpoints.
	// Distinct from /api/v1/gateway/metrics below, which is gateway's own
	// JSON connection-stats endpoint, not a Prometheus exposition.
	r.Get("/metrics", metricsHandler.ServeHTTP)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/stream", sseH.Stream)
		r.Get("/snapshot", snapH.GetSnapshot)
		r.Route("/gateway", func(r chi.Router) {
			r.Get("/metrics", metricsH.GetMetrics)
		})
	})

	// Internal DLQ inspection/replay routes — guarded by FLAG_API_TOKEN bearer
	// check (SEC-002). When FLAG_API_TOKEN is unset (local dev) auth is skipped
	// so local testing doesn't require a token, matching the reconciler's
	// fail-open-when-token-absent behaviour. In production FLAG_API_TOKEN is
	// always set, so the routes require the same token the reconciler uses.
	r.Route("/internal/dlq/{environment}", func(r chi.Router) {
		if flagAPIToken != "" {
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.Header.Get("Authorization") != "Bearer "+flagAPIToken {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
						return
					}
					next.ServeHTTP(w, req)
				})
			})
		}
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

	// Wait for the per-environment RunStreamConsumer goroutines to actually
	// observe cancellation and return before destroying their own consumer
	// group below. go-redis has no ctx.Done()-driven abort for an in-flight
	// blocking XREADGROUP — a goroutine can still be blocked reading against
	// this exact group for up to streamBlockDur (1s) after cancel() fires.
	// Destroying the group while that read is still in flight wakes it with
	// a NOGROUP error instead of a clean cancellation exit, and any message
	// published in that narrow window is never delivered via Streams to
	// this replica. Bounded by streamBlockDur per goroutine — not a
	// meaningful shutdown delay.
	streamConsumersWG.Wait()

	// GW-1: destroy this replica's own consumer group on every known
	// environment's stream — the fast, common-case path for a graceful
	// shutdown (rolling deploy, deliberate scale-down), so an abandoned
	// group and its PEL don't have to wait for the much slower idle-GC
	// backstop (groupIdleGCThreshold, 5m) to notice. Best-effort: a failure
	// here just means the backstop handles it instead. Runs in its own
	// goroutine, CONCURRENTLY with srv.Shutdown below rather than before
	// it, so it genuinely cannot delay when connection draining starts —
	// only shutdownWG.Wait() at the very end bounds how long main() waits
	// for it to finish, capped at 5s and normally fully absorbed by
	// srv.Shutdown's own (longer) grace period below.
	var shutdownWG sync.WaitGroup
	shutdownWG.Add(1)
	go func() {
		defer shutdownWG.Done()
		shutdownDestroyCtx, shutdownDestroyCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownDestroyCancel()
		for _, env := range knownEnvs {
			if err := rdb.XGroupDestroy(shutdownDestroyCtx, hub.StreamKey(env), broadcaster.Group()).Err(); err != nil {
				logger.Warn("shutdown: failed to destroy own consumer group",
					zap.String("stream", hub.StreamKey(env)), zap.String("group", broadcaster.Group()), zap.Error(err))
			}
		}
	}()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)

	shutdownWG.Wait()
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

// groupGCTickInterval controls how often the idle-consumer-group GC sweep
// runs. Much slower than reclaimTickInterval on purpose — this is looking
// for an entire ABANDONED GROUP (a dead replica), not a stuck message, and
// groupIdleGCThreshold (5m) already gives a wide safety margin, so there's
// no benefit to checking more often than this.
const groupGCTickInterval = 2 * time.Minute

// runGroupGCLoop periodically calls hub.GCIdleGroups for every known
// environment's primary stream until ctx is cancelled. Any live replica
// runs this against every stream, not just its own — it's cleaning up
// after replicas that are no longer around to clean up after themselves.
func runGroupGCLoop(ctx context.Context, rdb *redis.Client, environments []string, logger *zap.Logger) {
	ticker := time.NewTicker(groupGCTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, env := range environments {
				hub.GCIdleGroups(ctx, rdb, hub.StreamKey(env), logger)
			}
		}
	}
}
