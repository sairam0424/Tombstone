package middleware

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// testHMACKey generates a fresh random HMAC key at runtime — never a
// literal fixture — for signing test JWTs.
func testHMACKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	return hex.EncodeToString(b)
}

// mintTestJWT signs a token shaped exactly like sso.go's issueTombstoneJWT
// (sub/iat/exp), letting tests control iat directly — the claim
// tokenPredatesWatermark compares against the watermark row.
func mintTestJWT(t *testing.T, key, sub string, iat time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": sub,
		"iat": iat.Unix(),
		"exp": iat.Add(24 * time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(key))
	if err != nil {
		t.Fatalf("sign test jwt: %v", err)
	}
	return signed
}

// TestValidateJWT_TokenIssuedBeforeWatermarkRejected is the direct
// regression proof for SEC-5's revoke-after-watermark fix: a token whose
// signature and exp are both still perfectly valid must nonetheless be
// rejected once the subject has a forced-logout watermark newer than the
// token's own iat.
func TestValidateJWT_TokenIssuedBeforeWatermarkRejected(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	// iat must stay within jwt.Parse's own real-clock exp window (iat+24h),
	// so the rejection this test proves comes from the watermark check, not
	// from jwt.Parse's independent exp validation rejecting an
	// already-expired token for an unrelated reason.
	iat := time.Now().Add(-2 * time.Hour)
	watermark := iat.Add(time.Hour) // set AFTER this token was issued, still in the past

	mock.ExpectQuery("SELECT valid_after FROM user_token_watermarks").
		WithArgs("alice@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"valid_after"}).AddRow(watermark))

	key := testHMACKey(t)
	auth := NewAuthMiddleware(db, key, nil, zap.NewNop())
	token := mintTestJWT(t, key, "alice@example.com", iat)

	if _, ok := auth.validateJWT(context.Background(), token); ok {
		t.Fatal("a token issued before the subject's forced-logout watermark must be rejected")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v — the watermark query was never reached", err)
	}
}

// TestValidateJWT_TokenIssuedAfterWatermarkAccepted proves the fix doesn't
// break the legitimate case: a FRESH token (issued after a past forced
// logout) must still authenticate normally.
func TestValidateJWT_TokenIssuedAfterWatermarkAccepted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	watermark := time.Now().Add(-2 * time.Hour)
	iat := watermark.Add(time.Hour) // re-authenticated AFTER the forced logout, still in the past

	mock.ExpectQuery("SELECT valid_after FROM user_token_watermarks").
		WithArgs("alice@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"valid_after"}).AddRow(watermark))

	key := testHMACKey(t)
	auth := NewAuthMiddleware(db, key, nil, zap.NewNop())
	token := mintTestJWT(t, key, "alice@example.com", iat)

	actor, ok := auth.validateJWT(context.Background(), token)
	if !ok {
		t.Fatal("a token issued after the subject's forced-logout watermark must be accepted")
	}
	if actor != "alice@example.com" {
		t.Errorf("actor = %q, want alice@example.com", actor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v — the watermark query was never reached", err)
	}
}

// TestValidateJWT_NoWatermarkRowAccepted covers a subject who has never
// been force-logged-out at all — the common case for every login ever.
func TestValidateJWT_NoWatermarkRowAccepted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT valid_after FROM user_token_watermarks").
		WithArgs("alice@example.com").
		WillReturnError(sql.ErrNoRows)

	key := testHMACKey(t)
	auth := NewAuthMiddleware(db, key, nil, zap.NewNop())
	token := mintTestJWT(t, key, "alice@example.com", time.Now())

	if _, ok := auth.validateJWT(context.Background(), token); !ok {
		t.Fatal("a subject with no watermark row must be accepted — absence means never revoked")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestValidateJWT_WatermarkLookupErrorFailsOpen is the direct regression
// proof that a transient DB error on this NEW lookup does not lock out an
// otherwise-valid, already-signature-verified token — this is a
// defense-in-depth check layered on top of authentication, not the primary
// authentication decision, so it must fail open like rate limiting does.
func TestValidateJWT_WatermarkLookupErrorFailsOpen(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT valid_after FROM user_token_watermarks").
		WithArgs("alice@example.com").
		WillReturnError(errors.New("connection reset by peer"))

	core, logs := observer.New(zap.WarnLevel)
	key := testHMACKey(t)
	auth := NewAuthMiddleware(db, key, nil, zap.New(core))
	token := mintTestJWT(t, key, "alice@example.com", time.Now())

	if _, ok := auth.validateJWT(context.Background(), token); !ok {
		t.Fatal("a watermark lookup error must fail OPEN (allow), not lock out an already-verified token")
	}
	if logs.Len() != 1 {
		t.Fatalf("got %d warning log entries, want exactly 1 — a real lookup error must be visible to an operator, not silently swallowed", logs.Len())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestValidateJWT_InvalidSignatureStillRejected proves the watermark check
// doesn't run at all for (and can't accidentally rescue) a token that
// fails basic signature verification — the sqlmock instance with no
// .ExpectQuery set would fail this test if the watermark lookup were
// reached (sqlmock errors on an unexpected query by default).
func TestValidateJWT_InvalidSignatureStillRejected(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	key := testHMACKey(t)
	auth := NewAuthMiddleware(db, key, nil, zap.NewNop())
	forged := mintTestJWT(t, key, "alice@example.com", time.Now()) + "tampered"

	if _, ok := auth.validateJWT(context.Background(), forged); ok {
		t.Fatal("a token with an invalid signature must be rejected before any watermark lookup")
	}
}
