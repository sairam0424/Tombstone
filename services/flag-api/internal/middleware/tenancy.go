package middleware

import (
	"context"
	"encoding/json"
	"net/http"

	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/db/sqlcgen"
)

// ContextKeyProjectID carries the request's resolved, authoritative
// project_id — the tenant every downstream query must filter by. Set by
// RequireProjectID; handlers must read it via ProjectIDFromContext instead of
// trusting any client-supplied project_id (body field or query param), which
// is how every flag/environment/prerequisite handler worked before TEN-1a —
// a caller could name ANY project's UUID and the query never checked it
// belonged to them.
var ContextKeyProjectID = actorKey{"project_id"}

// ContextKeyServiceProjectID carries the project_id bound to an authenticated
// service token (SEC-1's ContextKeyServiceRole sibling). Set at
// token-validation time in auth.go, from service_tokens.project_id — a
// service token is minted for exactly one project and that binding is not
// something a caller can widen or override.
var ContextKeyServiceProjectID = actorKey{"service_project_id"}

// ProjectIDFromContext returns the resolved project_id set by
// RequireProjectID. The second return value is false only if RequireProjectID
// did not run — which every /api/v1 route requires — so callers can treat a
// false as "this code path is misconfigured", not as a normal case to handle.
func ProjectIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ContextKeyProjectID).(string)
	return v, ok && v != ""
}

// RequireProjectID resolves and validates the caller's project_id. It must
// run after Authenticate (needs the actor) and before LoadRole (role
// resolution needs project_id to pick the right user_roles row — see
// rbac.go's resolveRole).
//
// Service tokens carry their OWN project_id (service_tokens.project_id,
// captured into context by auth.go at validation time) — that is
// authoritative and is used directly; a service token cannot act on behalf of
// any project other than the one it was minted for.
//
// Human JWT callers must send X-Project-Id explicitly: flag-api's only JWT
// issuer (sso.go's issueTombstoneJWT) signs nothing but `sub`, and a user CAN
// hold a role in more than one project (user_roles' primary key is
// (user_id, project_id)), so nothing about the token alone says which project
// a given request is for. The header is never trusted on its own — it is
// checked against user_roles before being accepted, so a caller cannot claim
// membership in a project they hold no role in.
func (r *RBACMiddleware) RequireProjectID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()

		if pid, ok := ctx.Value(ContextKeyServiceProjectID).(string); ok && pid != "" {
			next.ServeHTTP(w, req.WithContext(context.WithValue(ctx, ContextKeyProjectID, pid)))
			return
		}

		actor := actorFromContext(ctx)
		headerPID := req.Header.Get("X-Project-Id")
		if headerPID == "" {
			writeTenancyError(w, http.StatusBadRequest, "X-Project-Id header is required")
			return
		}

		member, err := sqlcgen.New(r.db).IsProjectMember(ctx, sqlcgen.IsProjectMemberParams{UserID: actor, ProjectID: headerPID})
		if err != nil || !member {
			r.logger.Warn("[tenancy] project membership denied",
				zap.String("actor", actor), zap.String("project_id", headerPID))
			writeTenancyError(w, http.StatusForbidden, "not a member of this project")
			return
		}

		next.ServeHTTP(w, req.WithContext(context.WithValue(ctx, ContextKeyProjectID, headerPID)))
	})
}

func writeTenancyError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
