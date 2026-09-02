package secrets

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

// genTestECDSAPEM generates a fresh ECDSA P-256 key at runtime and returns
// it PKCS#8/PEM-encoded — test key material is never a literal fixture.
func genTestECDSAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestNewRekorSignerRequiresKey(t *testing.T) {
	if _, err := NewRekorSigner(""); err != ErrNoRekorSigningKey {
		t.Fatalf("err = %v, want ErrNoRekorSigningKey", err)
	}
}

func TestNewRekorSignerRejectsInvalidPEM(t *testing.T) {
	if _, err := NewRekorSigner("not pem at all"); err == nil {
		t.Fatal("expected an error for non-PEM input")
	}
}

// TestNewRekorSignerRejectsNonP256Curve is the direct regression proof: a
// key on any OTHER ECDSA curve (weaker, like P-224, or just non-standard for
// this control) would parse and sign/verify fine on its own terms — the bug
// this pins down is that nothing checked the curve at all before this fix,
// so a misconfigured REKOR_SIGNING_KEY could silently downgrade the
// control's strength with no error and no warning anywhere.
func TestNewRekorSignerRejectsNonP256Curve(t *testing.T) {
	for _, curve := range []elliptic.Curve{elliptic.P384(), elliptic.P521()} {
		t.Run(curve.Params().Name, func(t *testing.T) {
			key, err := ecdsa.GenerateKey(curve, rand.Reader)
			if err != nil {
				t.Fatalf("generate key: %v", err)
			}
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatalf("marshal pkcs8: %v", err)
			}
			pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

			if _, err := NewRekorSigner(pemKey); err == nil {
				t.Fatalf("expected an error for a %s key — only P-256 must be accepted", curve.Params().Name)
			}
		})
	}
}

func TestNewRekorSignerRejectsNonECDSAKey(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	rsaPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	if _, err := NewRekorSigner(rsaPEM); err == nil {
		t.Fatal("expected an error for an RSA (non-ECDSA) key — Sign()/PublicKeyPEM() assume ecdsa.PrivateKey")
	}
}

func TestNewRekorSignerAcceptsEscapedNewlines(t *testing.T) {
	realPEM := genTestECDSAPEM(t)
	escaped := strings.ReplaceAll(realPEM, "\n", `\n`)

	if _, err := NewRekorSigner(escaped); err != nil {
		t.Fatalf("a PEM with literal \\n instead of real newlines must still parse: %v", err)
	}
}

// TestRekorSignerSignVerifyRoundTrip is the direct regression proof for
// AUD-1b: the whole point of this type is producing a signature Rekor's
// OWN hashedrekord verifier — ecdsa.VerifyASN1 against the raw digest
// bytes, not the pre-hash content — will accept. This test signs with
// exactly the same primitives that verifier uses.
func TestRekorSignerSignVerifyRoundTrip(t *testing.T) {
	signer, err := NewRekorSigner(genTestECDSAPEM(t))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	digest := []byte("0123456789abcdef0123456789abcdef") // stand-in 32-byte-ish digest
	sig, err := signer.Sign(digest)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	pubPEM, err := signer.PublicKeyPEM()
	if err != nil {
		t.Fatalf("public key pem: %v", err)
	}
	block, _ := pem.Decode(pubPEM)
	if block == nil {
		t.Fatal("PublicKeyPEM did not return valid PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("public key type = %T, want *ecdsa.PublicKey", parsed)
	}

	if !ecdsa.VerifyASN1(pub, digest, sig) {
		t.Fatal("signature does not verify against the digest and public key it was produced from")
	}

	if ecdsa.VerifyASN1(pub, []byte("a different digest, same length!"), sig) {
		t.Fatal("signature verified against a DIFFERENT digest — it isn't actually binding the signature to the content")
	}
}

func TestRekorSignerPublicKeyPEMIsReusable(t *testing.T) {
	signer, err := NewRekorSigner(genTestECDSAPEM(t))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	first, err := signer.PublicKeyPEM()
	if err != nil {
		t.Fatalf("public key pem (1st call): %v", err)
	}
	second, err := signer.PublicKeyPEM()
	if err != nil {
		t.Fatalf("public key pem (2nd call): %v", err)
	}
	if string(first) != string(second) {
		t.Error("PublicKeyPEM() returned different output across two calls on the same signer")
	}
}
