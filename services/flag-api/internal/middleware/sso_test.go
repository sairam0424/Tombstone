package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	stdjwt "github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	oidcjwt "github.com/lestrrat-go/jwx/v3/jwt"
	"go.uber.org/zap"
)

// ssoTestIdP is a fully local, offline stand-in for an OIDC provider: it
// serves a discovery document and a JWKS over a real httptest server (so
// SSOMiddleware's actual discovery+fetch code path runs unmodified), and can
// sign ID tokens with its own private key. No real network access, no real
// IdP required.
type ssoTestIdP struct {
	server         *httptest.Server
	signingKey     jwk.Key
	publicSet      jwk.Set
	rsaPub         *rsa.PublicKey
	discoveryCalls atomic.Int32
	failDiscovery  atomic.Bool
}

func newSSOTestIdP(t *testing.T) *ssoTestIdP {
	return newSSOTestIdPOpts(t, true)
}

// newSSOTestIdPOpts is like newSSOTestIdP but lets a test omit "alg" from
// the published JWK — some real IdPs (e.g. Azure AD) do this, and
// verifyIDTokenAndExtractEmail is expected to still verify via
// jws.WithInferAlgorithmFromKey.
func newSSOTestIdPOpts(t *testing.T, keyHasAlg bool) *ssoTestIdP {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	signingKey, err := jwk.Import(privKey)
	if err != nil {
		t.Fatalf("import jwk: %v", err)
	}
	if err := signingKey.Set(jwk.KeyIDKey, "test-key"); err != nil {
		t.Fatalf("set kid: %v", err)
	}
	if keyHasAlg {
		if err := signingKey.Set(jwk.AlgorithmKey, jwa.RS256()); err != nil {
			t.Fatalf("set alg: %v", err)
		}
	}

	privSet := jwk.NewSet()
	if err := privSet.AddKey(signingKey); err != nil {
		t.Fatalf("add key to set: %v", err)
	}
	publicSet, err := jwk.PublicSetOf(privSet)
	if err != nil {
		t.Fatalf("derive public jwks: %v", err)
	}

	idp := &ssoTestIdP{signingKey: signingKey, publicSet: publicSet, rsaPub: &privKey.PublicKey}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		idp.discoveryCalls.Add(1)
		if idp.failDiscovery.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jwks_uri":%q}`, idp.server.URL+"/jwks.json")
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(idp.publicSet); err != nil {
			t.Errorf("encode jwks response: %v", err)
		}
	})

	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

// testNonce is the default nonce issueIDToken embeds (unless a test sets its
// own "nonce" claim) and the value verifyIDTokenAndExtractEmail's callers
// below pass as expectedNonce — tests that aren't specifically exercising
// nonce validation just need the two sides to agree.
const testNonce = "test-nonce"

// issueIDToken signs an ID token with the IdP's own key — the key the
// discovery/JWKS endpoints above actually publish.
func (idp *ssoTestIdP) issueIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	if _, ok := claims["nonce"]; !ok {
		claims["nonce"] = testNonce
	}
	tok := oidcjwt.New()
	for k, v := range claims {
		if err := tok.Set(k, v); err != nil {
			t.Fatalf("set claim %q: %v", k, err)
		}
	}
	signed, err := oidcjwt.Sign(tok, oidcjwt.WithKey(jwa.RS256(), idp.signingKey))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return string(signed)
}

func testSSOMiddleware(idp *ssoTestIdP, clientID string) *SSOMiddleware {
	return &SSOMiddleware{
		config: SSOConfig{OIDCIssuer: idp.server.URL, OIDCClientID: clientID},
		logger: zap.NewNop(),
	}
}

func TestVerifyIDTokenAndExtractEmail_ValidTokenSucceeds(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	now := time.Now()
	token := idp.issueIDToken(t, map[string]any{
		"iss":   idp.server.URL,
		"aud":   []string{"test-client"},
		"email": "alice@example.com",
		"iat":   now,
		"exp":   now.Add(time.Hour),
	})

	email, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", email)
	}
}

