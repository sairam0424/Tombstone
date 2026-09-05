package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	apiv1 "github.com/tombstone/evaluator/internal/api/v1"
	"github.com/tombstone/evaluator/internal/blast"
	"github.com/tombstone/evaluator/internal/circuit"
	"github.com/tombstone/evaluator/internal/health"
	"github.com/tombstone/evaluator/internal/middleware"
	"github.com/tombstone/evaluator/internal/notify"
	"github.com/tombstone/evaluator/internal/rollback"
	"github.com/tombstone/evaluator/internal/telemetry"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	initCtx := context.Background()

	// Initialise OpenTelemetry. OTLP_ENDPOINT is optional — noop when unset.
	shutdownTracer, err := telemetry.InitTracer(initCtx, "evaluator")
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
		logger.Fatal("REDIS_URL required")
	}
	flagAPIURL := os.Getenv("FLAG_API_URL")
	if flagAPIURL == "" {
		logger.Fatal("FLAG_API_URL required")
	}
	flagAPIToken := os.Getenv("FLAG_API_TOKEN")
	if flagAPIToken == "" {
		logger.Fatal("FLAG_API_TOKEN required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		logger.Fatal("parse redis url", zap.Error(err))
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		logger.Fatal("ping redis", zap.Error(err))
	}

	// DB is optional for blast radius — connect if available
	var db *sql.DB
	if dbURL := os.Getenv("DB_URL"); dbURL != "" {
		db, _ = sql.Open("postgres", dbURL)
		if db != nil {
			db.SetMaxOpenConns(3) // Neon free tier — evaluator uses DB rarely
			db.SetMaxIdleConns(1)
			db.SetConnMaxLifetime(5 * time.Minute) // recycle before Neon idle timeout
			db.SetConnMaxIdleTime(2 * time.Minute)
		}
	}

	rateMw := middleware.NewRateLimitMiddleware(rdb)
	defer rateMw.Stop()
	loadShedMw := middleware.NewLoadShedMiddleware(middleware.DefaultLoadShedConfig(), logger)

	breaker := circuit.NewBreaker(rdb, logger)
	exec := rollback.NewExecutor(flagAPIURL, flagAPIToken, rdb, logger)
	// EVAL-2: rollback.Execute has always disabled the flag and published a
	// Redis event on trip, but never notified anyone -- a human on-call
	// previously learned about an auto-rollback only by noticing the
	// dashboard or audit log, not proactively. dashboardURL/slackNotifier
	// are both safe to construct even when unset/empty: an empty
	// dashboardURL just produces a relative "/flags/{key}" link, and
	// NotifyRollback itself no-ops (logged, not fatal) when
	// SLACK_WEBHOOK_URL is unset -- matches this service's existing
	// posture of treating third-party notification config as optional,
	// unlike REDIS_URL/FLAG_API_URL/FLAG_API_TOKEN above.
	dashboardURL := os.Getenv("DASHBOARD_URL")
	slackNotifier := notify.NewSlackNotifier(os.Getenv("SLACK_WEBHOOK_URL"), logger)
	agg := telemetry.NewAggregator(breaker, rdb, logger)
	agg.OnTrip = func(flagKey, env string, errorRate float64) {
		ctx := context.Background()
		execErr := exec.Execute(ctx, rollback.RollbackRequest{
			FlagKey:     flagKey,
			Environment: env,
			Reason:      "circuit_breaker",
			ErrorRate:   errorRate,
			TriggeredBy: "circuit_breaker",
		})
		if execErr != nil {
			logger.Error("auto-rollback execution failed", zap.Error(execErr),
				zap.String("flag", flagKey), zap.String("env", env))
		}
		// Deliberately NOT notifying Slack when Execute failed: NotifyRollback's
		// message says the flag "has been automatically disabled" -- sending
		// that on failure would tell an on-call engineer the exact opposite of
		// what happened. A rollback-FAILURE alert is a real, valuable, but
		// SEPARATE notification (different message, arguably higher urgency)
		// needing its own scope decision, not silently folded into this slice.
		shouldNotify, rollbackURL := shouldNotifySlack(execErr, dashboardURL, flagKey)
		if !shouldNotify {
			return
		}
		// Dispatched off the hot rollback path, on its own bounded context:
		// Aggregator.Flush processes every flag that tripped in the current
		// window sequentially on one goroutine, so a blocking inline call
		// here would stack up to NotifyRollback's own 10s http.Client timeout
		// PER flag before Flush can even reach the next flag's real
		// kill-switch call -- exactly the scenario auto-rollback exists to
		// react to quickly. Matches internal/transparency/rekor.go's own
		// async dispatch for the same class of best-effort, non-critical
		// third-party call: an independent context that outlives this
		// closure, not a deadline inherited from anything upstream.
		go func() {
			notifyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			slackNotifier.NotifyRollback(notifyCtx, flagKey, env, errorRate, "circuit_breaker", rollbackURL)
		}()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agg.Run(ctx)

	var blastCalc *blast.Calculator
	if db != nil {
		blastCalc = blast.NewCalculator(db, flagAPIURL)
	}

	// OBS-1 (rollout): pull-based, no OTLP_ENDPOINT/collector needed —
	// unlike InitTracer, there's no "unset means no-op" branch. Mirrors
	// flag-api/gateway's OBS-1 slices exactly (same middleware, same
	// metric names/labels) so every service's RED metrics are directly
	// comparable in one dashboard.
	meter, metricsHandler, err := telemetry.InitMeter("evaluator")
	if err != nil {
		logger.Fatal("init meter", zap.Error(err))
	}
	httpMetrics, err := telemetry.HTTPMetrics(meter)
	if err != nil {
		logger.Fatal("init http metrics middleware", zap.Error(err))
	}

	r := chi.NewRouter()
	r.Use(chiMiddleware.Recoverer)
	r.Use(httpMetrics)
	r.Use(rateMw.RateLimit)
	// Load shedding runs AFTER rate limiting: rate limiting rejects
	// over-quota callers first, regardless of system load; load shedding
	// then additionally rejects when the service itself is saturated,
	// regardless of any individual caller's quota standing.
	r.Use(loadShedMw.LoadShed)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	// DB is optional for evaluator (see above) — Checker treats a nil *sql.DB
	// as healthy, so readiness never fails on an absent-and-optional dependency.
	healthChecker := &health.Checker{DB: db, RDB: rdb}
	r.Get("/readyz", healthChecker.Readyz)

	// OBS-1: Prometheus scrape endpoint — public, no auth middleware,
	// matching /health and /readyz above.
	r.Get("/metrics", metricsHandler.ServeHTTP)

	// SDK telemetry ingest endpoint
	r.Post("/api/v1/telemetry", func(w http.ResponseWriter, r *http.Request) {
		var events []telemetry.TelemetryEvent
		if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, e := range events {
			agg.Record(e)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Manual rollback endpoint
	r.Post("/api/v1/rollback", func(w http.ResponseWriter, r *http.Request) {
		var req rollback.RollbackRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := exec.Execute(r.Context(), req); err != nil {
			logger.Error("rollback failed", zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"rolled_back":true}`)
	})

	// Blast radius endpoint (only mounted when DB is available)
	if blastCalc != nil {
		r.Get("/api/v1/blast-radius", blast.HandleBlastRadius(blastCalc))
	}

	// Circuit breaker state endpoint. Circuit state is environment-scoped, so the
	// caller selects the environment via ?environment=; defaults to production.
	r.Get("/api/v1/circuit/{flagKey}", func(w http.ResponseWriter, r *http.Request) {
		flagKey := chi.URLParam(r, "flagKey")
		env := r.URL.Query().Get("environment")
		if env == "" {
			env = "production"
		}
		state := breaker.GetState(r.Context(), flagKey, env)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"flag_key":%q,"environment":%q,"state":%q}`, flagKey, env, state)
	})

	// Per-flag SLO dashboard endpoint
	sloHandler := apiv1.NewHandler(rdb, breaker, logger)
	r.Get("/api/v1/flags/{key}/slo", sloHandler.HandleFlagSLO)

	srv := &http.Server{
		Addr: ":" + port,
		// Wrap the router with otelhttp for automatic HTTP trace spans.
		Handler:      otelhttp.NewHandler(r, "evaluator"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	go func() {
		logger.Info("evaluator starting", zap.String("port", port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
}

// shouldNotifySlack decides whether agg.OnTrip's auto-rollback should
// trigger a Slack alert, and if so, builds the "View in Dashboard" URL.
// Kept as a pure function, separate from OnTrip's actual IO
// (exec.Execute/slackNotifier.NotifyRollback), specifically so this
// success/failure branching has direct unit test coverage without a live
// Redis/HTTP server -- unlike the OBS-1/tracer wiring in this same file,
// which has no equivalent extraction and relies on source-parsing regex
// guards (see main_test.go) instead.
//
// A non-nil execErr must produce shouldNotify=false: NotifyRollback's
// message says the flag "has been automatically disabled", which is only
// true when Execute actually succeeded.
func shouldNotifySlack(execErr error, dashboardURL, flagKey string) (shouldNotify bool, rollbackURL string) {
	if execErr != nil {
		return false, ""
	}
	// Trim any trailing slash so a DASHBOARD_URL configured either way
	// (with or without one) produces "https://host/flags/key", never
	// "https://host//flags/key" -- the double slash workspace-dashboard's
	// React Router "/flags/:key" route does not match, which would 404.
	dashboardURL = strings.TrimSuffix(dashboardURL, "/")
	return true, fmt.Sprintf("%s/flags/%s", dashboardURL, url.PathEscape(flagKey))
}
