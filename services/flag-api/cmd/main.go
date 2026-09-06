package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
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
	// DATA-2: env-tunable, defaulting to the exact values this pool has
	// always hardcoded, so an unconfigured deployment behaves identically
	// to before. The defaults themselves stay Neon-free-tier-shaped (share
	// a small connection budget with other services); a larger Postgres
	// tier can now raise them without a code change.
	db.SetMaxOpenConns(envIntOrDefault("DB_MAX_OPEN_CONNS", 5, logger))
	db.SetMaxIdleConns(envIntOrDefault("DB_MAX_IDLE_CONNS", 2, logger))
	db.SetConnMaxLifetime(envSecondsOrDefault("DB_CONN_MAX_LIFETIME_SECONDS", 5*time.Minute, logger))  // recycle before Neon's ~5 min idle timeout
	db.SetConnMaxIdleTime(envSecondsOrDefault("DB_CONN_MAX_IDLE_TIME_SECONDS", 2*time.Minute, logger)) // release idle conns quickly
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

	// AUD-1b: absent/invalid is NOT fatal — Rekor submission has always been
	// optional and fail-open (REKOR_ENABLED gates it); NewRekorClient itself
	// warns and disables submission if REKOR_ENABLED=true but no signer is
	// available, rather than pretending to submit signature-less entries.
	// A configured-but-INVALID key (distinct from simply unset) is worth its
	// own warning here regardless of REKOR_ENABLED, since that's a real
	// operator mistake, not an intentionally-disabled feature.
	rekorSigner, rekorSignerErr := secrets.NewRekorSignerFromEnv()
	if rekorSignerErr != nil {
		rekorSigner = nil
		if !errors.Is(rekorSignerErr, secrets.ErrNoRekorSigningKey) {
			logger.Warn("REKOR_SIGNING_KEY is set but invalid — Rekor submissions disabled", zap.Error(rekorSignerErr))
		}
	}
	rekorClient := transparency.NewRekorClient(rekorSigner)

	// The single audit writer — one canonical keyed hash, one advisory-locked
	// transactional append path for every call site (AUD-1). Always constructed:
	// if auditKey is nil the trail is still recorded, just unkeyed and reported
	// as unverifiable, because dropping audit records outright is worse.
	auditWriter := audit.NewWriter(db, auditKey)
	// DATA-2: shares auditKey rather than a dedicated key — see
	// audit.NewRetention's doc comment for why. AUDIT_LOG_RETENTION_DAYS
	// defaults to 365, matching the ~12-month observation window SOC2 Type II
	// evidence is typically drawn from.
	auditRetention := audit.NewRetention(db, auditKey)
	auditRetentionDays := envIntOrDefault("AUDIT_LOG_RETENTION_DAYS", 365, logger)

	authMw := middleware.NewAuthMiddleware(db, jwtSecret, tokenHasher, logger)
	rbacMw := middleware.NewRBACMiddleware(db, logger)
	rateMw := middleware.NewRateLimitMiddleware(rdb)
	defer rateMw.Stop()
	idempotencyMw := middleware.NewIdempotencyMiddleware(db, logger)
	loadShedMw := middleware.NewLoadShedMiddleware(middleware.DefaultLoadShedConfig(), logger)
	// MARKETPLACE_URL: base URL of services/marketplace, used to fire
	// flag-lifecycle events at its webhook dispatcher (Slack/Datadog/
	// PagerDuty/OpsGenie/Jira/Linear/OpenTelemetry). Optional and fail-open,
	// matching REKOR_ENABLED/SLACK_WEBHOOK_URL's own opt-in convention --
	// unset means notifyMarketplace is a permanent no-op, not an error.
	marketplaceURL := os.Getenv("MARKETPLACE_URL")
	flagH := v1.NewFlagHandler(db, rdb, logger, rekorClient, auditWriter, tokenHasher, marketplaceURL)
	snapH := v1.NewSnapshotHandler(db, logger)
	auditH := v1.NewAuditHandler(db, logger, auditWriter)
	retentionH := v1.NewRetentionHandler(logger, auditRetention, auditRetentionDays)
	complianceH := v1.NewComplianceHandler(db, logger, complianceSigner, auditWriter, rbacMw.PolicySource)
	prereqH := v1.NewPrerequisiteHandler(db, logger)
	scheduledH := v1.NewScheduledHandler(db, rdb, logger, auditWriter)
	breakGlassH := v1.NewBreakGlassHandler(db, rdb, logger, tokenHasher, auditWriter)
	crH := v1.NewChangeRequestHandler(db, rdb, logger, auditWriter)

	// Background workers — all share the same cancellable root context.
	bgCtx, bgCancel := context.WithCancel(context.Background())

	// Orphan detector: scans for stale flags every 24 h.
	go v1.NewOrphanDetector(db, logger).Run(bgCtx)

	// Scheduled-change executor: applies due flag changes every 30 s.
	go scheduler.Start(bgCtx, db, rdb, logger, auditWriter)

	// Idempotency-key cleanup: purges expired idempotency_keys rows every hour.
	go idempotencyMw.StartCleanup(bgCtx)

	// OBS-1 (first slice): pull-based, no OTLP_ENDPOINT/collector needed —
	// unlike InitTracer, there's no "unset means no-op" branch.
	meter, metricsHandler, err := telemetry.InitMeter("flag-api")
	if err != nil {
		logger.Fatal("init meter", zap.Error(err))
	}
	httpMetrics, err := telemetry.HTTPMetrics(meter)
	if err != nil {
		logger.Fatal("init http metrics middleware", zap.Error(err))
	}

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	r.Use(httpMetrics)
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

	// OBS-1: Prometheus scrape endpoint — public, no auth middleware,
	// matching /health and /readyz above. Network-level access control
	// (not app-level auth) is the expected boundary for scrape endpoints.
	r.Get("/metrics", metricsHandler.ServeHTTP)

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

		// EVAL-4: automated graduated rollback, restricted to
		// flags:circuit_breaker (RoleCircuitBreaker -- assignable only via
		// service_tokens.role, never a human project-membership grant).
		// Deliberately a SEPARATE permission from flags:kill_switch above,
		// so no OWNER/ADMIN gets this capability for free -- see PR #220's
		// adversarial review and RollbackStep's own doc comment.
		r.With(rbacMw.RequirePermission("flags", "circuit_breaker")).
			With(idempotencyMw.Handle("POST /flags/{key}/rollback-step")).
			Post("/flags/{key}/rollback-step", flagH.RollbackStep)

		// EVAL-4: the mirror image of rollback-step above, for the
		// HALF_OPEN recovery ladder's ascent direction -- SAME permission,
		// opposite invariant (can only increase, never decrease). See
		// RecoveryStep's own doc comment for why this is a separate
		// endpoint rather than relaxing rollback-step's own guard.
		r.With(rbacMw.RequirePermission("flags", "circuit_breaker")).
			With(idempotencyMw.Handle("POST /flags/{key}/recovery-step")).
			Post("/flags/{key}/recovery-step", flagH.RecoveryStep)

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
		// DATA-2: archives old audit_log partitions. ADMIN-only — same tier
		// as /compliance/export and break-glass token creation — since this
		// mutates the audit log's physical storage, unlike every other
		// audit:read route above.
		r.With(rbacMw.RequirePermission("admin", "admin")).
			Post("/audit/retention/run", retentionH.RunRetention)

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

		// Four-eyes approval workflow: list is available to any authenticated
		// user (see the documented SEC-3 exemption in cmd/authz_routes_test.go).
		// Proposing a change needs the same privilege making it directly would
		// (environments:write). Approve/reject use approvals:approve (SEC-3b) —
		// OWNER/ADMIN only, same role set flags:kill_switch granted, but now
		// under the permission actually meant for this action.
		r.Get("/change-requests", crH.ListChangeRequests)
		r.With(rbacMw.RequirePermission("environments", "write")).
			Post("/change-requests", crH.ProposeChangeRequest)
		r.With(rbacMw.RequirePermission("approvals", "approve")).
			Post("/change-requests/{id}/approve", crH.ApproveChangeRequest)
		r.With(rbacMw.RequirePermission("approvals", "approve")).
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
		// SEC-5: SSOConfig.AllowedDomains has always been read and enforced by
		// isAllowedDomain — it was just never populated here, so every
		// deployment's domain allowlist silently had zero effect and any
		// successfully-authenticated OIDC user from any domain could log in.
		rawAllowedDomains := os.Getenv("SSO_ALLOWED_DOMAINS")
		allowedDomains := splitCommaList(rawAllowedDomains)
		if rawAllowedDomains != "" && len(allowedDomains) == 0 {
			// The var is SET but parses to zero domains (e.g. a lone space or
			// comma from a config-templating mistake) — this is
			// indistinguishable from "unset" to isAllowedDomain, silently
			// reopening the exact gap this fix exists to close. Warn loudly
			// rather than booting into a fail-open state with no signal.
			logger.Warn("SSO_ALLOWED_DOMAINS is set but contains no usable domains — SSO login will accept ANY domain",
				zap.String("raw_value", rawAllowedDomains))
		}
		ssoMw := middleware.NewSSOMiddleware(middleware.SSOConfig{
			Provider:       ssoProvider,
			OIDCIssuer:     os.Getenv("OIDC_ISSUER"),
			OIDCClientID:   os.Getenv("OIDC_CLIENT_ID"),
			CallbackURL:    os.Getenv("SSO_CALLBACK_URL"),
			AllowedDomains: allowedDomains,
		}, jwtSecret, logger, db)
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

