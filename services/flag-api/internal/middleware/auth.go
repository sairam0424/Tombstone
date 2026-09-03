package middleware

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/db/sqlcgen"
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
	// expiry (24h). iat is a Unix-seconds numeric claim per issueTombstoneJWT
	// — required, not best-effort: a token missing it entirely is
	// structurally different from anything this service ever mints, so it
	// fails closed here (distinct from a watermark LOOKUP error below,
	// which fails open because that's a DB-availability concern layered on
	// top of an otherwise well-formed token, not a malformed-token concern).
	iat, ok := claims["iat"].(float64)
	if !ok {
		return "", false
	}
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
	// later revokes against have no guaranteed casing relationship. Only
	// lower($1) is folded, not the indexed user_email column itself — the
	// sole writer (revokeUserRoles) already stores lower(email), so
	// wrapping the column too would only defeat its PK index for no
	// behavioral gain. Accepted tradeoff: Postgres's lower() is
	// Unicode-aware (this project's default collation is en_US.utf8), so
	// two subject strings differing only by a code point that happens to
	// fold to the same lowercase (e.g. U+212A KELVIN SIGN vs "k") would
	// collide here — the same class of risk the existing case-insensitive
	// role-revocation match already accepts, for the same reason: erring
	// toward revoking/matching too broadly is safer than a silent
	// case-mismatch no-op.
	validAfter, err := sqlcgen.New(a.db).GetTokenWatermark(ctx, sub)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.logger.Warn("auth: token watermark lookup failed, failing open", zap.Error(err))
		}
		return false
	}
	// Both sides compared at whole-second granularity, matching iat's own
	// precision: valid_after is a microsecond-precision TIMESTAMPTZ, and
	// comparing it directly against time.Unix(iat, 0) would spuriously
	// reject a token minted in the SAME wall-clock second as the watermark
	// (e.g. an immediate reactivate-then-login) even though iat can't
	// actually distinguish "before" from "same second" at that resolution.
	return iat < validAfter.Unix()
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
	resolvedToken, err := sqlcgen.New(a.db).ResolveServiceToken(ctx, sql.NullString{String: a.hasher.Hash(token), Valid: true})
	if err != nil {
		return "", "", "", false
	}
	name, role, projectID := resolvedToken.Name, resolvedToken.Role, resolvedToken.ProjectID
	resolved := Role(role)
	if _, known := permissionMatrix[resolved]; !known {
		resolved = RoleViewer
	}
	return "sdk:" + name, resolved, projectID, true
}
