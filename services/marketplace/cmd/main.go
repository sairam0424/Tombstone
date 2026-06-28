package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	v1 "github.com/tombstone/marketplace/internal/api/v1"
	"github.com/tombstone/marketplace/internal/integrations"
	"github.com/tombstone/marketplace/internal/registry"
	"github.com/tombstone/marketplace/internal/telemetry"
	"github.com/tombstone/marketplace/internal/webhook"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync() //nolint:errcheck

	initCtx := context.Background()

	// Initialise OpenTelemetry. OTLP_ENDPOINT is optional — noop when unset.
	shutdownTracer, err := telemetry.InitTracer(initCtx, "marketplace")
	if err != nil {
		logger.Fatal("init tracer", zap.Error(err))
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracer(shutdownCtx)
	}()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8086"
	}

	flagAPIURL := os.Getenv("FLAG_API_URL")
	if flagAPIURL == "" {
		flagAPIURL = "http://flag-api:8081"
	}

	// Redis is optional — if unavailable the registry operates in ephemeral mode.
	var rdb *redis.Client
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			logger.Warn("marketplace: invalid REDIS_URL, state is ephemeral (Redis unavailable)",
				zap.Error(err),
			)
		} else {
			client := redis.NewClient(opt)
			if err := client.Ping(context.Background()).Err(); err != nil {
				logger.Warn("marketplace state is ephemeral (Redis unavailable)",
					zap.Error(err),
				)
			} else {
				rdb = client
			}
		}
	} else {
		logger.Warn("marketplace state is ephemeral (Redis unavailable): REDIS_URL not set")
	}

	reg := registry.NewRegistry(rdb, logger)
	reg.LoadFromRedis(context.Background())
	dispatcher := webhook.NewDispatcher(reg, logger)
	handler := v1.NewHandler(reg, dispatcher, logger)

	// Interactive Slack app — wired at runtime if SLACK_BOT_TOKEN is set.
	slackEnabled := false
	if slackToken := os.Getenv("SLACK_BOT_TOKEN"); slackToken != "" {
		slackApp := integrations.NewSlackApp(
			slackToken,
			os.Getenv("SLACK_SIGNING_SECRET"),
			flagAPIURL,
			os.Getenv("FLAG_API_TOKEN"),
		)
		handler.SetSlackApp(slackApp)
		reg.MarkBidirectional("slack", []string{
			"/api/v1/marketplace/slack/commands",
			"/api/v1/marketplace/slack/actions",
		})
		slackEnabled = true
		logger.Info("marketplace: Slack interactive app enabled",
			zap.Strings("routes", []string{
				"/api/v1/marketplace/slack/commands",
				"/api/v1/marketplace/slack/actions",
			}))
	}

	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{
			"Accept", "Authorization", "Content-Type", "X-Request-ID",
			"X-Slack-Request-Timestamp", "X-Slack-Signature",
		},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"marketplace"}`))
	})

	// Marketplace routes
	r.Route("/api/v1/marketplace", func(r chi.Router) {
		r.Get("/", handler.ListIntegrations)
		r.Post("/register", handler.RegisterIntegration)
		r.Post("/events", handler.TriggerEvent)

		// Inbound webhook routes — external services post alerts here.
		r.Route("/inbound", func(r chi.Router) {
			// POST /api/v1/marketplace/inbound/datadog
			// Receives Datadog monitor alerts; auto-triggers blast-radius check and
			// optional kill switch for P1/P2 alerts with BLOCKED flags.
			r.Post("/datadog", handler.HandleDatadogInbound)
		})

		// Interactive Slack app routes — active only when SLACK_BOT_TOKEN is set.
		if slackEnabled {
			r.Route("/slack", func(r chi.Router) {
				r.Post("/commands", handler.HandleSlackCommands)
				r.Post("/actions", handler.HandleSlackActions)
			})
		}

		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", handler.GetIntegration)
			r.Delete("/", handler.UninstallIntegration)
			r.Post("/install", handler.InstallIntegration)
		})
	})

	addr := ":" + port
	logger.Info("marketplace service starting", zap.String("addr", addr))
	// Wrap the router with otelhttp for automatic HTTP trace spans.
	if err := http.ListenAndServe(addr, otelhttp.NewHandler(r, "marketplace")); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}
