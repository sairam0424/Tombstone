package middleware

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/db"
)

// TestClassifyMFAEvent needs no DB or IdP — it pins the deliberate
// distinction between "amr absent" (no evidence either way — must not
// fabricate a claim) and "amr present but asserts no second factor" (real
// evidence that MFA was not used for this specific login).
func TestClassifyMFAEvent(t *testing.T) {
	cases := []struct {
		name           string
		amr            []string
		wantEventType  string
		wantApplicable bool
	}{
		{"absent amr — no evidence, log nothing", nil, "", false},
		{"empty amr slice — same as absent", []string{}, "", false},
		{"amr asserts a second factor (otp)", []string{"pwd", "otp"}, "mfa_verified", true},
		{"amr asserts a second factor via explicit mfa marker", []string{"mfa"}, "mfa_verified", true},
		{"amr asserts hardware key", []string{"pwd", "hwk"}, "mfa_verified", true},
		{"amr is case-insensitive", []string{"PWD", "OTP"}, "mfa_verified", true},
		{"amr present, only single factor — real evidence MFA was skipped", []string{"pwd"}, "mfa_bypassed", true},
		{"amr present with an unrecognized single method", []string{"pwd", "rba"}, "mfa_bypassed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eventType, applicable := classifyMFAEvent(tc.amr)
			if applicable != tc.wantApplicable {
				t.Fatalf("applicable = %v, want %v", applicable, tc.wantApplicable)
			}
			if eventType != tc.wantEventType {
				t.Errorf("eventType = %q, want %q", eventType, tc.wantEventType)
			}
		})
	}
}

// TestExtractAMR proves extraction against a REAL signed token (via the
// same fake-IdP infrastructure sso_test.go uses), not a hand-built claims
// map — the exact JSON shape a JWT library round-trips an array through
// matters here (amr is []interface{} after JSON decode, not []string).
func TestExtractAMR(t *testing.T) {
	idp := newSSOTestIdP(t)

	t.Run("amr present as a string array", func(t *testing.T) {
		now := time.Now()
		token := idp.issueIDToken(t, map[string]any{
			"iss": idp.server.URL, "aud": []string{"test-client"}, "email": "alice@example.com",
			"iat": now, "exp": now.Add(time.Hour), "amr": []string{"pwd", "otp"},
		})
		_, amr, err := (&SSOMiddleware{config: SSOConfig{OIDCIssuer: idp.server.URL, OIDCClientID: "test-client"}, logger: zap.NewNop()}).
			verifyIDTokenAndExtractEmail(context.Background(), token, testNonce)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if len(amr) != 2 || amr[0] != "pwd" || amr[1] != "otp" {
			t.Errorf("amr = %v, want [pwd otp]", amr)
		}
	})

	t.Run("amr absent", func(t *testing.T) {
		now := time.Now()
		token := idp.issueIDToken(t, map[string]any{
			"iss": idp.server.URL, "aud": []string{"test-client"}, "email": "alice@example.com",
			"iat": now, "exp": now.Add(time.Hour),
		})
		_, amr, err := (&SSOMiddleware{config: SSOConfig{OIDCIssuer: idp.server.URL, OIDCClientID: "test-client"}, logger: zap.NewNop()}).
			verifyIDTokenAndExtractEmail(context.Background(), token, testNonce)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if amr != nil {
			t.Errorf("amr = %v, want nil", amr)
		}
	})
}

// TestLogMFAEvent is the executable gate for SEC-5's MFA-logging fix — it
// runs against a real Postgres in the flag-api-migrations CI job and skips
// locally (same convention as every DB-backed test elsewhere in this repo).
// user_mfa_log has existed since migration 003 ("MFA event log for SOC 2
// CC6 evidence") with zero Go code ever writing to it before this fix.
func TestLogMFAEvent(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed MFA log test")
	}

	database, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := db.Migrate(ctx, database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	countEventsFor := func(t *testing.T, userID, eventType string) int {
		t.Helper()
		var n int
		if err := database.QueryRowContext(ctx,
			`SELECT count(*) FROM user_mfa_log WHERE user_id = $1 AND event_type = $2`, userID, eventType,
		).Scan(&n); err != nil {
			t.Fatalf("count user_mfa_log rows: %v", err)
		}
		return n
	}

	t.Run("amr with a second factor writes mfa_verified", func(t *testing.T) {
		s := &SSOMiddleware{logger: zap.NewNop(), db: database}
		s.logMFAEvent(ctx, "mfa-verified@example.com", []string{"pwd", "otp"})

		if got := countEventsFor(t, "mfa-verified@example.com", "mfa_verified"); got != 1 {
			t.Errorf("mfa_verified rows = %d, want 1", got)
		}
	})

	t.Run("amr with only a single factor writes mfa_bypassed", func(t *testing.T) {
		s := &SSOMiddleware{logger: zap.NewNop(), db: database}
		s.logMFAEvent(ctx, "mfa-bypassed@example.com", []string{"pwd"})

		if got := countEventsFor(t, "mfa-bypassed@example.com", "mfa_bypassed"); got != 1 {
			t.Errorf("mfa_bypassed rows = %d, want 1", got)
		}
	})

	t.Run("absent amr writes nothing", func(t *testing.T) {
		s := &SSOMiddleware{logger: zap.NewNop(), db: database}
		s.logMFAEvent(ctx, "mfa-no-evidence@example.com", nil)

		var total int
		if err := database.QueryRowContext(ctx,
			`SELECT count(*) FROM user_mfa_log WHERE user_id = $1`, "mfa-no-evidence@example.com",
		).Scan(&total); err != nil {
			t.Fatalf("count user_mfa_log rows: %v", err)
		}
		if total != 0 {
			t.Errorf("rows for a login with no amr evidence = %d, want 0 — must not fabricate a claim", total)
		}
	})

	t.Run("a nil db does not panic — it just skips the write", func(t *testing.T) {
		s := &SSOMiddleware{logger: zap.NewNop(), db: nil}
		s.logMFAEvent(ctx, "mfa-no-db@example.com", []string{"pwd", "otp"})
	})
}
