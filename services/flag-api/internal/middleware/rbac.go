package middleware

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/open-policy-agent/opa/rego"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/db/sqlcgen"
)

type Role string

const (
	RoleViewer   Role = "VIEWER"
	RoleOperator Role = "OPERATOR"
	RoleOwner    Role = "OWNER"
	RoleAdmin    Role = "ADMIN"
	// RoleCircuitBreaker is assignable ONLY via service_tokens.role
	// (migration 026) -- user_roles' own CHECK constraint has no such value,
	// so no human project-membership grant can ever resolve to this role.
	// Scopes flags:circuit_breaker (EVAL-4's automated rollback-step
	// endpoint) away from the human-held OWNER/ADMIN roles that already
	// hold flags:kill_switch.
	RoleCircuitBreaker Role = "CIRCUIT_BREAKER"
)

type Permission struct {
	Resource string
	Action   string
}

// permissionMatrix maps Role -> set of allowed (resource, action) pairs.
// Used as a fallback when OPA policy evaluation is unavailable.
var permissionMatrix = map[Role][]Permission{
	RoleViewer: {
		{Resource: "flags", Action: "read"},
		{Resource: "segments", Action: "read"},
		{Resource: "environments", Action: "read"},
		{Resource: "audit", Action: "read"},
		{Resource: "experiments", Action: "read"},
	},
	RoleOperator: {
		{Resource: "flags", Action: "read"},
		{Resource: "flags", Action: "write"},
		{Resource: "segments", Action: "read"},
		{Resource: "segments", Action: "write"},
		{Resource: "environments", Action: "read"},
		{Resource: "environments", Action: "write"},
		{Resource: "audit", Action: "read"},
	},
	RoleOwner: {
		{Resource: "flags", Action: "read"},
		{Resource: "flags", Action: "write"},
		{Resource: "flags", Action: "kill_switch"},
		{Resource: "segments", Action: "read"},
		{Resource: "segments", Action: "write"},
		{Resource: "environments", Action: "read"},
		{Resource: "environments", Action: "write"},
		{Resource: "approvals", Action: "read"},
		{Resource: "approvals", Action: "approve"},
		{Resource: "audit", Action: "read"},
		{Resource: "experiments", Action: "read"},
		{Resource: "experiments", Action: "write"},
	},
	RoleAdmin: {
		{Resource: "flags", Action: "read"},
		{Resource: "flags", Action: "write"},
		{Resource: "flags", Action: "kill_switch"},
		{Resource: "segments", Action: "read"},
		{Resource: "segments", Action: "write"},
		{Resource: "environments", Action: "read"},
		{Resource: "environments", Action: "write"},
		{Resource: "approvals", Action: "read"},
		{Resource: "approvals", Action: "approve"},
		{Resource: "audit", Action: "read"},
		{Resource: "experiments", Action: "read"},
		{Resource: "experiments", Action: "write"},
		{Resource: "admin", Action: "admin"},
	},
	// RoleCircuitBreaker deliberately holds neither flags:write nor
	// flags:kill_switch -- its entire purpose is the automated rollback-step
	// endpoint, not general flag administration. flags:read lets a future
	// caller sanity-check current state before acting; nothing else.
	RoleCircuitBreaker: {
		{Resource: "flags", Action: "read"},
		{Resource: "flags", Action: "circuit_breaker"},
	},
}

// opaEvaluator holds a compiled OPA query that can be swapped atomically on hot-reload.
type opaEvaluator struct {
	mu        sync.RWMutex
	preparedQ *rego.PreparedEvalQuery
	available bool
	policyDir string
	query     string
}

func newOPAEvaluator(policyDir, query string, logger *zap.Logger) *opaEvaluator {
	e := &opaEvaluator{policyDir: policyDir, query: query}
	if err := e.load(logger); err != nil {
		logger.Warn("[rbac] OPA policy load failed — falling back to hardcoded matrix",
			zap.String("policy_dir", policyDir), zap.Error(err))
	}
	return e
}

