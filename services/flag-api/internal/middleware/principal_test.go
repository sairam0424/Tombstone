package middleware

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/secrets"
)

// A service token is told apart from a human by what Authenticate records, not
// by the shape of the actor string. These tests run the real Authenticate and
// read the result back through IsServiceTokenCaller, so the helper handlers
// rely on is pinned to the code that produces the signal.

// authenticatedRequestContext runs one request through Authenticate and returns
// the context the next handler saw.
func authenticatedRequestContext(t *testing.T, auth *AuthMiddleware, bearer string) context.Context {
	t.Helper()
	var seen context.Context
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Context()
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/break-glass/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	auth.Authenticate(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || seen == nil {
		t.Fatalf("Authenticate did not pass the request through: status %d, body %s", rec.Code, rec.Body.String())
	}
	return seen
}

func TestIsServiceTokenCaller_ServiceTokenIsRecognised(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	hasher, err := secrets.NewTokenHasher("principal-test-pepper")
	if err != nil {
		t.Fatalf("token hasher: %v", err)
	}
	presented := "TEST-SENTINEL-" + "NOT-A-REAL-TOKEN-1234"
	mock.ExpectQuery("SELECT name, role, project_id FROM service_tokens").
		WithArgs(hasher.Hash(presented)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "role", "project_id"}).
			AddRow("ci-deploy", "ADMIN", "11111111-1111-4111-8111-111111111111"))

	auth := NewAuthMiddleware(db, testHMACKey(t), hasher, zap.NewNop())
	ctx := authenticatedRequestContext(t, auth, presented)

	if got := actorFromContext(ctx); got != "sdk:ci-deploy" {
		t.Fatalf("actor = %q, want sdk:ci-deploy (the harness did not take the service-token path)", got)
	}
	if !IsServiceTokenCaller(ctx) {
		t.Error("a request authenticated with a service token must be recognised as a service-token caller, whatever role the token holds")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestIsServiceTokenCaller_HumanJWTIsNotRecognised(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("SELECT valid_after FROM user_token_watermarks").
		WithArgs("alice@example.com").
		WillReturnError(sql.ErrNoRows)

	key := testHMACKey(t)
	auth := NewAuthMiddleware(db, key, nil, zap.NewNop())
	ctx := authenticatedRequestContext(t, auth, mintTestJWT(t, key, "alice@example.com", time.Now()))

	if got := actorFromContext(ctx); got != "alice@example.com" {
		t.Fatalf("actor = %q, want alice@example.com (the harness did not take the JWT path)", got)
	}
	if IsServiceTokenCaller(ctx) {
		t.Error("a human JWT caller must not be recognised as a service-token caller")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestIsServiceTokenCaller_ContextShapes covers the helper on bare contexts:
// it reports the principal kind Authenticate recorded, independent of the role.
func TestIsServiceTokenCaller_ContextShapes(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"empty context", context.Background(), false},
		{"human actor only", context.WithValue(context.Background(), ContextKeyActor, "alice@example.com"), false},
		{"service token holding VIEWER", context.WithValue(context.Background(), ContextKeyServiceRole, RoleViewer), true},
		{"service token holding ADMIN", context.WithValue(context.Background(), ContextKeyServiceRole, RoleAdmin), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsServiceTokenCaller(tc.ctx); got != tc.want {
				t.Errorf("IsServiceTokenCaller = %v, want %v", got, tc.want)
			}
		})
	}
}