// TestVerifyIDTokenAndExtractEmail_UnsignedForgedTokenRejected is the direct
// regression proof: the OLD code decoded the payload segment and trusted
// whatever "email" it found, with NO signature check at all. This token has
// a well-formed header and a plausible-looking payload, but its signature
// segment is garbage. The old code would have returned "attacker@evil.com"
// as a verified identity; this must now be rejected outright.
func TestVerifyIDTokenAndExtractEmail_UnsignedForgedTokenRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"iss":%q,"aud":%q,"email":"attacker@evil.com","exp":%d}`,
		idp.server.URL, "test-client", time.Now().Add(time.Hour).Unix(),
	)))
	forged := header + "." + payload + ".not-a-real-signature"

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), forged, testNonce); err == nil {
		t.Fatal("a token with a fabricated signature must not verify, even with well-formed, " +
			"plausible-looking claims — this is exactly the vulnerability class being fixed")
	}
}

func TestVerifyIDTokenAndExtractEmail_WrongSigningKeyRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	// A key the IdP's JWKS endpoint never published — simulates an attacker
	// (or a compromised/misdirected network path) signing their own token.
	bogusPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate bogus key: %v", err)
	}
	bogusKey, err := jwk.Import(bogusPriv)
	if err != nil {
		t.Fatalf("import bogus jwk: %v", err)
	}
	if err := bogusKey.Set(jwk.AlgorithmKey, jwa.RS256()); err != nil {
		t.Fatalf("set alg: %v", err)
	}

	tok := oidcjwt.New()
	_ = tok.Set("iss", idp.server.URL)
	_ = tok.Set("aud", []string{"test-client"})
	_ = tok.Set("email", "attacker@evil.com")
	_ = tok.Set("exp", time.Now().Add(time.Hour))
	signed, err := oidcjwt.Sign(tok, oidcjwt.WithKey(jwa.RS256(), bogusKey))
	if err != nil {
		t.Fatalf("sign with bogus key: %v", err)
	}

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), string(signed), testNonce); err == nil {
		t.Fatal("a token signed with a key the issuer never published must not verify")
	}
}

func TestVerifyIDTokenAndExtractEmail_WrongAudienceRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	token := idp.issueIDToken(t, map[string]any{
		"iss":   idp.server.URL,
		"aud":   []string{"someone-elses-client"},
		"email": "alice@example.com",
		"exp":   time.Now().Add(time.Hour),
	})

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce); err == nil {
		t.Fatal("a token issued for a different client (aud) must not verify")
	}
}

func TestVerifyIDTokenAndExtractEmail_WrongIssuerRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	token := idp.issueIDToken(t, map[string]any{
		"iss":   "https://not-our-configured-issuer.example",
		"aud":   []string{"test-client"},
		"email": "alice@example.com",
		"exp":   time.Now().Add(time.Hour),
	})

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce); err == nil {
		t.Fatal("a token whose iss does not match the configured issuer must not verify")
	}
}

func TestVerifyIDTokenAndExtractEmail_ExpiredTokenRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	token := idp.issueIDToken(t, map[string]any{
		"iss":   idp.server.URL,
		"aud":   []string{"test-client"},
		"email": "alice@example.com",
		"exp":   time.Now().Add(-time.Hour),
	})

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce); err == nil {
		t.Fatal("an expired token must not verify")
	}
}

func TestVerifyIDTokenAndExtractEmail_MissingEmailClaimRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	token := idp.issueIDToken(t, map[string]any{
		"iss": idp.server.URL,
		"aud": []string{"test-client"},
		"exp": time.Now().Add(time.Hour),
	})

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce); err == nil {
		t.Fatal("a validly-signed token with no email claim must not resolve to an identity")
	}
}

// TestVerifyIDTokenAndExtractEmail_NonceMismatchRejected is the direct
// regression proof for the missing-nonce-validation gap: a validly-signed
// token for the right iss/aud can still be a response to a DIFFERENT
// authorization request than the one this callback is completing (e.g. one
// an attacker started themselves). Without checking nonce, that token would
// be indistinguishable from a legitimate response to THIS login attempt.
func TestVerifyIDTokenAndExtractEmail_NonceMismatchRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	now := time.Now()
	token := idp.issueIDToken(t, map[string]any{
		"iss":   idp.server.URL,
		"aud":   []string{"test-client"},
		"email": "alice@example.com",
		"nonce": "nonce-from-a-different-login-attempt",
		"iat":   now,
		"exp":   now.Add(time.Hour),
	})

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce); err == nil {
		t.Fatal("a token whose nonce does not match this login attempt's nonce must not verify")
	}
}

func TestVerifyIDTokenAndExtractEmail_MissingNonceRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	now := time.Now()
	tok := oidcjwt.New()
	for k, v := range map[string]any{
		"iss":   idp.server.URL,
		"aud":   []string{"test-client"},
		"email": "alice@example.com",
		"iat":   now,
		"exp":   now.Add(time.Hour),
	} {
		if err := tok.Set(k, v); err != nil {
			t.Fatalf("set claim %q: %v", k, err)
		}
	}
	signed, err := oidcjwt.Sign(tok, oidcjwt.WithKey(jwa.RS256(), idp.signingKey))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), string(signed), testNonce); err == nil {
		t.Fatal("a validly-signed token with no nonce claim must not verify once a nonce is expected")
	}
}

// TestVerifyIDTokenAndExtractEmail_UnverifiedEmailRejected is the direct
// regression proof for the email_verified gap: a token can be genuinely
// signed by the real IdP key, with correct iss/aud/exp, and still carry an
// IdP-user-supplied, unconfirmed email. Trusting it anyway would let anyone
// who can self-assert an unconfirmed email at the IdP impersonate that
// address in Tombstone once it clears the AllowedDomains check.
func TestVerifyIDTokenAndExtractEmail_UnverifiedEmailRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	now := time.Now()
	token := idp.issueIDToken(t, map[string]any{
		"iss":            idp.server.URL,
		"aud":            []string{"test-client"},
		"email":          "victim@allowed-domain.example",
		"email_verified": false,
		"iat":            now,
		"exp":            now.Add(time.Hour),
	})

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce); err == nil {
		t.Fatal("a validly-signed token with email_verified=false must not resolve to a trusted identity")
	}
}

func TestVerifyIDTokenAndExtractEmail_EmailVerifiedAsStringFalseRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	now := time.Now()
	token := idp.issueIDToken(t, map[string]any{
		"iss":            idp.server.URL,
		"aud":            []string{"test-client"},
		"email":          "victim@allowed-domain.example",
		"email_verified": "false",
		"iat":            now,
		"exp":            now.Add(time.Hour),
	})

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce); err == nil {
		t.Fatal(`email_verified sent as the string "false" (e.g. AWS Cognito) must be rejected the same as the boolean`)
	}
}

func TestVerifyIDTokenAndExtractEmail_MissingEmailVerifiedAccepted(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	now := time.Now()
	token := idp.issueIDToken(t, map[string]any{
		"iss":   idp.server.URL,
		"aud":   []string{"test-client"},
		"email": "alice@example.com",
		"iat":   now,
		"exp":   now.Add(time.Hour),
	})

	email, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce)
	if err != nil {
		t.Fatalf("an IdP that omits email_verified entirely must still be accepted for compatibility: %v", err)
	}
	if email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", email)
	}
}

func TestVerifyIDTokenAndExtractEmail_MissingExpRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	token := idp.issueIDToken(t, map[string]any{
		"iss":   idp.server.URL,
		"aud":   []string{"test-client"},
		"email": "alice@example.com",
		"iat":   time.Now(),
	})

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce); err == nil {
		t.Fatal("a validly-signed token with no exp claim must not verify — exp is a required OIDC ID token claim")
	}
}

func TestVerifyIDTokenAndExtractEmail_MissingIatRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	token := idp.issueIDToken(t, map[string]any{
		"iss":   idp.server.URL,
		"aud":   []string{"test-client"},
		"email": "alice@example.com",
		"exp":   time.Now().Add(time.Hour),
	})

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce); err == nil {
		t.Fatal("a validly-signed token with no iat claim must not verify — iat is a required OIDC ID token claim")
	}
}

// TestVerifyIDTokenAndExtractEmail_KeyWithoutAlgFieldStillVerifies exercises
// jws.WithInferAlgorithmFromKey — without it, this would fail with "no
// matching keys were provided by any key provider" against a JWKS entry
// that omits alg, exactly as real IdPs like Azure AD do.
func TestVerifyIDTokenAndExtractEmail_KeyWithoutAlgFieldStillVerifies(t *testing.T) {
	idp := newSSOTestIdPOpts(t, false)
	sso := testSSOMiddleware(idp, "test-client")

	now := time.Now()
	token := idp.issueIDToken(t, map[string]any{
		"iss":   idp.server.URL,
		"aud":   []string{"test-client"},
		"email": "alice@example.com",
		"iat":   now,
		"exp":   now.Add(time.Hour),
	})

	email, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token, testNonce)
	if err != nil {
		t.Fatalf("verify against a JWKS entry with no alg field: %v", err)
	}
	if email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", email)
	}
}

func TestVerifyIDTokenAndExtractEmail_AlgNoneRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"iss":%q,"aud":%q,"email":"attacker@evil.com","iat":%d,"exp":%d}`,
		idp.server.URL, "test-client", now.Unix(), now.Add(time.Hour).Unix(),
	)))
	forged := header + "." + payload + "."

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), forged, testNonce); err == nil {
		t.Fatal("a token asserting alg=none must not verify — this is the classic 'none algorithm' JWT bypass")
	}
}

