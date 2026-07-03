package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/tombstone/evaluator/internal/blast"
	"github.com/tombstone/evaluator/internal/circuit"
	"github.com/tombstone/evaluator/internal/health"
	"github.com/tombstone/evaluator/internal/middleware"
	"github.com/tombstone/evaluator/internal/rollback"
	"github.com/tombstone/evaluator/internal/telemetry"
	apiv1 "github.com/tombstone/evaluator/internal/api/v1"
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
			db.SetMaxOpenConns(3)                  // Neon free tier — evaluator uses DB rarely
			db.SetMaxIdleConns(1)
			db.SetConnMaxLifetime(5 * time.Minute) // recycle before Neon idle timeout
			db.SetConnMaxIdleTime(2 * time.Minute)
		}
	}

	rateMw := middleware.NewRateLimitMiddleware()
	defer rateMw.Stop()

	breaker := circuit.NewBreaker(rdb, logger)
	exec := rollback.NewExecutor(flagAPIURL, flagAPIToken, rdb, logger)
	agg := telemetry.NewAggregator(breaker, rdb, logger)
	agg.OnTrip = func(flagKey, env string, errorRate float64) {
		ctx := context.Background()
		_ = exec.Execute(ctx, rollback.RollbackRequest{
			FlagKey:     flagKey,
			Environment: env,
			Reason:      "circuit_breaker",
			ErrorRate:   errorRate,
			TriggeredBy: "circuit_breaker",
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agg.Run(ctx)

	var blastCalc *blast.Calculator
	if db != nil {
		blastCalc = blast.NewCalculator(db, flagAPIURL)
	}

	r := chi.NewRouter()
	r.Use(chiMiddleware.Recoverer)
	r.Use(rateMw.RateLimit)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	// DB is optional for evaluator (see above) — Checker treats a nil *sql.DB
	// as healthy, so readiness never fails on an absent-and-optional dependency.
	healthChecker := &health.Checker{DB: db, RDB: rdb}
	r.Get("/readyz", healthChecker.Readyz)

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

	// Circuit breaker state endpoint
	r.Get("/api/v1/circuit/{flagKey}", func(w http.ResponseWriter, r *http.Request) {
		flagKey := chi.URLParam(r, "flagKey")
		state := breaker.GetState(r.Context(), flagKey)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"flag_key":%q,"state":%q}`, flagKey, state)
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
