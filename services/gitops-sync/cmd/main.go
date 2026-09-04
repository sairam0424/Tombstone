package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/tombstone/gitops-sync/internal/health"
	"github.com/tombstone/gitops-sync/internal/parser"
	"github.com/tombstone/gitops-sync/internal/syncer"
	"github.com/tombstone/gitops-sync/internal/telemetry"
	"github.com/tombstone/gitops-sync/internal/validator"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	initCtx := context.Background()

	// Initialise OpenTelemetry. OTLP_ENDPOINT is optional — noop when unset.
	shutdownTracer, err := telemetry.InitTracer(initCtx, "gitops-sync")
	if err != nil {
		logger.Fatal("init tracer", zap.Error(err))
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracer(shutdownCtx)
	}()

	flagAPIURL := os.Getenv("FLAG_API_URL")
	if flagAPIURL == "" {
		logger.Fatal("FLAG_API_URL required")
	}
	apiToken := os.Getenv("FLAG_API_TOKEN")
	if apiToken == "" {
		logger.Fatal("FLAG_API_TOKEN required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8084"
	}
	projectID := os.Getenv("PROJECT_ID")
	if projectID == "" {
		projectID = "00000000-0000-0000-0000-000000000001"
	}

	s := syncer.NewSyncer(flagAPIURL, apiToken, logger)

	// CLI mode: if FLAGS_DIR env is set, run a one-shot sync and exit
	if flagsDir := os.Getenv("FLAGS_DIR"); flagsDir != "" {
		runCLISync(flagsDir, projectID, s, logger)
		return
	}

	// OBS-1 (rollout): pull-based, no OTLP_ENDPOINT/collector needed —
	// unlike InitTracer, there's no "unset means no-op" branch. Mirrors
	// flag-api/gateway/evaluator's OBS-1 slices exactly (same middleware,
	// same metric names/labels) so every service's RED metrics are
	// directly comparable in one dashboard. Placed after the CLI-mode
	// early return above — CLI mode never serves HTTP, so it has nothing
	// to instrument.
	meter, metricsHandler, err := telemetry.InitMeter("gitops-sync")
	if err != nil {
		logger.Fatal("init meter", zap.Error(err))
	}
	httpMetrics, err := telemetry.HTTPMetrics(meter)
	if err != nil {
		logger.Fatal("init http metrics middleware", zap.Error(err))
	}

	r := chi.NewRouter()
	r.Use(httpMetrics)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"ok","service":"gitops-sync"}`)
	})

	// gitops-sync has no Postgres or Redis dependency of its own — it is
	// stateless aside from calling flag-api over HTTP. Checker with both
	// fields nil always reports ready.
	healthChecker := &health.Checker{}
	r.Get("/readyz", healthChecker.Readyz)

	// OBS-1: Prometheus scrape endpoint — public, no auth middleware,
	// matching /health and /readyz above.
	r.Get("/metrics", metricsHandler.ServeHTTP)

	// POST /api/v1/sync — triggered by CI/CD webhook on merge
	r.Post("/api/v1/sync", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FlagsDir  string `json:"flags_dir"`
			ProjectID string `json:"project_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.ProjectID == "" {
			req.ProjectID = projectID
		}

		defs, parseErrs := parser.ParseDirectory(req.FlagsDir)
		if len(parseErrs) > 0 {
			errMsgs := make([]string, len(parseErrs))
			for i, e := range parseErrs {
				errMsgs[i] = e.Error()
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"parse_errors": errMsgs})
			return
		}

		validationErrs := validator.ValidateAll(defs)
		if len(validationErrs) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"validation_errors": validationErrs})
			return
		}

		result := s.Sync(r.Context(), defs, req.ProjectID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})

	srv := &http.Server{
		Addr: ":" + port,
		// Wrap the router with otelhttp for automatic HTTP trace spans.
		Handler:      otelhttp.NewHandler(r, "gitops-sync"),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	logger.Info("gitops-sync starting", zap.String("port", port))
	if err := srv.ListenAndServe(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

func runCLISync(flagsDir, projectID string, s *syncer.Syncer, logger *zap.Logger) {
	logger.Info("running one-shot sync", zap.String("dir", flagsDir))
	defs, errs := parser.ParseDirectory(flagsDir)
	if len(errs) > 0 {
		for _, e := range errs {
			logger.Error("parse error", zap.Error(e))
		}
		os.Exit(1)
	}
	validErrs := validator.ValidateAll(defs)
	if len(validErrs) > 0 {
		for key, verrs := range validErrs {
			for _, e := range verrs {
				logger.Error("validation error", zap.String("flag", key), zap.Error(e))
			}
		}
		os.Exit(1)
	}
	result := s.Sync(context.Background(), defs, projectID)
	logger.Info("sync complete",
		zap.Int("created", len(result.Created)),
		zap.Int("updated", len(result.Updated)),
		zap.Int("errors", len(result.Errors)))
	if len(result.Errors) > 0 {
		os.Exit(1)
	}
}
