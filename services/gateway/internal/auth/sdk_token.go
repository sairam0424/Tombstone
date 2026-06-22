package auth

import (
	"net/http"
	"strings"

	"go.uber.org/zap"
)

// ValidateSDKToken returns middleware that enforces Bearer token authentication
// on all requests. The expected token is provided at construction time (typically
// read from the GATEWAY_AUTH_TOKEN environment variable).
//
// Behaviour:
//   - If gatewayToken is empty the middleware allows all requests through and
//     emits a one-time warning at startup (the caller is responsible for logging
//     the startup warning; this function does NOT log at startup to avoid
//     duplicate messages).
//   - Otherwise, the incoming Authorization header must be of the form
//     "Bearer <token>" where <token> matches gatewayToken exactly.
//   - On mismatch the middleware responds with HTTP 401 and does not call next.
func ValidateSDKToken(gatewayToken string, logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Dev / backward-compat mode: no token configured → allow all.
			if gatewayToken == "" {
				logger.Warn("GATEWAY_AUTH_TOKEN not set — request allowed without authentication",
					zap.String("path", r.URL.Path),
					zap.String("remote_addr", r.RemoteAddr),
				)
				next.ServeHTTP(w, r)
				return
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				logger.Warn("missing Authorization header",
					zap.String("path", r.URL.Path),
					zap.String("remote_addr", r.RemoteAddr),
				)
				http.Error(w, `{"error":"missing Authorization header"}`, http.StatusUnauthorized)
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				logger.Warn("malformed Authorization header",
					zap.String("path", r.URL.Path),
					zap.String("remote_addr", r.RemoteAddr),
				)
				http.Error(w, `{"error":"Authorization header must be: Bearer <token>"}`, http.StatusUnauthorized)
				return
			}

			token := parts[1]
			if token != gatewayToken {
				logger.Warn("invalid SDK token",
					zap.String("path", r.URL.Path),
					zap.String("remote_addr", r.RemoteAddr),
				)
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
