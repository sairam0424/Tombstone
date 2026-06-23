package main

import (
	"context"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	v1 "github.com/tombstone/marketplace/internal/api/v1"
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

	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
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
