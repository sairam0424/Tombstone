package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	server     *httptest.Server
	signingKey jwk.Key
	publicSet  jwk.Set
}

func newSSOTestIdP(t *testing.T) *ssoTestIdP {
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
	if err := signingKey.Set(jwk.AlgorithmKey, jwa.RS256()); err != nil {
		t.Fatalf("set alg: %v", err)
	}

	privSet := jwk.NewSet()
	if err := privSet.AddKey(signingKey); err != nil {
		t.Fatalf("add key to set: %v", err)
	}
	publicSet, err := jwk.PublicSetOf(privSet)
	if err != nil {
		t.Fatalf("derive public jwks: %v", err)
	}

	idp := &ssoTestIdP{signingKey: signingKey, publicSet: publicSet}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
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

// issueIDToken signs an ID token with the IdP's own key — the key the
// discovery/JWKS endpoints above actually publish.
func (idp *ssoTestIdP) issueIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
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

	email, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token)
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

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), forged); err == nil {
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

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), string(signed)); err == nil {
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

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token); err == nil {
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

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token); err == nil {
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

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token); err == nil {
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

	if _, err := sso.verifyIDTokenAndExtractEmail(context.Background(), token); err == nil {
		t.Fatal("a validly-signed token with no email claim must not resolve to an identity")
	}
}