// splitCommaList parses a comma-separated env var into a trimmed,
// non-empty slice. Returns nil (not enforcing any restriction) for an
// unset/empty value — matching SSOConfig.AllowedDomains' own documented
// "empty means unrestricted" contract.
func splitCommaList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// envIntOrDefault parses an optional positive-integer env var (DATA-2's
// pool-tuning knobs), falling back to def for an unset, empty, non-numeric,
// or non-positive value. A misconfigured value warns rather than crashing
// startup — pool tuning is an optimization, not something that should be
// able to take the whole service down.
func envIntOrDefault(envVar string, def int, logger *zap.Logger) int {
	raw := os.Getenv(envVar)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		logger.Warn("invalid value for env var, using default",
			zap.String("env_var", envVar), zap.String("value", raw), zap.Int("default", def))
		return def
	}
	return n
}

// envSecondsOrDefault parses an optional positive-integer-seconds env var,
// falling back to def under the same conditions as envIntOrDefault.
func envSecondsOrDefault(envVar string, def time.Duration, logger *zap.Logger) time.Duration {
	raw := os.Getenv(envVar)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		logger.Warn("invalid value for env var, using default",
			zap.String("env_var", envVar), zap.String("value", raw), zap.Duration("default", def))
		return def
	}
	return time.Duration(n) * time.Second
}
