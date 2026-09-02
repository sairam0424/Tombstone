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

// oidcDiscoveryTimeout bounds the /.well-known/openid-configuration fetch —
// without it, a stalled discovery endpoint blocks the (unauthenticated)
// /auth/callback handler indefinitely, since neither http.DefaultClient nor
// the incoming request's context enforces any deadline here on its own.
const oidcDiscoveryTimeout = 10 * time.Second

// jwksInitCooldown bounds how often a failed JWKS cache initialization is
// retried. Without it, every concurrent or repeated login attempt during an
// IdP outage independently re-hits the (already struggling) discovery and
// JWKS endpoints with no backoff at all.
const jwksInitCooldown = 5 * time.Second

// SSOMiddleware handles OIDC/SAML SSO flows.
type SSOMiddleware struct {
	config    SSOConfig
	jwtSecret []byte
	logger    *zap.Logger

	// jwksMu guards the fields below and is held for the FULL duration of a
	// cold JWKS-cache initialization, not just the read/write of the cached
	// pointer — a deliberate, minimal "poor man's singleflight". Without
	// holding the lock across the whole init, concurrent cold-start callers
	// would each independently spin up their own jwk.Cache/httprc.Client, and
	// every loser's cache would be discarded without ever being shut down,
	// permanently leaking its background goroutines and an
	// indefinitely-running JWKS refresh subscription against the real IdP.
	//
	// Lazy (not constructor-time) initialization avoids blocking Tombstone's
	// own startup on IdP reachability — an unrelated network hiccup at boot
	// must not become an availability incident for the whole service, not
	// just login.
	jwksMu          sync.Mutex
	jwksCache       *jwk.Cache
	jwksURL         string
	jwksLastFailAt  time.Time
	jwksLastFailErr error
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
		// OIDC Core 1.0 §2 lists both exp and iat as REQUIRED ID Token
		// claims. jwt.Parse's default time-claim validators only check
		// exp/iat *if present* — a token that omits them entirely would
		// otherwise pass with no expiration enforced at all.
		oidcjwt.WithRequiredClaim(oidcjwt.ExpirationKey),
		oidcjwt.WithRequiredClaim(oidcjwt.IssuedAtKey),
	)
	if err != nil {
		return "", fmt.Errorf("verify id_token: %w", err)
	}

	var email string
	if err := tok.Get("email", &email); err != nil || email == "" {
		return "", fmt.Errorf("id_token missing email claim")
	}

	// email_verified=false means the IdP itself has not confirmed the user
	// controls this address — trusting it anyway would let anyone who can
	// self-assert an unconfirmed email at the IdP impersonate that address in
	// Tombstone once it clears the AllowedDomains check. Only reject when the
	// claim is explicitly present and not truthy; many IdPs omit it entirely,
	// and treating absence as untrusted would break those.
	if verified, present := emailVerifiedClaim(tok); present && !verified {
		return "", fmt.Errorf("id_token email is not verified by issuer")
	}

	return email, nil
}

// emailVerifiedClaim returns the token's email_verified claim and whether it
// was present at all. Both bool and string ("true"/"false") representations
// are handled — some IdPs (e.g. AWS Cognito) send it as a string.
func emailVerifiedClaim(tok oidcjwt.Token) (verified bool, present bool) {
	var raw any
	if err := tok.Get("email_verified", &raw); err != nil {
		return false, false
	}
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		return v == "true", true
	default:
		return false, true
	}
}

// jwks returns the issuer's current JSON Web Key Set, fetching and caching
// it (via OIDC discovery of the issuer's jwks_uri) on first use. A failed
// attempt is never cached beyond jwksInitCooldown — an early transient
// failure must not wedge SSO for the lifetime of the process, but repeated
// failures must not retry unboundedly fast either.
func (s *SSOMiddleware) jwks(ctx context.Context) (jwk.Set, error) {
	s.jwksMu.Lock()
	defer s.jwksMu.Unlock()

	if s.jwksCache == nil {
		if !s.jwksLastFailAt.IsZero() && time.Since(s.jwksLastFailAt) < jwksInitCooldown {
			return nil, fmt.Errorf("jwks initialization failed recently, not retrying yet: %w", s.jwksLastFailErr)
		}

		cache, discoveredURL, err := initJWKSCache(ctx, s.config.OIDCIssuer)
		if err != nil {
			s.jwksLastFailAt = time.Now()
			s.jwksLastFailErr = err
			return nil, err
		}
		s.jwksCache, s.jwksURL = cache, discoveredURL
		s.jwksLastFailAt = time.Time{}
		s.jwksLastFailErr = nil
	}

	return s.jwksCache.Lookup(ctx, s.jwksURL)
}

// initJWKSCache performs OIDC discovery and registers the resulting
// jwks_uri with a fresh jwk.Cache. If Register fails, the already-started
// cache is shut down before returning — otherwise its background
// httprc.Client goroutines would leak for the remaining process lifetime.
func initJWKSCache(ctx context.Context, issuer string) (*jwk.Cache, string, error) {
	discoveredURL, err := discoverJWKSURL(ctx, issuer)
	if err != nil {
		return nil, "", fmt.Errorf("discover jwks_uri: %w", err)
	}

	cache, err := jwk.NewCache(context.Background(), httprc.NewClient())
	if err != nil {
		return nil, "", fmt.Errorf("create jwks cache: %w", err)
	}
	if err := cache.Register(ctx, discoveredURL); err != nil {
		_ = cache.Shutdown(context.Background())
		return nil, "", fmt.Errorf("register jwks endpoint: %w", err)
	}
	return cache, discoveredURL, nil
}

// discoverJWKSURL fetches the issuer's OIDC discovery document and returns
// its jwks_uri, per the standard /.well-known/openid-configuration convention.
func discoverJWKSURL(ctx context.Context, issuer string) (string, error) {
	if !isAllowedDiscoveryScheme(issuer) {
		return "", fmt.Errorf("issuer %q must use https (plain http is only allowed for loopback testing)", issuer)
	}

	ctx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()

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
	if !isAllowedDiscoveryScheme(doc.JWKSURI) {
		return "", fmt.Errorf("discovery document's jwks_uri %q must use https (plain http is only allowed for loopback testing)", doc.JWKSURI)
	}
	return doc.JWKSURI, nil
}

// isAllowedDiscoveryScheme requires https — the entire cryptographic
// guarantee this file adds depends on the JWKS being fetched over a channel
// an attacker cannot tamper with — with a narrow loopback exception so
// local/CI tests (and genuinely local dev IdPs) can use plain http.
func isAllowedDiscoveryScheme(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		return host == "127.0.0.1" || host == "::1" || host == "localhost"
	default:
		return false
	}
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
