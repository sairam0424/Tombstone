package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	oidcjwt "github.com/lestrrat-go/jwx/v3/jwt"
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

	// jwksMu guards jwksCache/jwksURL, lazily initialized on first ID-token
	// verification rather than in the constructor — a constructor that
	// blocks (or fails outright) on the IdP being reachable at Tombstone's
	// own startup would make an unrelated network hiccup an availability
	// incident for the whole service, not just login.
	jwksMu    sync.Mutex
	jwksCache *jwk.Cache
	jwksURL   string
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

// oidcDiscoveryDocument is the minimal subset of an OIDC provider's
// /.well-known/openid-configuration response this package needs.
type oidcDiscoveryDocument struct {
	JWKSURI string `json:"jwks_uri"`
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

	email, err := s.verifyIDTokenAndExtractEmail(r.Context(), tokenResp.IDToken)
	if err != nil {
		s.logger.Error("sso: id_token verification failed", zap.Error(err))
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

// verifyIDTokenAndExtractEmail verifies the ID token's signature against the
// issuer's published JWKS and returns its email claim.
//
// SEC-5: this used to decode the token's payload segment and trust its
// claims directly, with NO signature check at all — anything shaped like a
// 3-segment base64 JWT with an "email" field would be accepted, regardless
// of who (if anyone) actually signed it. That means Tombstone's own
// signature-verified, 24h-valid login token could be minted for an
// arbitrary email supplied by anything positioned to answer the token
// exchange (a compromised/misdirected network path to the IdP, a
// misconfigured issuer, etc.) — not necessarily the person who actually
// authenticated. This now cryptographically verifies the token was signed
// by a key the configured issuer actually publishes, for this exact client
// (aud) and issuer (iss), before trusting anything in it.
func (s *SSOMiddleware) verifyIDTokenAndExtractEmail(ctx context.Context, idToken string) (string, error) {
	keySet, err := s.jwks(ctx)
	if err != nil {
		return "", fmt.Errorf("fetch issuer jwks: %w", err)
	}

	tok, err := oidcjwt.Parse([]byte(idToken),
		// Infer the verification algorithm from the KEY we fetched from the
		// issuer's own trusted JWKS endpoint, never from the token's own
		// (attacker-influenceable) header — some IdPs (e.g. Azure AD) omit
		// "alg" from their published JWKs, so relying on the token header
		// alone would fail verification for them.
		oidcjwt.WithKeySet(keySet, jws.WithInferAlgorithmFromKey(true)),
		oidcjwt.WithIssuer(s.config.OIDCIssuer),
		oidcjwt.WithAudience(s.config.OIDCClientID),
	)
	if err != nil {
		return "", fmt.Errorf("verify id_token: %w", err)
	}

	var email string
	if err := tok.Get("email", &email); err != nil || email == "" {
		return "", fmt.Errorf("id_token missing email claim")
	}
	return email, nil
}

// jwks returns the issuer's current JSON Web Key Set, fetching and caching
// it (via OIDC discovery of the issuer's jwks_uri) on first use. A failed
// attempt is never cached — an early transient failure must not wedge SSO
// for the lifetime of the process.
func (s *SSOMiddleware) jwks(ctx context.Context) (jwk.Set, error) {
	s.jwksMu.Lock()
	cache, jwksURL := s.jwksCache, s.jwksURL
	s.jwksMu.Unlock()

	if cache == nil {
		discoveredURL, err := discoverJWKSURL(ctx, s.config.OIDCIssuer)
		if err != nil {
			return nil, fmt.Errorf("discover jwks_uri: %w", err)
		}

		newCache, err := jwk.NewCache(context.Background(), httprc.NewClient())
		if err != nil {
			return nil, fmt.Errorf("create jwks cache: %w", err)
		}
		if err := newCache.Register(ctx, discoveredURL); err != nil {
			return nil, fmt.Errorf("register jwks endpoint: %w", err)
		}

		s.jwksMu.Lock()
		s.jwksCache, s.jwksURL = newCache, discoveredURL
		cache, jwksURL = newCache, discoveredURL
		s.jwksMu.Unlock()
	}

	return cache.Lookup(ctx, jwksURL)
}

// discoverJWKSURL fetches the issuer's OIDC discovery document and returns
// its jwks_uri, per the standard /.well-known/openid-configuration convention.
func discoverJWKSURL(ctx context.Context, issuer string) (string, error) {
	discoveryURL := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", fmt.Errorf("build discovery request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch discovery document: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discovery endpoint returned status %d", resp.StatusCode)
	}

	var doc oidcDiscoveryDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("decode discovery document: %w", err)
	}
	if doc.JWKSURI == "" {
		return "", fmt.Errorf("discovery document missing jwks_uri")
	}
	return doc.JWKSURI, nil
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
