package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

// SSOConfig holds the configuration for the SSO middleware.
type SSOConfig struct {
	Provider        string
	SAMLMetadataURL string
	OIDCIssuer      string
	OIDCClientID    string
	CallbackURL     string
	AllowedDomains  []string
}

// SSOMiddleware handles OIDC/SAML SSO flows.
type SSOMiddleware struct {
	config    SSOConfig
	jwtSecret []byte
	logger    *zap.Logger
}

// NewSSOMiddleware creates a new SSOMiddleware.
func NewSSOMiddleware(config SSOConfig, jwtSecret string, logger *zap.Logger) *SSOMiddleware {
	return &SSOMiddleware{
		config:    config,
		jwtSecret: []byte(jwtSecret),
		logger:    logger,
	}
}

// LoginHandler redirects the user to the OIDC authorization endpoint.
func (s *SSOMiddleware) LoginHandler(w http.ResponseWriter, r *http.Request) {
	state, err := s.generateState()
	if err != nil {
		s.logger.Error("sso: failed to generate state", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	params := url.Values{}
	params.Set("client_id", s.config.OIDCClientID)
	params.Set("redirect_uri", s.config.CallbackURL)
	params.Set("response_type", "code")
	params.Set("scope", "openid email")
	params.Set("state", state)

	authorizeURL := strings.TrimRight(s.config.OIDCIssuer, "/") + "/oauth/authorize?" + params.Encode()

	s.logger.Info("sso: redirecting to authorization endpoint",
		zap.String("provider", s.config.Provider),
		zap.String("state", state),
	)

	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// tokenResponse is the JSON body returned by the OIDC token endpoint.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// idTokenClaims holds the minimal claims parsed from the OIDC ID token.
type idTokenClaims struct {
	Email string `json:"email"`
	Sub   string `json:"sub"`
}

// CallbackHandler handles the OIDC authorization code callback.
// It exchanges the code for tokens, validates the email domain, and issues a
// Tombstone JWT.
func (s *SSOMiddleware) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	tokenResp, err := s.exchangeCode(code)
	if err != nil {
		s.logger.Error("sso: token exchange failed", zap.Error(err))
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}

	email, err := s.extractEmailFromIDToken(tokenResp.IDToken)
	if err != nil {
		s.logger.Error("sso: failed to extract email from id_token", zap.Error(err))
		http.Error(w, "invalid id_token", http.StatusBadGateway)
		return
	}

	if !s.isAllowedDomain(email) {
		s.logger.Warn("sso: email domain not allowed", zap.String("email", email))
		http.Error(w, "email domain not permitted", http.StatusForbidden)
		return
	}

	tombstoneToken, err := s.issueTombstoneJWT(email)
	if err != nil {
		s.logger.Error("sso: failed to issue tombstone jwt", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	s.logger.Info("sso: login successful", zap.String("email", email))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"token": tombstoneToken,
		"email": email,
	})
}

// exchangeCode posts the authorization code to the OIDC token endpoint and
// returns the parsed token response.
func (s *SSOMiddleware) exchangeCode(code string) (*tokenResponse, error) {
	tokenURL := strings.TrimRight(s.config.OIDCIssuer, "/") + "/oauth/token"

	params := url.Values{}
	params.Set("grant_type", "authorization_code")
	params.Set("code", code)
	params.Set("redirect_uri", s.config.CallbackURL)
	params.Set("client_id", s.config.OIDCClientID)

	resp, err := http.PostForm(tokenURL, params)
	if err != nil {
		return nil, fmt.Errorf("post to token endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned status %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if tr.IDToken == "" {
		return nil, fmt.Errorf("token response missing id_token")
	}
	return &tr, nil
}

// extractEmailFromIDToken decodes the middle (payload) segment of the JWT-format
// ID token and returns the email claim without verifying the signature.
// Full signature verification should be added for production use.
func (s *SSOMiddleware) extractEmailFromIDToken(idToken string) (string, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("id_token does not have three segments")
	}

	// JWT uses base64url encoding without padding.
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("base64 decode id_token payload: %w", err)
	}

	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("unmarshal id_token claims: %w", err)
	}
	if claims.Email == "" {
		return "", fmt.Errorf("id_token claims missing email")
	}
	return claims.Email, nil
}

// issueTombstoneJWT creates a signed HS256 JWT containing sub=email valid for
// 24 hours.
func (s *SSOMiddleware) issueTombstoneJWT(email string) (string, error) {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub": email,
		"iat": now.Unix(),
		"exp": now.Add(24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

// isAllowedDomain returns true when AllowedDomains is empty (no restriction) or
// when the email ends with one of the configured domain suffixes.
func (s *SSOMiddleware) isAllowedDomain(email string) bool {
	if len(s.config.AllowedDomains) == 0 {
		return true
	}
	lower := strings.ToLower(email)
	for _, domain := range s.config.AllowedDomains {
		suffix := "@" + strings.ToLower(strings.TrimPrefix(domain, "@"))
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// generateState produces a cryptographically-random 16-byte hex string for use
// as the OAuth2 state parameter.
func (s *SSOMiddleware) generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random state: %w", err)
	}
	return hex.EncodeToString(b), nil
}
