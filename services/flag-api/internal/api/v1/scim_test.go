package v1

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// SEC-5: SCIMAuthMiddleware used to allow every request through
// unauthenticated whenever SCIM_TOKEN was unset ("dev mode"), and SCIM_TOKEN
// is documented nowhere any of this repo's deployment configs set it — so
// every currently-documented deployment ran with SCIM wide open. These tests
// pin the fixed, fail-closed behavior.

func scimNextHandlerReached(t *testing.T) (http.Handler, *bool) {
	t.Helper()
	reached := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	return h, &reached
}

func TestSCIMAuthMiddleware_NoTokenConfiguredFailsClosed(t *testing.T) {
	next, reached := scimNextHandlerReached(t)
	handler := SCIMAuthMiddleware("")(next)

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer anything") // must not matter — nothing is configured to check against
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (SCIM unconfigured must be unavailable, not open); body: %s",
			rec.Code, rec.Body.String())
	}
	if *reached {
		t.Fatal("handler ran despite SCIM_TOKEN being unset — this is the exact fail-open bug being fixed")
	}
}

func TestSCIMAuthMiddleware_MissingBearerHeaderDenied(t *testing.T) {
	next, reached := scimNextHandlerReached(t)
	handler := SCIMAuthMiddleware("configured-token")(next)

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", rec.Code, rec.Body.String())
	}
	if *reached {
		t.Fatal("handler ran despite no Authorization header")
	}
}

func TestSCIMAuthMiddleware_WrongTokenDenied(t *testing.T) {
	next, reached := scimNextHandlerReached(t)
	handler := SCIMAuthMiddleware("configured-token")(next)

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", rec.Code, rec.Body.String())
	}
	if *reached {
		t.Fatal("handler ran despite a wrong token")
	}
}

func TestSCIMAuthMiddleware_CorrectTokenAllowed(t *testing.T) {
	next, reached := scimNextHandlerReached(t)
	handler := SCIMAuthMiddleware("configured-token")(next)

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer configured-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !*reached {
		t.Fatal("handler did not run despite a correct token")
	}
}
