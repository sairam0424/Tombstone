package v1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// TestTriggerEvent_DeliversAfterRequestContextIsCanceled is the regression
// test for a real bug found by adversarial review of the flag-api
// notifyMarketplace PR: TriggerEvent passed r.Context() straight into
// Dispatcher.Dispatch, whose delivery goroutines outlive TriggerEvent's own
// return by design (that's the whole point of dispatching asynchronously).
// A real net/http server cancels a request's context the instant its
// handler returns (see http.Request.Context()'s own docs) -- so every
// delivery was being aborted with context.Canceled before a real network
// round trip could complete, and no test had ever caught it because no
// caller had ever exercised this path with a genuine request-scoped
// context (flag-api's new notifyMarketplace is the first).
//
// This test drives that exact sequence directly: install a real
// integration pointing at a fake webhook server, call TriggerEvent with a
// cancelable request context, cancel that context immediately (simulating
// ServeHTTP returning), and confirm the fake webhook server still receives
// the delivery.
func TestTriggerEvent_DeliversAfterRequestContextIsCanceled(t *testing.T) {
	delivered := make(chan struct{}, 1)
	fakeWebhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer fakeWebhook.Close()

	h := newTestHandler("http://localhost:8081")

	installBody, err := json.Marshal(map[string]string{"webhook_url": fakeWebhook.URL})
	if err != nil {
		t.Fatalf("marshal install body: %v", err)
	}
	installReq := newChiRequest(t, http.MethodPost, "/api/v1/marketplace/slack", installBody, map[string]string{"id": "slack"})
	installW := httptest.NewRecorder()
	h.InstallIntegration(installW, installReq)
	if installW.Code != http.StatusOK {
		t.Fatalf("InstallIntegration status = %d, body: %s", installW.Code, installW.Body.String())
	}

	triggerBody, err := json.Marshal(map[string]any{
		"event_type": "flag.enabled",
		"flag_key":   "checkout-v2",
		"actor":      "test",
	})
	if err != nil {
		t.Fatalf("marshal trigger body: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	triggerReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/marketplace/events", bytes.NewReader(triggerBody))

	triggerW := httptest.NewRecorder()
	h.TriggerEvent(triggerW, triggerReq)
	if triggerW.Code != http.StatusAccepted {
		t.Fatalf("TriggerEvent status = %d, body: %s", triggerW.Code, triggerW.Body.String())
	}

	// Simulate net/http canceling the request's context the instant
	// ServeHTTP returns -- exactly what happens after TriggerEvent's own
	// return above in a real server.
	cancel()

	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("fake webhook never received the delivery -- Dispatch's goroutine was aborted by the canceled request context")
	}
}

// newChiRequest builds a request carrying a chi URL param, matching how
// chi's own router injects {id} before InstallIntegration/TriggerEvent run.
func newChiRequest(t *testing.T, method, path string, body []byte, urlParams map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	for k, v := range urlParams {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