// TestVerifyIDTokenAndExtractEmail_HMACAlgConfusionRejected is the classic
// RS256->HS256 confusion attack: sign with HMAC using the RSA public key's
// own DER bytes as the "secret" — anyone can fetch this from the public
// JWKS endpoint. If the verifier ever treated the RSA key as an HMAC secret,
// this would verify.
func TestVerifyIDTokenAndExtractEmail_HMACAlgConfusionRejected(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	pubDER, err := x509.MarshalPKIXPublicKey(idp.rsaPub)
	if err != nil {
		t.Fatalf("marshal rsa public key: %v", err)
	}

	claims := stdjwt.MapClaims{
		"iss":   idp.server.URL,
		"aud":   []string{"test-client"},
		"email": "attacker@evil.com",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	forged, err := stdjwt.NewWithClaims(stdjwt.SigningMethodHS256, claims).SignedString(pubDER)
	if err != nil {
		t.Fatalf("sign hmac-confusion token: %v", err)
	}

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), forged, testNonce); err == nil {
		t.Fatal("an HS256 token HMAC-signed with the RSA public key's own bytes must not verify against that RSA key")
	}
}

func TestDiscoverJWKSURL_NonHTTPSIssuerRejected(t *testing.T) {
	if _, err := discoverJWKSURL(context.Background(), "http://evil.example"); err == nil {
		t.Fatal("a non-https, non-loopback issuer must be rejected before any network call")
	}
}

