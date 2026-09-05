package main

import (
	"context"
	"crypto/subtle"
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
	// EVAL-4: OnTrip's binary "trip -> immediately call the full kill
	// switch" became a stepped ladder -- targetPct==0 still calls the SAME
	// exec.Execute kill switch as before (identical audit/event semantics
	// at the ladder's terminal step); every intermediate, non-zero step
	// calls exec.SetRolloutPct against flag-api's new graduated
	// rollback-step endpoint instead. Only on a SUCCESSFUL API call does
	// Aggregator commit the new step/state to Redis (see Flush's own
	// handlers) -- this callback's bool return is exactly that signal.
	agg.OnRolloutChange = func(flagKey, env string, targetPct int, errorRate float64, phase circuit.RolloutPhase) bool {
		ctx := context.Background()
		var err error
		switch rolloutActionFor(targetPct, phase) {
		case rolloutActionKill:
			err = exec.Execute(ctx, rollback.RollbackRequest{
				FlagKey:     flagKey,
				Environment: env,
				Reason:      "circuit_breaker",
				ErrorRate:   errorRate,
				TriggeredBy: "circuit_breaker",
			})
		case rolloutActionIncrease:
			err = exec.IncreaseRolloutPct(ctx, flagKey, env, targetPct, "circuit_breaker")
		case rolloutActionDecrease:
			err = exec.SetRolloutPct(ctx, flagKey, env, targetPct, "circuit_breaker")
		}
		if err != nil {
			logger.Error("rollout change failed", zap.Error(err),
				zap.String("flag", flagKey), zap.String("env", env),
				zap.Int("target_pct", targetPct), zap.String("phase", string(phase)))
			return false
		}

		// Only the ladder's terminal 0% step -- a genuine "flag now fully
		// disabled" event, matching NotifyRollback's own message text --
		// triggers a Slack alert. Richer per-phase alerting (trip-started,
		// recovery-succeeded, recovery-reverted) is a real, valuable, but
		// SEPARATE notification need (each its own message shape) that
		// this slice deliberately leaves for a follow-up rather than
		// expanding notify.go's surface here.
		if phase == circuit.PhaseKilled {
			shouldNotify, rollbackURL := shouldNotifySlack(nil, dashboardURL, flagKey)
			if shouldNotify {
				// Dispatched off the hot rollback path, on its own bounded
				// context: Aggregator.Flush processes every flag that
				// tripped in the current window sequentially on one
				// goroutine, so a blocking inline call here would stack up
				// to NotifyRollback's own 10s http.Client timeout PER flag
				// before Flush can even reach the next flag's real
				// kill-switch call -- exactly the scenario auto-rollback
				// exists to react to quickly. Matches internal/
				// transparency/rekor.go's own async dispatch for the same
				// class of best-effort, non-critical third-party call.
				go func() {
					notifyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					slackNotifier.NotifyRollback(notifyCtx, flagKey, env, errorRate, "circuit_breaker", rollbackURL)
				}()
			}
		}
		return true
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

	// Manual rollback endpoint. EVAL-4: this was previously reachable by
	// anyone who could reach the evaluator's port at all -- no auth
	// whatsoever, unlike every mutating flag-api endpoint it ends up
	// calling into. Guarded with the SAME shared secret (FLAG_API_TOKEN)
	// evaluator already holds for its own outbound calls, reused here as
	// the credential for triggering a rollback INBOUND -- not a general
	// RBAC system, but a meaningful improvement over completely open for
	// what this doc comment's own title calls a safety net of last resort.
	r.Post("/api/v1/rollback", func(w http.ResponseWriter, r *http.Request) {
		if !isAuthorizedManualRollback(r, flagAPIToken) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
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

// rolloutAction identifies which flag-api endpoint (and direction
// semantics) an OnRolloutChange call should use.
type rolloutAction int

const (
	// rolloutActionKill calls the binary kill switch (Executor.Execute) --
	// used for the ladder's terminal 0% step in EITHER direction (a full
	// descent, or a HALF_OPEN probe reverting).
	rolloutActionKill rolloutAction = iota
	// rolloutActionIncrease calls the recovery-step endpoint
	// (Executor.IncreaseRolloutPct) -- flag-api's rollback-step endpoint
	// can never increase exposure by design, so every HALF_OPEN recovery
	// step (which is by definition an increase) MUST go through this
	// separate, mirror-image endpoint instead (fix for a HIGH finding from
	// adversarial review of PR #221: routing every non-zero step through
	// rollback-step meant the recovery ladder could never actually climb --
	// every probe was unconditionally rejected).
	rolloutActionIncrease
	// rolloutActionDecrease calls the rollback-step endpoint
	// (Executor.SetRolloutPct) -- every other non-zero step, i.e. the
	// descent ladder's intermediate rungs.
	rolloutActionDecrease
)

// rolloutActionFor decides the action for a given OnRolloutChange call.
// Extracted as a pure function specifically so this dispatch -- the exact
// logic that was wrong before the PR #221 fix above -- has direct unit
// test coverage without needing a live flag-api server, matching this
// file's own shouldNotifySlack/isAuthorizedManualRollback convention.
func rolloutActionFor(targetPct int, phase circuit.RolloutPhase) rolloutAction {
	switch {
	case targetPct == 0:
		return rolloutActionKill
	case phase == circuit.PhaseRecovering || phase == circuit.PhaseRecovered:
		return rolloutActionIncrease
	default:
		return rolloutActionDecrease
	}
}

// isAuthorizedManualRollback requires a Bearer token matching expectedToken
// (FLAG_API_TOKEN in production) -- see the route registration's own
// comment for why this specific shared secret was reused rather than
// building a dedicated credential. subtle.ConstantTimeCompare avoids a
// timing side-channel a plain "==" comparison would have; ConstantTimeCompare
// itself first checks length equality in non-constant time, which is fine
// here since token length alone leaks nothing an attacker doesn't already
// know (both this token and FLAG_API_TOKEN are operator-configured, not a
// secret-length oracle over untrusted input).
func isAuthorizedManualRollback(r *http.Request, expectedToken string) bool {
	if expectedToken == "" {
		return false
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	token := strings.TrimPrefix(auth, prefix)
	return subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) == 1
}

// shouldNotifySlack decides whether agg.OnRolloutChange's auto-rollback should
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
