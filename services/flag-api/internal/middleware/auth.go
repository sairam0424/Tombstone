package middleware

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// actorKey is an unexported struct used as the context key for the authenticated actor.
// Using a struct type prevents collisions with any string-keyed context values.
type actorKey struct{ name string }

// ContextKeyActor is the context key for the authenticated actor identity.
// Must match the key used in v1.ActorContextKey.
var ContextKeyActor = actorKey{"actor"}

type AuthMiddleware struct {
	db        *sql.DB
	jwtSecret []byte
}

func NewAuthMiddleware(db *sql.DB, jwtSecret string) *AuthMiddleware {
	return &AuthMiddleware{db: db, jwtSecret: []byte(jwtSecret)}
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
		if actor, ok := a.validateServiceToken(r.Context(), token); ok {
			ctx := context.WithValue(r.Context(), ContextKeyActor, actor)
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

func (a *AuthMiddleware) validateServiceToken(ctx context.Context, token string) (string, bool) {
	var name string
	err := a.db.QueryRowContext(ctx, `
		SELECT name FROM service_tokens
		WHERE token=$1 AND revoked_at IS NULL
	`, token).Scan(&name)
	if err != nil {
		return "", false
	}
	return "sdk:" + name, true
}
