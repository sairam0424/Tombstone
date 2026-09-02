package middleware

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

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
	logger *zap.Logger
}

func NewAuthMiddleware(db *sql.DB, jwtSecret string, hasher *secrets.TokenHasher, logger *zap.Logger) *AuthMiddleware {
	return &AuthMiddleware{db: db, jwtSecret: []byte(jwtSecret), hasher: hasher, logger: logger}
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
		if actor, ok := a.validateJWT(r.Context(), token); ok {
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

func (a *AuthMiddleware) validateJWT(ctx context.Context, tokenStr string) (string, bool) {
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

	// SEC-5: a token issued before the subject's most recent forced-logout
	// watermark must be rejected even though its signature and exp are
	// still valid — this closes the gap where SCIM deprovisioning deletes
	// user_roles but leaves an already-issued JWT valid until natural
	// expiry (24h). iat is a Unix-seconds numeric claim per issueTombstoneJWT.
	iat, _ := claims["iat"].(float64)
	if a.tokenPredatesWatermark(ctx, sub, int64(iat)) {
		return "", false
	}

	return sub, true
}

// tokenPredatesWatermark reports whether sub has a forced-logout watermark
// newer than iat, meaning this specific token must be rejected. Returns
// false (allow) both when no watermark row exists for sub (nobody has ever
// forced a logout for this subject) AND when the lookup itself errors —
// this is a defense-in-depth check layered on top of an already
// signature-verified, unexpired token, not the primary authentication
// decision, so a transient DB blip must not lock out every JWT-authenticated
// user the way it would if this failed closed. Matches the fail-open
// posture already established for rate limiting (ratelimit.go) and MFA
// event logging, not validateServiceToken's fail-closed DB lookup (which
// IS the primary authentication decision for that caller type).
func (a *AuthMiddleware) tokenPredatesWatermark(ctx context.Context, sub string, iat int64) bool {
	// Matched case-insensitively for the same reason SCIM's role-revocation
	// query is (internal/api/v1/scim.go's revokeUserRoles): the email in a
	// JWT's sub claim (asserted by the IdP at login) and the email SCIM
	// later revokes against have no guaranteed casing relationship.
	var validAfter time.Time
	err := a.db.QueryRowContext(ctx, `
		SELECT valid_after FROM user_token_watermarks WHERE lower(user_email) = lower($1)
	`, sub).Scan(&validAfter)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.logger.Warn("auth: token watermark lookup failed, failing open", zap.Error(err))
		}
		return false
	}
	return time.Unix(iat, 0).Before(validAfter)
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