func TestDiscoverJWKSURL_NonHTTPSJWKSURIRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jwks_uri":"http://attacker.example/jwks.json"}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if _, err := discoverJWKSURL(context.Background(), server.URL); err == nil {
		t.Fatal("a discovery document pointing jwks_uri at a non-https, non-loopback host must be rejected")
	}
}

func TestDiscoverJWKSURL_NonOKStatusRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if _, err := discoverJWKSURL(context.Background(), server.URL); err == nil {
		t.Fatal("a non-200 discovery response must be rejected")
	}
}

func TestDiscoverJWKSURL_MalformedJSONRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{not valid json`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if _, err := discoverJWKSURL(context.Background(), server.URL); err == nil {
		t.Fatal("a malformed discovery document must be rejected")
	}
}

func TestDiscoverJWKSURL_MissingJWKSURIRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if _, err := discoverJWKSURL(context.Background(), server.URL); err == nil {
		t.Fatal("a discovery document missing jwks_uri must be rejected")
	}
}

// TestDiscoverJWKSURL_RespectsContextTimeout proves the context-timeout
// wiring is real: discoverJWKSURL wraps its own internal oidcDiscoveryTimeout
// around whatever context it's given via context.WithTimeout, which always
// takes the sooner of the two deadlines. A 50ms parent deadline against a
// discovery endpoint that never responds must return quickly, not hang.
func TestDiscoverJWKSURL_RespectsContextTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := discoverJWKSURL(ctx, server.URL)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a discovery endpoint that never responds")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("discoverJWKSURL took %s to return — context/timeout wiring did not actually bound the request", elapsed)
	}
}

// TestJWKS_ConcurrentColdStartDiscoversOnce locks in the singleflight fix:
// without holding jwksMu across the whole cold-init sequence, every
// concurrent cold-start caller independently spins up its own
// jwk.Cache/httprc.Client, permanently leaking the losers' background
// goroutines.
func TestJWKS_ConcurrentColdStartDiscoversOnce(t *testing.T) {
	idp := newSSOTestIdP(t)
	sso := testSSOMiddleware(idp, "test-client")

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = sso.jwks(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: unexpected error: %v", i, err)
		}
	}
	if got := idp.discoveryCalls.Load(); got != 1 {
		t.Errorf("discovery endpoint hit %d times for %d concurrent cold-start callers, want exactly 1 — every extra hit is a leaked jwk.Cache/httprc.Client", got, n)
	}
}

func TestJWKS_FailureCooldownPreventsImmediateRetry(t *testing.T) {
	idp := newSSOTestIdP(t)
	idp.failDiscovery.Store(true)
	sso := testSSOMiddleware(idp, "test-client")

	if _, err := sso.jwks(context.Background()); err == nil {
		t.Fatal("expected an error from a failing discovery endpoint")
	}
	if _, err := sso.jwks(context.Background()); err == nil {
		t.Fatal("expected the cooldown error, not a fresh success")
	}

	if got := idp.discoveryCalls.Load(); got != 1 {
		t.Errorf("discovery endpoint hit %d times across 2 calls within the cooldown window, want exactly 1 — the second call should short-circuit without hitting the network", got)
	}
}

func TestJWKS_RecoversAfterCooldownWindow(t *testing.T) {
	idp := newSSOTestIdP(t)
	idp.failDiscovery.Store(true)
	sso := testSSOMiddleware(idp, "test-client")

	if _, err := sso.jwks(context.Background()); err == nil {
		t.Fatal("expected an error from a failing discovery endpoint")
	}

	idp.failDiscovery.Store(false)
	time.Sleep(jwksInitCooldown + 200*time.Millisecond)

	if _, err := sso.jwks(context.Background()); err != nil {
		t.Fatalf("expected recovery after the cooldown window elapsed and the IdP started succeeding, got: %v", err)
	}
}

func newCallbackTestSSOMiddleware() *SSOMiddleware {
	return &SSOMiddleware{
		config: SSOConfig{
			OIDCIssuer:   "https://issuer.example",
			OIDCClientID: "test-client",
			CallbackURL:  "https://tombstone.example/auth/callback",
		},
		logger: zap.NewNop(),
	}
}

func redirectParam(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()
	loc, err := url.Parse(rec.Result().Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	v := loc.Query().Get(name)
	if v == "" {
		t.Fatalf("redirect location missing %q param: %s", name, loc)
	}
	return v
}

// TestLoginHandler_SetsSessionCookieAndAuthorizeParams is the direct
// regression proof for the CSRF gap: state used to be generated and put
// into the redirect with nothing to check it against later. This asserts
// LoginHandler actually persists state (plus nonce and a PKCE challenge)
// somewhere CallbackHandler can read it back from.
func TestLoginHandler_SetsSessionCookieAndAuthorizeParams(t *testing.T) {
	sso := newCallbackTestSSOMiddleware()

	rec := httptest.NewRecorder()
	sso.LoginHandler(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want exactly 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != oauthSessionCookieName {
		t.Errorf("cookie name = %q, want %q", cookie.Name, oauthSessionCookieName)
	}
	if !cookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if !cookie.Secure {
		t.Error("session cookie must be Secure for an https CallbackURL")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax (Strict would drop the cookie on the IdP's cross-site redirect back)", cookie.SameSite)
	}

	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		t.Fatalf("decode cookie value: %v", err)
	}
	var sess oauthSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatalf("unmarshal cookie value: %v", err)
	}

	if got := redirectParam(t, rec, "state"); got != sess.State {
		t.Errorf("redirect state = %q, cookie state = %q, want match", got, sess.State)
	}
	if got := redirectParam(t, rec, "nonce"); got != sess.Nonce {
		t.Errorf("redirect nonce = %q, cookie nonce = %q, want match", got, sess.Nonce)
	}
	if got, want := redirectParam(t, rec, "code_challenge_method"), "S256"; got != want {
		t.Errorf("code_challenge_method = %q, want %q", got, want)
	}
	if got, want := redirectParam(t, rec, "code_challenge"), pkceChallengeS256(sess.Verifier); got != want {
		t.Errorf("code_challenge = %q, want sha256(cookie verifier) = %q", got, want)
	}
}

func TestCallbackHandler_MissingSessionCookieRejected(t *testing.T) {
	sso := newCallbackTestSSOMiddleware()

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=whatever", nil)
	rec := httptest.NewRecorder()
	sso.CallbackHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no session cookie must be rejected before ever contacting the IdP); body: %s", rec.Code, rec.Body.String())
	}
}

// TestCallbackHandler_StateMismatchRejected is the direct regression proof:
// with the pre-fix code, this exact request (a real session cookie present,
// but an attacker-controlled state param) would have sailed past with no
// check at all and gone straight to token exchange.
func TestCallbackHandler_StateMismatchRejected(t *testing.T) {
	sso := newCallbackTestSSOMiddleware()

	loginRec := httptest.NewRecorder()
	sso.LoginHandler(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	cookies := loginRec.Result().Cookies()

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=attacker-controlled-state", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	sso.CallbackHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (state mismatch must be rejected before ever contacting the IdP); body: %s", rec.Code, rec.Body.String())
	}
}

// TestCallbackHandler_MatchingStateProceedsPastCSRFGate proves the fix does
// not break the legitimate path: a real session cookie plus its own
// matching state must get past the CSRF check (the only failure left is
// token exchange against a nonexistent IdP — 502, not 400/403).
func TestCallbackHandler_MatchingStateProceedsPastCSRFGate(t *testing.T) {
	sso := newCallbackTestSSOMiddleware()

	loginRec := httptest.NewRecorder()
	sso.LoginHandler(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	cookies := loginRec.Result().Cookies()
	state := redirectParam(t, loginRec, "state")

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state="+state, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	sso.CallbackHandler(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (matching state should get past the CSRF gate to token exchange, which then fails against a fake issuer); body: %s", rec.Code, rec.Body.String())
	}
}

func TestCallbackHandler_ClearsSessionCookie(t *testing.T) {
	sso := newCallbackTestSSOMiddleware()

	loginRec := httptest.NewRecorder()
	sso.LoginHandler(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	cookies := loginRec.Result().Cookies()
	state := redirectParam(t, loginRec, "state")

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state="+state, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	sso.CallbackHandler(rec, req)

	respCookies := rec.Result().Cookies()
	if len(respCookies) != 1 {
		t.Fatalf("got %d Set-Cookie headers on the callback response, want exactly 1 (clearing the session cookie)", len(respCookies))
	}
	if respCookies[0].MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want negative (cookie must be cleared, not left to expire on its original 10-minute schedule)", respCookies[0].MaxAge)
	}
}

func TestExchangeCode_SendsCodeVerifier(t *testing.T) {
	var gotVerifier, gotCode string
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotVerifier = r.PostForm.Get("code_verifier")
		gotCode = r.PostForm.Get("code")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id_token":"stub-id-token","access_token":"stub","token_type":"Bearer","expires_in":3600}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	sso := &SSOMiddleware{
		config: SSOConfig{OIDCIssuer: server.URL, CallbackURL: "https://app.example/callback", OIDCClientID: "test-client"},
		logger: zap.NewNop(),
	}

	tr, err := sso.exchangeCode("test-code", "test-verifier")
	if err != nil {
		t.Fatalf("exchangeCode: %v", err)
	}
	if tr.IDToken != "stub-id-token" {
		t.Errorf("id_token = %q, want stub-id-token", tr.IDToken)
	}
	if gotCode != "test-code" {
		t.Errorf("token endpoint received code = %q, want test-code", gotCode)
	}
	if gotVerifier != "test-verifier" {
		t.Errorf("token endpoint received code_verifier = %q, want test-verifier — PKCE verifier is not being sent", gotVerifier)
	}
}

func TestIsSecureCallbackURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://app.example/callback", true},
		{"http://app.example/callback", false},
		{"http://127.0.0.1:8081/callback", false},
		{"not a url", false},
	}
	for _, tc := range cases {
		if got := isSecureCallbackURL(tc.url); got != tc.want {
			t.Errorf("isSecureCallbackURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}
