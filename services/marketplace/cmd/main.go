package main

import (
	"database/sql"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	_ "github.com/lib/pq"
	"go.uber.org/zap"

	v1 "github.com/tombstone/marketplace/internal/api/v1"
	"github.com/tombstone/marketplace/internal/registry"
	"github.com/tombstone/marketplace/internal/webhook"
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
	defer logger.Sync() //nolint:errcheck

	port := os.Getenv("PORT")
	if port == "" {
		port = "8086"
	}

	// Optional PostgreSQL persistence. Falls back to in-memory if DB_URL is unset or unreachable.
	var store registry.Store = &registry.MemoryStore{}
	if dbURL := os.Getenv("DB_URL"); dbURL != "" {
		db, err := sql.Open("postgres", dbURL)
		if err == nil {
			if pingErr := db.Ping(); pingErr == nil {
				store = registry.NewPostgresStore(db)
				logger.Info("marketplace: using PostgreSQL persistence")
			} else {
				logger.Warn("marketplace: DB_URL set but connection failed, using in-memory store",
					zap.Error(pingErr))
			}
		} else {
			logger.Warn("marketplace: failed to open DB, using in-memory store", zap.Error(err))
		}
	} else {
		logger.Warn("marketplace: DB_URL not set — installations not persisted across restarts")
	}

	reg := registry.NewRegistry(store)
	dispatcher := webhook.NewDispatcher(reg, logger)
	handler := v1.NewHandler(reg, dispatcher, logger)

	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// Build CORS allowlist from environment. Default: localhost for dev.
	allowedOrigins := buildAllowedOrigins()
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
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