// load reads all .rego files in policyDir and compiles the query.
func (e *opaEvaluator) load(logger *zap.Logger) error {
	files, err := filepath.Glob(filepath.Join(e.policyDir, "*.rego"))
	if err != nil || len(files) == 0 {
		e.mu.Lock()
		e.available = false
		e.preparedQ = nil
		e.mu.Unlock()
		return err
	}

	r := rego.New(
		rego.Query(e.query),
		rego.Load(files, nil),
	)

	ctx := context.Background()
	pq, err := r.PrepareForEval(ctx)
	if err != nil {
		e.mu.Lock()
		e.available = false
		e.preparedQ = nil
		e.mu.Unlock()
		return err
	}

	e.mu.Lock()
	e.preparedQ = &pq
	e.available = true
	e.mu.Unlock()

	if logger != nil {
		logger.Info("[rbac] OPA policies loaded", zap.Strings("files", files))
	}
	return nil
}

// evaluate runs the OPA query against input. Returns (allow, true) if OPA is
// available, or (false, false) if the fallback should be used.
func (e *opaEvaluator) evaluate(ctx context.Context, input map[string]interface{}) (allow bool, ok bool) {
	e.mu.RLock()
	pq := e.preparedQ
	avail := e.available
	e.mu.RUnlock()

	if !avail || pq == nil {
		return false, false
	}

	rs, err := pq.Eval(ctx, rego.EvalInput(input))
	if err != nil || len(rs) == 0 {
		return false, false
	}
	if v, isBool := rs[0].Expressions[0].Value.(bool); isBool {
		return v, true
	}
	return false, false
}

// RBACMiddleware enforces role-based access control via OPA (primary) with a
// hardcoded permission matrix as fallback.
type RBACMiddleware struct {
	db        *sql.DB
	logger    *zap.Logger
	flagsEval *opaEvaluator
}

func NewRBACMiddleware(db *sql.DB, logger *zap.Logger) *RBACMiddleware {
	policyDir := os.Getenv("POLICY_DIR")
	if policyDir == "" {
		policyDir = "policies/"
	}

	mw := &RBACMiddleware{
		db:        db,
		logger:    logger,
		flagsEval: newOPAEvaluator(policyDir, "data.tombstone.flags.allow", logger),
	}

	// Start fsnotify watcher for hot-reload.
	go mw.watchPolicies(policyDir)

	return mw
}

// watchPolicies monitors policyDir for .rego file changes and triggers reloads.
func (mw *RBACMiddleware) watchPolicies(policyDir string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		mw.logger.Warn("[rbac] fsnotify unavailable — hot-reload disabled", zap.Error(err))
		return
	}
	defer watcher.Close()

	if err := watcher.Add(policyDir); err != nil {
		mw.logger.Warn("[rbac] cannot watch policy dir — hot-reload disabled",
			zap.String("dir", policyDir), zap.Error(err))
		return
	}

	mw.logger.Info("[rbac] watching policy directory for changes", zap.String("dir", policyDir))

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(event.Name, ".rego") {
				continue
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Remove) {
				mw.logger.Info("[rbac] policy file changed — reloading", zap.String("file", event.Name))
				if err := mw.flagsEval.load(mw.logger); err != nil {
					mw.logger.Error("[rbac] policy reload failed — keeping last-known-good policy",
						zap.Error(err))
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			mw.logger.Error("[rbac] fsnotify error", zap.Error(err))
		}
	}
}

type contextKeyRole string

const ContextKeyRole contextKeyRole = "role"

// PolicySource reports which authorization source is actually live: "opa" when
// Rego policies are compiled and loaded, "fallback_matrix" when the hardcoded
// permissionMatrix is in use. Compliance evidence reports this instead of
// asserting a hardcoded rbac_enabled=true (AUD-1).
func (r *RBACMiddleware) PolicySource() string {
	if r.flagsEval == nil {
		return "fallback_matrix"
	}
	r.flagsEval.mu.RLock()
	defer r.flagsEval.mu.RUnlock()
	if r.flagsEval.available && r.flagsEval.preparedQ != nil {
		return "opa"
	}
	return "fallback_matrix"
}

