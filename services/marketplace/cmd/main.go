package main

import (
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.uber.org/zap"

	v1 "github.com/tombstone/marketplace/internal/api/v1"
	"github.com/tombstone/marketplace/internal/integrations"
	"github.com/tombstone/marketplace/internal/registry"
	"github.com/tombstone/marketplace/internal/webhook"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync() //nolint:errcheck

	port := os.Getenv("PORT")
	if port == "" {
		port = "8086"
	}

	flagAPIURL := os.Getenv("FLAG_API_URL")
	if flagAPIURL == "" {
		flagAPIURL = "http://flag-api:8081"
	}

	reg := registry.NewRegistry()
	dispatcher := webhook.NewDispatcher(reg, logger)
	handler := v1.NewHandler(reg, dispatcher, logger)

	// Interactive Slack app — requires SLACK_BOT_TOKEN + SLACK_SIGNING_SECRET.
	slackBotToken := os.Getenv("SLACK_BOT_TOKEN")
	slackSigningSecret := os.Getenv("SLACK_SIGNING_SECRET")
	slackApp := integrations.NewSlackApp(slackBotToken, slackSigningSecret, flagAPIURL)
	inboundHandler := v1.NewInboundHandler(slackApp, logger)

	// Update Slack integration metadata to reflect bidirectional capability.
	reg.MarkBidirectional("slack", []string{
		"/api/v1/marketplace/slack/commands",
		"/api/v1/marketplace/slack/actions",
	})

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

		// Slack inbound endpoints (slash commands + block actions).
		r.Route("/slack", func(r chi.Router) {
			r.Post("/commands", inboundHandler.HandleSlashCommand)
			r.Post("/actions", inboundHandler.HandleBlockAction)
		})

		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", handler.GetIntegration)
			r.Delete("/", handler.UninstallIntegration)
			r.Post("/install", handler.InstallIntegration)
		})
	})

	addr := ":" + port
	logger.Info("marketplace service starting", zap.String("addr", addr))
	if err := http.ListenAndServe(addr, r); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}
