package middleware

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/tombstone/flag-api/internal/secrets"
)

// actorKey is an unexported struct used as the context key for the authenticated actor.
// Using a struct type prevents collisions with any string-keyed context values.
type actorKey struct{ name string }

// ContextKeyActor is the context key for the authenticated actor identity.
// Must match the key used in v1.ActorContextKey.
var ContextKeyActor = actorKey{"actor"}

// ContextKeyServiceRole carries the role granted to an authenticated service
// token (SEC-1). It is set ONLY for service-token callers — human JWT callers
// have their role resolved from user_roles by RBACMiddleware.LoadRole. Resolving
// it here, at token-validation time, keeps it keyed by the unique token rather
// than by the non-unique service_tokens.name.
var ContextKeyServiceRole = actorKey{"service_role"}

type AuthMiddleware struct {
	db        *sql.DB
	jwtSecret []byte
	// hasher converts a presented service token into the keyed hash stored in
	// the database (SEC-4). Required — without it there is no way to look a
	// token up, since the plaintext is no longer stored.
	hasher *secrets.TokenHasher
}

func NewAuthMiddleware(db *sql.DB, jwtSecret string, hasher *secrets.TokenHasher) *AuthMiddleware {
	return &AuthMiddleware{db: db, jwtSecret: []byte(jwtSecret), hasher: hasher}
}

// Authenticate validates either a JWT (human user) or a service token (SDK).
func (a *AuthMiddleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			http.Error(w, `{"error":"missing Authorization header"}`, http.StatusUnauthorized)
			return
		}
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, `{"error":"invalid Authorization format"}`, http.StatusUnauthorized)
			return
		}
		token := parts[1]

		// Try JWT first
		if actor, ok := a.validateJWT(token); ok {
			ctx := context.WithValue(r.Context(), ContextKeyActor, actor)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Fallback: service token lookup
		if actor, role, projectID, ok := a.validateServiceToken(r.Context(), token); ok {
			ctx := context.WithValue(r.Context(), ContextKeyActor, actor)
			ctx = context.WithValue(ctx, ContextKeyServiceRole, role)
			ctx = context.WithValue(ctx, ContextKeyServiceProjectID, projectID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
	})
}

func (a *AuthMiddleware) validateJWT(tokenStr string) (string, bool) {
	tok, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return a.jwtSecret, nil
	})
	if err != nil || !tok.Valid {
		return "", false
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return "", false
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", false
	}
	return sub, true
}

// validateServiceToken resolves a service token to its actor identity, the
// role granted to that specific token, and the project it is scoped to.
// Returns (actor, role, projectID, ok).
//
// A token with no usable role falls back to VIEWER (read-only) rather than to a
// writable role: an unrecognized or missing role must never widen access.
func (a *AuthMiddleware) validateServiceToken(ctx context.Context, token string) (string, Role, string, bool) {
	// SEC-4: tokens are stored as HMAC(pepper, token), never as plaintext, so the
	// lookup is by hash. A missing hasher must reject rather than fall back to a
	// plaintext comparison, which would defeat hashing entirely.
	if a.hasher == nil {
		return "", "", "", false
	}
	var name, role, projectID string
	err := a.db.QueryRowContext(ctx, `
		SELECT name, role, project_id FROM service_tokens
		WHERE token_hash=$1 AND revoked_at IS NULL
	`, a.hasher.Hash(token)).Scan(&name, &role, &projectID)
	if err != nil {
		return "", "", "", false
	}
	resolved := Role(role)
	if _, known := permissionMatrix[resolved]; !known {
		resolved = RoleViewer
	}
	return "sdk:" + name, resolved, projectID, true
}