// RequirePermission returns a middleware that enforces resource+action permission.
// It first tries OPA evaluation; if unavailable, falls back to the hardcoded matrix.
func (r *RBACMiddleware) RequirePermission(resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			role := r.getRoleFromContext(req.Context())
			actor := actorFromContext(req.Context())

			allowed, source := r.checkPermissionWithOPA(req, role, resource, action)

			r.logger.Debug("[rbac] decision",
				zap.String("actor", actor),
				zap.String("method", req.Method),
				zap.String("path", req.URL.Path),
				zap.Bool("allow", allowed),
				zap.String("source", source),
			)

			if !allowed {
				r.logger.Warn("[rbac] permission denied",
					zap.String("actor", actor),
					zap.String("method", req.Method),
					zap.String("path", req.URL.Path),
					zap.String("role", string(role)),
					zap.String("resource", resource),
					zap.String("action", action),
					zap.String("source", source),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error":     "insufficient permissions",
					"required":  resource + ":" + action,
					"your_role": string(role),
				})
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

// checkPermissionWithOPA tries OPA first, then falls back to the hardcoded matrix.
// Returns (allowed bool, source string) where source is "opa" or "fallback".
func (r *RBACMiddleware) checkPermissionWithOPA(req *http.Request, role Role, resource, action string) (bool, string) {
	actor := actorFromContext(req.Context())
	pathParts := splitPath(req.URL.Path)

	// resource/action are the SAME two strings the route handler passed to
	// RequirePermission(resource, action) — this is the permission actually
	// being checked. Before this fix they were dropped here, leaving OPA to
	// approximate the decision from method+path alone; that approximation
	// (flags.rego) was looser than permissionMatrix on every gated route
	// (e.g. any role could GET anything, any operator could POST/PATCH
	// anything under /api/...), so a merged SEC-1 fix was silently defeated
	// at runtime whenever OPA was live — which is the default, since
	// POLICY_DIR defaults to "policies/" and that directory ships in the repo.
	input := map[string]interface{}{
		"method":   req.Method,
		"path":     pathParts,
		"role":     strings.ToLower(string(role)),
		"actor":    actor,
		"resource": resource,
		"action":   action,
	}

	if allow, ok := r.flagsEval.evaluate(req.Context(), input); ok {
		return allow, "opa"
	}

	// OPA unavailable — use hardcoded fallback.
	return r.hasPermission(role, resource, action), "fallback"
}

// LoadRole middleware resolves the actor's role from the database and injects it into context.
func (r *RBACMiddleware) LoadRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		actor := actorFromContext(req.Context())
		role := r.resolveRole(req.Context(), actor)
		ctx := context.WithValue(req.Context(), ContextKeyRole, role)
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

func (r *RBACMiddleware) resolveRole(ctx context.Context, actor string) Role {
	// Service tokens carry a per-token role, resolved by AuthMiddleware at
	// token-validation time (SEC-1). Previously every service token was
	// hardcoded to OPERATOR, so any SDK token could write flags and change
	// production rollouts. Absent/unknown role => VIEWER (least privilege).
	if strings.HasPrefix(actor, "sdk:") {
		if role, ok := ctx.Value(ContextKeyServiceRole).(Role); ok {
			if _, known := permissionMatrix[role]; known {
				return role
			}
		}
		return RoleViewer
	}

	// user_roles' primary key is (user_id, project_id) — a user can hold
	// different roles in different projects. Querying by user_id alone (as
	// this did before TEN-1a) is nondeterministic whenever a user belongs to
	// more than one project: Postgres returns SOME row with no defined
	// ordering, so which project's role applies could vary request to
	// request. RequireProjectID runs before LoadRole precisely so a specific,
	// validated project_id is available here.
	projectID, ok := ProjectIDFromContext(ctx)
	if !ok {
		return RoleViewer // no resolved project => least privilege, never a guess
	}
	role, err := sqlcgen.New(r.db).GetUserRole(ctx, sqlcgen.GetUserRoleParams{UserID: actor, ProjectID: projectID})
	if err != nil {
		return RoleViewer // default to least privilege
	}
	return Role(role)
}

func (r *RBACMiddleware) getRoleFromContext(ctx context.Context) Role {
	if v, ok := ctx.Value(ContextKeyRole).(Role); ok {
		return v
	}
	return RoleViewer
}

func (r *RBACMiddleware) hasPermission(role Role, resource, action string) bool {
	perms, ok := permissionMatrix[role]
	if !ok {
		return false
	}
	for _, p := range perms {
		if p.Resource == resource && p.Action == action {
			return true
		}
	}
	return false
}

// splitPath splits a URL path into non-empty segments.
func splitPath(path string) []string {
	parts := strings.Split(path, "/")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// actorFromContext retrieves the authenticated actor identifier from context.
// Uses the same contextKey type and ContextKeyActor constant defined in auth.go.
func actorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ContextKeyActor).(string); ok {
		return v
	}
	return "anonymous"
}
