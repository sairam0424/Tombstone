package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	v1 "github.com/tombstone/flag-api/internal/api/v1"
	"github.com/tombstone/flag-api/internal/health"
	"github.com/tombstone/flag-api/internal/middleware"
	"github.com/tombstone/flag-api/internal/scheduler"
	"github.com/tombstone/flag-api/internal/telemetry"
	"github.com/tombstone/flag-api/internal/tlsutil"
	"github.com/tombstone/flag-api/internal/transparency"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	ctx := context.Background()

	// Initialise OpenTelemetry. OTLP_ENDPOINT is optional — noop when unset.
	shutdownTracer, err := telemetry.InitTracer(ctx, "flag-api")
	if err != nil {
		logger.Fatal("init tracer", zap.Error(err))
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracer(shutdownCtx)
	}()

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
	db.SetMaxOpenConns(5)                      // Neon free tier — share budget with other services
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)     // recycle before Neon's ~5 min idle timeout
	db.SetConnMaxIdleTime(2 * time.Minute)     // release idle conns quickly
	if err := db.Ping(); err != nil {
		logger.Fatal("ping db", zap.Error(err))
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		logger.Fatal("parse redis url", zap.Error(err))
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Fatal("ping redis", zap.Error(err))
	}

	rekorClient := transparency.NewRekorClient()

	authMw := middleware.NewAuthMiddleware(db, jwtSecret)
	rbacMw := middleware.NewRBACMiddleware(db, logger)
	rateMw := middleware.NewRateLimitMiddleware(rdb)
	defer rateMw.Stop()
	idempotencyMw := middleware.NewIdempotencyMiddleware(db, logger)
	flagH := v1.NewFlagHandler(db, rdb, logger, rekorClient)
	snapH := v1.NewSnapshotHandler(db, logger)
	auditH := v1.NewAuditHandler(db, logger)
	complianceH := v1.NewComplianceHandler(db, logger)
	prereqH := v1.NewPrerequisiteHandler(db, logger)
	scheduledH := v1.NewScheduledHandler(db, rdb, logger)
	breakGlassH := v1.NewBreakGlassHandler(db, rdb, logger)

	// Background workers — all share the same cancellable root context.
	bgCtx, bgCancel := context.WithCancel(context.Background())

	// Orphan detector: scans for stale flags every 24 h.
	go v1.NewOrphanDetector(db, logger).Run(bgCtx)

	// Scheduled-change executor: applies due flag changes every 30 s.
	go scheduler.Start(bgCtx, db, rdb, logger)

	// Idempotency-key cleanup: purges expired idempotency_keys rows every hour.
	go idempotencyMw.StartCleanup(bgCtx)

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	r.Use(rateMw.RateLimit)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
	}))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	healthChecker := &health.Checker{DB: db, RDB: rdb}
	r.Get("/readyz", healthChecker.Readyz)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMw.Authenticate)
		r.Use(rbacMw.LoadRole)

		r.Get("/flags", flagH.ListFlags)
		// Idempotency-key support (opt-in via "Idempotency-Key" header) guards
		// against a Phase-2 resilient-client retry executing the same mutation
		// twice. Applied ONLY to CreateFlag, UpdateEnvironment, and KillSwitch —
		// not globally, and not to any other route.
		r.With(idempotencyMw.Handle("POST /flags")).
			Post("/flags", flagH.CreateFlag)
		r.Get("/flags/{key}", flagH.GetFlag)
		r.Delete("/flags/{key}", flagH.ArchiveFlag)
		r.With(idempotencyMw.Handle("PATCH /flags/{key}/environments/{env}")).
			Patch("/flags/{key}/environments/{env}", flagH.UpdateEnvironment)

		// Kill-switch: restricted to OWNER and ADMIN only (flags:kill_switch permission).
		r.With(rbacMw.RequirePermission("flags", "kill_switch")).
			With(idempotencyMw.Handle("POST /flags/{key}/kill")).
			Post("/flags/{key}/kill", flagH.KillSwitch)

		// Flag prerequisites (GrowthBook ParentConditions pattern)
		r.Post("/flags/{key}/prerequisites", prereqH.AddPrerequisite)
		r.Get("/flags/{key}/prerequisites", prereqH.ListPrerequisites)
		r.Delete("/flags/{key}/prerequisites/{id}", prereqH.DeletePrerequisite)

		// Scheduled changes
		r.Post("/flags/{key}/schedule", scheduledH.CreateSchedule)
		r.Get("/flags/{key}/schedule", scheduledH.ListSchedule)
		r.Delete("/flags/{key}/schedule/{id}", scheduledH.CancelSchedule)

		r.Get("/environments/snapshot", snapH.GetSnapshot)
		r.Get("/audit", auditH.ListAuditLog)

		r.Get("/compliance/evidence", complianceH.GetEvidence)
		r.Get("/compliance/controls", complianceH.GetControls)
		r.Get("/compliance/export", complianceH.ExportAuditLog)

		// Break-glass endpoints: all require elevated roles.
		// CreateToken and ListTokens require ADMIN (admin:admin permission).
		// UseToken requires OWNER or ADMIN (flags:kill_switch permission covers emergency use).
		r.Route("/break-glass", func(r chi.Router) {
			r.With(rbacMw.RequirePermission("admin", "admin")).
				Post("/tokens", breakGlassH.CreateToken)
			r.With(rbacMw.RequirePermission("flags", "kill_switch")).
				Post("/use", breakGlassH.UseToken)
			r.With(rbacMw.RequirePermission("admin", "admin")).
				Get("/tokens", breakGlassH.ListTokens)
		})
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
		Addr: ":" + port,
		// Wrap the router with otelhttp for automatic HTTP trace spans.
		Handler:      otelhttp.NewHandler(r, "flag-api"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	// mTLS: generate and write certs, then configure the server for mutual TLS.
	// Falls back to plain HTTP when MTLS_ENABLED is not set (safe default for local dev).
	if os.Getenv("MTLS_ENABLED") == "true" {
		certsDir := os.Getenv("CERTS_DIR")
		if certsDir == "" {
			certsDir = "/certs"
		}
		logger.Info("mTLS enabled — generating internal PKI certs", zap.String("certs_dir", certsDir))
		caCert, _, err := tlsutil.GenerateCACert()
		if err != nil {
			logger.Fatal("generate CA cert", zap.Error(err))
		}
		serverCert, err := tlsutil.GenerateServiceCert(caCert, "flag-api")
		if err != nil {
			logger.Fatal("generate server cert", zap.Error(err))
		}
		clientCert, err := tlsutil.GenerateServiceCert(caCert, "client")
		if err != nil {
			logger.Fatal("generate client cert", zap.Error(err))
		}
		if err := tlsutil.WriteCerts(certsDir, caCert, serverCert, clientCert); err != nil {
			logger.Fatal("write certs", zap.Error(err))
		}
		tlsCfg, err := tlsutil.LoadServerTLSConfig(certsDir)
		if err != nil {
			logger.Fatal("load server TLS config", zap.Error(err))
		}
		srv.TLSConfig = tlsCfg
		logger.Info("flag-api mTLS configured — client certs written", zap.String("certs_dir", certsDir))
	}

	go func() {
		logger.Info("flag-api starting", zap.String("port", port))
		var serveErr error
		if os.Getenv("MTLS_ENABLED") == "true" {
			// Certs are already loaded into srv.TLSConfig; empty strings tell ListenAndServeTLS
			// to use the pre-configured TLSConfig rather than reading files from disk again.
			serveErr = srv.ListenAndServeTLS("", "")
		} else {
			serveErr = srv.ListenAndServe()
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Fatal("server error", zap.Error(serveErr))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutting down flag-api")

	bgCancel() // stop background workers (orphan detector + scheduled executor)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}
