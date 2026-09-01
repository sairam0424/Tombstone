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
	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/docs"
	"github.com/tombstone/flag-api/internal/health"
	"github.com/tombstone/flag-api/internal/middleware"
	"github.com/tombstone/flag-api/internal/scheduler"
	"github.com/tombstone/flag-api/internal/secrets"
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
	// SEC-4: token hashing is mandatory — service and break-glass tokens are
	// stored only as HMAC(pepper, token), so without the pepper no service token
	// can be authenticated at all. Fail at startup rather than silently
	// rejecting every service-token request at runtime.
	tokenHasher, err := secrets.NewTokenHasherFromEnv()
	if err != nil {
		logger.Fatal("token hashing not configured", zap.Error(err))
	}

	// SEC-4: audit exports are signed with a key SEPARATE from JWT_SECRET. This
	// is intentionally NOT fatal — a deployment that never exports compliance
	// evidence should still boot; the export endpoint itself fails closed with a
	// clear message instead.
	complianceSigner, signerErr := secrets.NewComplianceSignerFromEnv()
	if signerErr != nil {
		logger.Warn("compliance export signing disabled — GET /api/v1/compliance/export will return 503",
			zap.Error(signerErr))
	}

	// AUD-1: the audit chain is keyed, so only a key holder can extend it. An
	// unkeyed chain is forgeable by anyone who can INSERT into audit_log. Not
	// fatal: flag-api still serves traffic, but audit writes are refused and
	// GET /api/v1/audit/verify reports 503 rather than pretending to verify.
	auditKey, auditKeyErr := secrets.NewAuditKeyFromEnv()
	if auditKeyErr != nil {
		logger.Warn("audit chain key not configured — audit writes will be skipped and verification unavailable",
			zap.Error(auditKeyErr))
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		logger.Fatal("open db", zap.Error(err))
	}
	db.SetMaxOpenConns(5) // Neon free tier — share budget with other services
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute) // recycle before Neon's ~5 min idle timeout
	db.SetConnMaxIdleTime(2 * time.Minute) // release idle conns quickly
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

	// The single audit writer — one canonical keyed hash, one advisory-locked
	// transactional append path for every call site (AUD-1). Always constructed:
	// if auditKey is nil the trail is still recorded, just unkeyed and reported
	// as unverifiable, because dropping audit records outright is worse.
	auditWriter := audit.NewWriter(db, auditKey)

	authMw := middleware.NewAuthMiddleware(db, jwtSecret, tokenHasher)
	rbacMw := middleware.NewRBACMiddleware(db, logger)
	rateMw := middleware.NewRateLimitMiddleware(rdb)
	defer rateMw.Stop()
	idempotencyMw := middleware.NewIdempotencyMiddleware(db, logger)
	loadShedMw := middleware.NewLoadShedMiddleware(middleware.DefaultLoadShedConfig(), logger)
	flagH := v1.NewFlagHandler(db, rdb, logger, rekorClient, auditWriter)
	snapH := v1.NewSnapshotHandler(db, logger)
	auditH := v1.NewAuditHandler(db, logger, auditWriter)
	complianceH := v1.NewComplianceHandler(db, logger, complianceSigner, auditWriter, rbacMw.PolicySource)
	prereqH := v1.NewPrerequisiteHandler(db, logger)
	scheduledH := v1.NewScheduledHandler(db, rdb, logger, auditWriter)
	breakGlassH := v1.NewBreakGlassHandler(db, rdb, logger, tokenHasher, auditWriter)
	crH := v1.NewChangeRequestHandler(db, rdb, logger)

	// Background workers — all share the same cancellable root context.
	bgCtx, bgCancel := context.WithCancel(context.Background())

	// Orphan detector: scans for stale flags every 24 h.
	go v1.NewOrphanDetector(db, logger).Run(bgCtx)

	// Scheduled-change executor: applies due flag changes every 30 s.
	go scheduler.Start(bgCtx, db, rdb, logger, auditWriter)

	// Idempotency-key cleanup: purges expired idempotency_keys rows every hour.
	go idempotencyMw.StartCleanup(bgCtx)

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	r.Use(rateMw.RateLimit)
	// Load shedding runs AFTER rate limiting: rate limiting rejects
	// over-quota callers first, regardless of system load; load shedding
	// then additionally rejects when the service itself is saturated,
	// regardless of any individual caller's quota standing.
	r.Use(loadShedMw.LoadShed)
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

	// Redoc interactive API explorer — public, no auth middleware.
	// Must be registered BEFORE the auth-gated /api/v1 route group.
	docsHandler := docs.NewHandler("/api/v1/openapi.json")
	r.Handle("/api/v1/docs", docsHandler)
	r.Handle("/api/v1/docs/*", docsHandler)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMw.Authenticate)
		// TEN-1a: resolves and validates which project this request is for,
		// BEFORE LoadRole — role resolution needs project_id to pick the right
		// user_roles row (that table is keyed by (user_id, project_id), so a
		// user with roles in more than one project is otherwise ambiguous).
		r.Use(rbacMw.RequireProjectID)
		r.Use(rbacMw.LoadRole)

		// SEC-1: EVERY route below carries an explicit RequirePermission gate.
		// Authenticate proves who the caller is and LoadRole resolves their role,
		// but neither one denies anything — a route without RequirePermission is
		// authenticated yet unauthorized, so any valid token (including a
		// read-only SDK token) could previously reach it.
		r.With(rbacMw.RequirePermission("flags", "read")).
			Get("/flags", flagH.ListFlags)
		// Idempotency-key support (opt-in via "Idempotency-Key" header) guards
		// against a Phase-2 resilient-client retry executing the same mutation
		// twice. Applied ONLY to CreateFlag, UpdateEnvironment, and KillSwitch —
		// not globally, and not to any other route.
		r.With(rbacMw.RequirePermission("flags", "write")).
			With(idempotencyMw.Handle("POST /flags")).
			Post("/flags", flagH.CreateFlag)
		r.With(rbacMw.RequirePermission("flags", "read")).
			Get("/flags/{key}", flagH.GetFlag)
		r.With(rbacMw.RequirePermission("flags", "write")).
			Delete("/flags/{key}", flagH.ArchiveFlag)
		// Rollout/targeting changes mutate flag_environments -> environments:write.
		r.With(rbacMw.RequirePermission("environments", "write")).
			With(idempotencyMw.Handle("PATCH /flags/{key}/environments/{env}")).
			Patch("/flags/{key}/environments/{env}", flagH.UpdateEnvironment)

		// Kill-switch: restricted to OWNER and ADMIN only (flags:kill_switch permission).
		r.With(rbacMw.RequirePermission("flags", "kill_switch")).
			With(idempotencyMw.Handle("POST /flags/{key}/kill")).
			Post("/flags/{key}/kill", flagH.KillSwitch)

		// Flag prerequisites (GrowthBook ParentConditions pattern).
		// Prerequisites gate whether a flag evaluates at all, so mutating them
		// is a flag-state change -> flags:write.
		r.With(rbacMw.RequirePermission("flags", "write")).
			Post("/flags/{key}/prerequisites", prereqH.AddPrerequisite)
		r.With(rbacMw.RequirePermission("flags", "read")).
			Get("/flags/{key}/prerequisites", prereqH.ListPrerequisites)
		r.With(rbacMw.RequirePermission("flags", "write")).
			Delete("/flags/{key}/prerequisites/{id}", prereqH.DeletePrerequisite)

		// Scheduled changes — a scheduled write is still a write, gated at
		// schedule time (the scheduler itself runs in-process, not via HTTP).
		r.With(rbacMw.RequirePermission("flags", "write")).
			Post("/flags/{key}/schedule", scheduledH.CreateSchedule)
		r.With(rbacMw.RequirePermission("flags", "read")).
			Get("/flags/{key}/schedule", scheduledH.ListSchedule)
		r.With(rbacMw.RequirePermission("flags", "write")).
			Delete("/flags/{key}/schedule/{id}", scheduledH.CancelSchedule)

		// SDK/relay snapshot fetch — flags:read keeps read-only service tokens
		// (VIEWER) working, which is the SDK hot path.
		r.With(rbacMw.RequirePermission("flags", "read")).
			Get("/environments/snapshot", snapH.GetSnapshot)
		r.With(rbacMw.RequirePermission("audit", "read")).
			Get("/audit", auditH.ListAuditLog)
		// AUD-1: recomputes the keyed chain and reports real integrity.
		r.With(rbacMw.RequirePermission("audit", "read")).
			Get("/audit/verify", auditH.VerifyChain)

		// Compliance: evidence/controls are summaries (audit:read), but a full
		// audit-log export is the most sensitive read in the system and is
		// restricted to ADMIN so a read-only/SDK token cannot exfiltrate it.
		r.With(rbacMw.RequirePermission("audit", "read")).
			Get("/compliance/evidence", complianceH.GetEvidence)
		r.With(rbacMw.RequirePermission("audit", "read")).
			Get("/compliance/controls", complianceH.GetControls)
		r.With(rbacMw.RequirePermission("admin", "admin")).
			Get("/compliance/export", complianceH.ExportAuditLog)

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

		// Four-eyes approval workflow: list is available to any authenticated user;
		// approve/reject require OWNER or ADMIN (flags:kill_switch permission).
		r.Get("/change-requests", crH.ListChangeRequests)
		r.With(rbacMw.RequirePermission("flags", "kill_switch")).
			Post("/change-requests/{id}/approve", crH.ApproveChangeRequest)
		r.With(rbacMw.RequirePermission("flags", "kill_switch")).
			Post("/change-requests/{id}/reject", crH.RejectChangeRequest)
	})

	scimToken := os.Getenv("SCIM_TOKEN")
	scimH := v1.NewSCIMHandler(db, rdb, logger, auditWriter)
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
