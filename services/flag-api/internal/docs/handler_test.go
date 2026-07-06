package docs_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tombstone/flag-api/internal/docs"
)

func TestDocsHandlerReturns200(t *testing.T) {
	handler := docs.NewHandler("/api/v1/openapi.json")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/docs", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "redoc") && !strings.Contains(body, "ReDoc") {
		t.Error("response body does not appear to contain Redoc HTML")
	}
}
