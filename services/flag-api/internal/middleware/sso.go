package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
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

	"github.com/tombstone/flag-api/internal/db/sqlcgen"
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

// oauthSessionCookieName/oauthSessionMaxAge bound the lifetime of a single
// in-flight login attempt's CSRF state, OIDC nonce, and PKCE verifier. 15m
// (matching oauth2-proxy's --cookie-csrf-expire and next-auth's OAuth
// state/PKCE cookie default) rather than a tighter window, since the clock
// has to cover the IdP's own login/consent/step-up flow too — push-MFA
// approval, first-time device enrollment, or a delayed SMS OTP can each
// eat several minutes on their own before the user even gets back here.
const (
	oauthSessionCookieName = "tombstone_oauth_session"
	oauthSessionMaxAge     = 15 * time.Minute
)

// SSOMiddleware handles OIDC/SAML SSO flows.
type SSOMiddleware struct {
	config    SSOConfig
	jwtSecret []byte
	logger    *zap.Logger
	// db is used only for the best-effort user_mfa_log write in
	// CallbackHandler (SEC-5) — never for anything on the auth-decision path.
	db *sql.DB

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
func NewSSOMiddleware(config SSOConfig, jwtSecret string, logger *zap.Logger, db *sql.DB) *SSOMiddleware {
	return &SSOMiddleware{
		config:    config,
		jwtSecret: []byte(jwtSecret),
		logger:    logger,
		db:        db,
	}
}

// LoginHandler redirects the user to the OIDC authorization endpoint.
//
// SEC-5: state was previously generated but never validated on callback —
// generateState()'s only caller wrote it into the redirect and nothing ever
// read it back, so /auth/callback would accept a code+state pair regardless
// of whether this server ever issued that state. That is a CSRF hole in the
// classic OAuth sense: an attacker who starts their own login flow can hand
// the resulting authorization code (and any state string) to a victim and
// have the victim's browser complete it, binding the victim's session to
// the attacker's IdP account. This now stores state (plus a PKCE verifier
// and OIDC nonce) in a short-lived, HttpOnly, SameSite=Lax cookie that
// CallbackHandler must match before doing anything else.
func (s *SSOMiddleware) LoginHandler(w http.ResponseWriter, r *http.Request) {
	state, err := generateRandomHexToken(16)
	if err != nil {
		s.logger.Error("sso: failed to generate state", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	nonce, err := generateRandomHexToken(16)
	if err != nil {
		s.logger.Error("sso: failed to generate nonce", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	verifier, err := generatePKCEVerifier()
	if err != nil {
		s.logger.Error("sso: failed to generate pkce verifier", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if err := s.setOAuthSessionCookie(w, oauthSession{State: state, Nonce: nonce, Verifier: verifier}); err != nil {
		s.logger.Error("sso: failed to set oauth session cookie", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	params := url.Values{}
	params.Set("client_id", s.config.OIDCClientID)
	params.Set("redirect_uri", s.config.CallbackURL)
	params.Set("response_type", "code")
	params.Set("scope", "openid email")
	params.Set("state", state)
	params.Set("nonce", nonce)
	// PKCE (RFC 7636): binds the authorization code to whoever holds
	// `verifier`, so a code intercepted in transit (e.g. via a leaky
	// redirect_uri, browser history, or a referrer header) can't be
	// redeemed by anyone but this server.
	params.Set("code_challenge", pkceChallengeS256(verifier))
	params.Set("code_challenge_method", "S256")

	authorizeURL := strings.TrimRight(s.config.OIDCIssuer, "/") + "/oauth/authorize?" + params.Encode()

	s.logger.Info("sso: redirecting to authorization endpoint",
		zap.String("provider", s.config.Provider),
	)

	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// oauthSession is the CSRF state, OIDC nonce, and PKCE verifier for a single
// in-flight login attempt. All three are server-generated randomness with
// no meaning to the client — tampering with the cookie can only make
// validation fail, never succeed, so no separate signature over the cookie
// value is needed.
type oauthSession struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
}

func (s *SSOMiddleware) setOAuthSessionCookie(w http.ResponseWriter, sess oauthSession) error {
	raw, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("marshal oauth session: %w", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthSessionCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/auth",
		MaxAge:   int(oauthSessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   isSecureCallbackURL(s.config.CallbackURL),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// consumeOAuthSessionCookie reads the login attempt's session cookie and
// immediately expires it client-side — hygiene, so a stale single-purpose
// cookie doesn't linger in the browser past the login attempt it was
// generated for. The actual CSRF protection is the state comparison against
// this cookie's contents, not the expiry itself: expiry is not a
// server-side revocation, so a party that already possesses the raw cookie
// value (which HttpOnly keeps out of reach of any XSS on this origin) could
// still replay it — but a party in that position could equally well use the
// resulting Tombstone JWT directly.
func (s *SSOMiddleware) consumeOAuthSessionCookie(w http.ResponseWriter, r *http.Request) (*oauthSession, error) {
	cookie, err := r.Cookie(oauthSessionCookieName)
	if err != nil {
		return nil, fmt.Errorf("read oauth session cookie: %w", err)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     oauthSessionCookieName,
		Value:    "",
		Path:     "/auth",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureCallbackURL(s.config.CallbackURL),
		SameSite: http.SameSiteLaxMode,
	})

	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return nil, fmt.Errorf("decode oauth session cookie: %w", err)
	}
	var sess oauthSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("unmarshal oauth session cookie: %w", err)
	}
	if sess.State == "" || sess.Nonce == "" || sess.Verifier == "" {
		return nil, fmt.Errorf("incomplete oauth session cookie")
	}
	return &sess, nil
}

// isSecureCallbackURL reports whether the OAuth session cookie should carry
// the Secure flag. Required for any real deployment (CallbackURL is https);
// relaxed for http so local/CI testing works without standing up TLS —
// Secure cookies are silently dropped by browsers over plain http anyway,
// so this only ever narrows, never widens, what a real deployment gets.
func isSecureCallbackURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == "https"
}

// generatePKCEVerifier produces a PKCE code_verifier per RFC 7636: 32 random
// bytes, base64url-encoded (43 characters, all within RFC 7636's unreserved
// character set).
func generatePKCEVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate pkce verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceChallengeS256 derives the PKCE code_challenge for the S256 method.
func pkceChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
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
// It validates the CSRF state, exchanges the code for tokens (via PKCE),
// validates the email domain, and issues a Tombstone JWT.
func (s *SSOMiddleware) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := s.consumeOAuthSessionCookie(w, r)
	if err != nil {
		s.logger.Warn("sso: missing or invalid oauth session cookie", zap.Error(err))
		http.Error(w, "invalid or expired login attempt, please try again", http.StatusBadRequest)
		return
	}

	if !hmac.Equal([]byte(r.URL.Query().Get("state")), []byte(sess.State)) {
		s.logger.Warn("sso: state parameter mismatch")
		http.Error(w, "invalid state parameter", http.StatusForbidden)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	tokenResp, err := s.exchangeCode(code, sess.Verifier)
	if err != nil {
		s.logger.Error("sso: token exchange failed", zap.Error(err))
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}

	email, amr, err := s.verifyIDTokenAndExtractEmail(r.Context(), tokenResp.IDToken, sess.Nonce)
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

	// SEC-5 (SOC 2 CC6 evidence): dispatched in the background, not just
	// best-effort-but-synchronous — a slow user_mfa_log write must never
	// delay this response, and one that hangs past the server's
	// WriteTimeout must not be able to make an already-successful login look
	// like it failed to the client. context.Background(), not r.Context():
	// this write is meant to outlive the request. Mirrors
	// FlagHandler.writeAudit's async Rekor submission goroutine.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.logMFAEvent(ctx, email, amr)
	}()

	s.logger.Info("sso: login successful", zap.String("email", email))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"token": tombstoneToken,
		"email": email,
	})
}

// exchangeCode posts the authorization code (plus its PKCE verifier) to the
// OIDC token endpoint and returns the parsed token response.
func (s *SSOMiddleware) exchangeCode(code, codeVerifier string) (*tokenResponse, error) {
	tokenURL := strings.TrimRight(s.config.OIDCIssuer, "/") + "/oauth/token"

	params := url.Values{}
	params.Set("grant_type", "authorization_code")
	params.Set("code", code)
	params.Set("redirect_uri", s.config.CallbackURL)
	params.Set("client_id", s.config.OIDCClientID)
	params.Set("code_verifier", codeVerifier)

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
func (s *SSOMiddleware) verifyIDTokenAndExtractEmail(ctx context.Context, idToken, expectedNonce string) (email string, amr []string, err error) {
	keySet, err := s.jwks(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("fetch issuer jwks: %w", err)
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
		return "", nil, fmt.Errorf("verify id_token: %w", err)
	}

	if err := tok.Get("email", &email); err != nil || email == "" {
		return "", nil, fmt.Errorf("id_token missing email claim")
	}

	// email_verified=false means the IdP itself has not confirmed the user
	// controls this address — trusting it anyway would let anyone who can
	// self-assert an unconfirmed email at the IdP impersonate that address in
	// Tombstone once it clears the AllowedDomains check. Only reject when the
	// claim is explicitly present and not truthy; many IdPs omit it entirely,
	// and treating absence as untrusted would break those.
	if verified, present := emailVerifiedClaim(tok); present && !verified {
		return "", nil, fmt.Errorf("id_token email is not verified by issuer")
	}

	// nonce binds this ID token to the specific authorization request that
	// this server initiated (via LoginHandler) — without it, a token issued
	// for a different, unrelated login attempt (e.g. one an attacker
	// started themselves) would otherwise be indistinguishable from a
	// legitimate response to THIS callback, as long as it carries a valid
	// signature and matching iss/aud.
	var nonce string
	if err := tok.Get("nonce", &nonce); err != nil || nonce == "" {
		return "", nil, fmt.Errorf("id_token missing nonce claim")
	}
	if !hmac.Equal([]byte(nonce), []byte(expectedNonce)) {
		return "", nil, fmt.Errorf("id_token nonce does not match this login attempt")
	}

	// amr (Authentication Methods References, RFC 8176) is optional and
	// IdP-dependent — absence just means no MFA evidence either way, not
	// that MFA didn't happen. See classifyMFAEvent's own comment for why
	// this is treated as a best-effort signal, not a guarantee.
	amr = s.extractAMR(tok)

	return email, amr, nil
}

// mfaAMRValues are RFC 8176 Authentication Method Reference values (plus
// common vendor extensions for OTP/WebAuthn/U2F) that indicate a second
// factor was used, distinct from "pwd" (password) alone. This list is
// necessarily incomplete — amr values vary across IdPs, and there is no
// live IdP in this environment to test real-world coverage against. Treat
// classifyMFAEvent's output as a best-effort signal, not a guarantee.
var mfaAMRValues = map[string]bool{
	"mfa": true, "otp": true, "hwk": true, "swk": true, "sms": true,
	"tel": true, "face": true, "fpt": true, "iris": true, "vbm": true, "u2f": true,
}

// classifyMFAEvent decides what (if anything) to record in user_mfa_log
// for this login, based on the ID token's amr claim. Deliberately does NOT
// invent an event when amr is absent: many IdPs never send it, and
// fabricating "MFA was/wasn't used" from silence would be exactly the kind
// of false compliance evidence this project's "make the claims true"
// mandate exists to eliminate. mfa_required is never emitted here — this
// codebase has no MFA-required *policy* concept to log against yet (that
// would be a separate enforcement feature); this only records evidence for
// logins that assert a first-vs-second-factor makeup.
func classifyMFAEvent(amr []string) (eventType string, applicable bool) {
	if len(amr) == 0 {
		return "", false
	}
	for _, method := range amr {
		if mfaAMRValues[strings.ToLower(method)] {
			return "mfa_verified", true
		}
	}
	// amr was present and positively asserts a set of methods, none of
	// which indicate a second factor — e.g. amr=["pwd"]. Distinct from amr
	// being absent: this is actual evidence, not silence.
	return "mfa_bypassed", true
}

// extractAMR reads the ID token's "amr" (Authentication Methods References)
// claim. RFC 8176 specifies a JSON array of strings, but some IdPs and
// claim-mapping layers emit a bare string instead when there is exactly one
// method — accepted here as a single-element list rather than silently
// dropped. Returns nil when the claim is genuinely absent (expected,
// unlogged — amr is optional and its absence must never fail the login) and
// also logs a warning for the residual case where it's present in some
// other, unrecognized shape: without this, "no evidence either way" and
// "evidence we failed to parse" were indistinguishable, which is exactly
// backwards for a feature whose entire point is not fabricating evidence.
func (s *SSOMiddleware) extractAMR(tok oidcjwt.Token) []string {
	var raw any
	if err := tok.Get("amr", &raw); err != nil {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		var amr []string
		for _, item := range v {
			if str, ok := item.(string); ok {
				amr = append(amr, str)
			}
		}
		if len(amr) == 0 {
			s.logger.Warn("sso: amr claim present but contained no string entries — MFA evidence unavailable for this login, not confirmed absent")
		}
		return amr
	case string:
		return []string{v}
	default:
		s.logger.Warn("sso: amr claim present in an unrecognized shape — MFA evidence unavailable for this login, not confirmed absent",
			zap.String("go_type", fmt.Sprintf("%T", raw)))
		return nil
	}
}

// logMFAEvent writes a best-effort user_mfa_log entry (SOC 2 CC6 evidence).
// Never returns an error — a failed or skipped write must never affect the
// login it's describing.
func (s *SSOMiddleware) logMFAEvent(ctx context.Context, email string, amr []string) {
	eventType, applicable := classifyMFAEvent(amr)
	if !applicable {
		return
	}
	if s.db == nil {
		s.logger.Warn("sso: mfa event log skipped — no db configured", zap.String("event_type", eventType))
		return
	}
	if err := sqlcgen.New(s.db).InsertMFALogEvent(ctx, sqlcgen.InsertMFALogEventParams{UserID: email, EventType: eventType}); err != nil {
		s.logger.Warn("sso: mfa event log write failed", zap.Error(err), zap.String("event_type", eventType))
	}
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

// generateRandomHexToken produces a cryptographically-random n-byte hex
// string, used for both the OAuth2 state and OIDC nonce parameters.
func generateRandomHexToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
