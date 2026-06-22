package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	v1 "github.com/tombstone/flag-api/internal/api/v1"
	"github.com/tombstone/flag-api/internal/middleware"
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
	defer logger.Sync()

	// All connection strings MUST be supplied via environment variables.
	// See infra/docker-compose.yml for local dev values.
	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		logger.Fatal("DB_URL environment variable is required")
	}
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		logger.Fatal("REDIS_URL environment variable is required")
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		logger.Fatal("JWT_SECRET environment variable is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		logger.Fatal("open db", zap.Error(err))
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	if err := db.Ping(); err != nil {
		logger.Fatal("ping db", zap.Error(err))
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		logger.Fatal("parse redis url", zap.Error(err))
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		logger.Fatal("ping redis", zap.Error(err))
	}

	authMw := middleware.NewAuthMiddleware(db, jwtSecret)
	rbacMw := middleware.NewRBACMiddleware(db, logger)
	flagH := v1.NewFlagHandler(db, rdb, logger)
	snapH := v1.NewSnapshotHandler(db, logger)
	auditH := v1.NewAuditHandler(db, logger)
	complianceH := v1.NewComplianceHandler(db, logger)
	breakGlassH := v1.NewBreakGlassHandler(db, rdb, logger)

	// Start background orphan detector (runs every 24 h, stops on shutdown).
	orphanCtx, orphanCancel := context.WithCancel(context.Background())
	go v1.NewOrphanDetector(db, logger).Run(orphanCtx)

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	// Build CORS allowlist from environment. Default: localhost for dev.
	allowedOrigins := buildAllowedOrigins()
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: allowedOrigins,
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
	}))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMw.Authenticate)
		r.Use(rbacMw.LoadRole)

		r.Get("/flags", flagH.ListFlags)
		r.Post("/flags", flagH.CreateFlag)
		r.Get("/flags/{key}", flagH.GetFlag)
		r.Delete("/flags/{key}", flagH.ArchiveFlag)
		r.Patch("/flags/{key}/environments/{env}", flagH.UpdateEnvironment)
		r.Post("/flags/{key}/kill", flagH.KillSwitch)

		r.Get("/environments/snapshot", snapH.GetSnapshot)
		r.Get("/audit", auditH.ListAuditLog)

		r.Get("/compliance/evidence", complianceH.GetEvidence)
		r.Get("/compliance/controls", complianceH.GetControls)
		r.Get("/compliance/export", complianceH.ExportAuditLog)

		r.Post("/break-glass/tokens", breakGlassH.CreateToken)
		r.Post("/break-glass/use", breakGlassH.UseToken)
		r.Get("/break-glass/tokens", breakGlassH.ListTokens)
	})

	scimToken := os.Getenv("SCIM_TOKEN")
	scimH := v1.NewSCIMHandler(db, rdb, logger)
	r.Route("/scim/v2", func(r chi.Router) {
		r.Use(v1.SCIMAuthMiddleware(scimToken))
		r.Get("/Users", scimH.ListUsers)
		r.Post("/Users", scimH.ProvisionUser)
		r.Get("/Users/{id}", scimH.GetUser)
		r.Put("/Users/{id}", scimH.UpdateUser)
		r.Delete("/Users/{id}", scimH.DeprovisionUser)
	})

	if ssoProvider := os.Getenv("SSO_PROVIDER"); ssoProvider != "" {
		ssoMw := middleware.NewSSOMiddleware(middleware.SSOConfig{
			Provider:     ssoProvider,
			OIDCIssuer:   os.Getenv("OIDC_ISSUER"),
			OIDCClientID: os.Getenv("OIDC_CLIENT_ID"),
			CallbackURL:  os.Getenv("SSO_CALLBACK_URL"),
		}, jwtSecret, logger)
		r.Get("/auth/login", ssoMw.LoginHandler)
		r.Get("/auth/callback", ssoMw.CallbackHandler)
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		logger.Info("flag-api starting", zap.String("port", port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutting down flag-api")

	orphanCancel() // stop background orphan detector

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
