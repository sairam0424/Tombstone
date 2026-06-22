package middleware

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

type Role string

const (
	RoleViewer   Role = "VIEWER"
	RoleOperator Role = "OPERATOR"
	RoleOwner    Role = "OWNER"
	RoleAdmin    Role = "ADMIN"
)

type Permission struct {
	Resource string
	Action   string
}

// permissionMatrix maps Role -> set of allowed (resource, action) pairs
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
}

type RBACMiddleware struct {
	db     *sql.DB
	logger *zap.Logger
}

func NewRBACMiddleware(db *sql.DB, logger *zap.Logger) *RBACMiddleware {
	return &RBACMiddleware{db: db, logger: logger}
}

type contextKeyRole string

const ContextKeyRole contextKeyRole = "role"

// RequirePermission returns a middleware that enforces resource+action permission.
func (r *RBACMiddleware) RequirePermission(resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			role := r.getRoleFromContext(req.Context())
			if !r.hasPermission(role, resource, action) {
				r.logger.Warn("permission denied",
					zap.String("role", string(role)),
					zap.String("resource", resource),
					zap.String("action", action))
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
	// Service tokens are always OPERATOR
	if strings.HasPrefix(actor, "sdk:") {
		return RoleOperator
	}
	var role string
	err := r.db.QueryRowContext(ctx,
		"SELECT role FROM user_roles WHERE user_id = $1", actor).Scan(&role)
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

// RoleFromContext retrieves the role stored by LoadRole middleware from context.
// Returns RoleViewer if no role is present (least privilege default).
func RoleFromContext(ctx context.Context) Role {
	if v, ok := ctx.Value(ContextKeyRole).(Role); ok {
		return v
	}
	return RoleViewer
}

// actorFromContext retrieves the authenticated actor identifier from context.
// Uses the same contextKey type and ContextKeyActor constant defined in auth.go.
func actorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ContextKeyActor).(string); ok {
		return v
	}
	return "anonymous"
}
