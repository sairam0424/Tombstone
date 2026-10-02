package v1_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	v1 "github.com/tombstone/marketplace/internal/api/v1"
	"github.com/tombstone/marketplace/internal/clientip"
	"github.com/tombstone/marketplace/internal/registry"
	"github.com/tombstone/marketplace/internal/webhook"
)

func signDatadogBody(t *testing.T, secret string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// TestHandleDatadogInbound_KillSwitchesOnBlockedP1 is the end-to-end
// regression test for a Datadog alert -> real blast-radius check -> real
// auto-kill-switch. Before this fix, this entire chain was silently dead
// for three independent reasons this test would have caught: (1)
// fetchFlagsByService sent no Authorization header against flag-api's
// authenticated /api/v1/flags, so every call 401'd; (2) it also decoded
// the response into a bare array when flag-api actually returns
// {"flags": [...], "total": N}, an unconditional decode error; (3)
// fetchBlastRadius called a /{flag_key} path segment
// blast.HandleBlastRadius never registered (the real route is
// query-parameter based) and decoded into a flat struct that didn't match
// the real nested {"result": {"risk_score": ...}} response shape, so
// Status was always "". Any one of these alone would keep KillSwitched
// empty forever, even for a genuinely BLOCKED, P1 flag.
func TestHandleDatadogInbound_KillSwitchesOnBlockedP1(t *testing.T) {
	const flagKey = "checkout-v2"
	const owner = "payments-team"

	var gotFlagAPIAuth string
	fakeFlagAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/flags":
			gotFlagAPIAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"flags": []map[string]string{
					{"key": flagKey, "owner_id": owner},
					{"key": "unrelated-flag", "owner_id": "billing-team"},
				},
				"total": 2,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/flags/"+flagKey+"/kill-switch":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected flag-api request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeFlagAPI.Close()

	fakeEvaluator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/blast-radius" {
			t.Errorf("evaluator request used a path segment, not query params: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("flag_key"); got != flagKey {
			t.Errorf("flag_key query param = %q, want %q", got, flagKey)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"flag_key":        flagKey,
			"environment":     "production",
			"new_rollout_pct": 100,
			"result": map[string]any{
				"risk_score":             "BLOCKED",
				"traffic_pct_affected":   100,
				"dependent_flags_count":  0,
				"affected_services":      []string{},
				"historical_error_rate":  0.08,
				"confidence":             "HIGH",
				"justification_required": "Risk score BLOCKED: ...",
			},
		})
	}))
	defer fakeEvaluator.Close()

	t.Setenv("FLAG_API_URL", fakeFlagAPI.URL)
	t.Setenv("EVALUATOR_URL", fakeEvaluator.URL)
	t.Setenv("DD_WEBHOOK_SECRET", "test-secret")

	h := newTestHandler(fakeFlagAPI.URL)

	body, err := json.Marshal(map[string]any{
		"alert_id": "alert-1",
		"title":    "Payments error rate spike",
		"severity": "P1",
		"tags":     []string{"service:" + owner},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/inbound/datadog", bytes.NewReader(body))
	req.Header.Set("DD-Signature", signDatadogBody(t, "test-secret", body))

	w := httptest.NewRecorder()
	h.HandleDatadogInbound(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDatadogInbound() status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if gotFlagAPIAuth == "" {
		t.Error("fetchFlagsByService sent no Authorization header -- flag-api's /api/v1/flags requires auth")
	}

	var summary struct {
		FlagsEvaluated int      `json:"flags_evaluated"`
		Triggered      []any    `json:"triggered"`
		KillSwitched   []string `json:"kill_switched"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary: %v; body: %s", err, w.Body.String())
	}
	if summary.FlagsEvaluated != 1 {
		t.Errorf("FlagsEvaluated = %d, want 1 (owner_id filtering should have excluded unrelated-flag)", summary.FlagsEvaluated)
	}
	if len(summary.KillSwitched) != 1 || summary.KillSwitched[0] != flagKey {
		t.Errorf("KillSwitched = %v, want [%q] -- the blast-radius-driven auto-kill-switch never fired", summary.KillSwitched, flagKey)
	}
}

// randomHex returns a fresh value generated at run time, so no secret-looking
// literal is ever committed.
func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generate random value: %v", err)
	}
	return hex.EncodeToString(b)
}

// failedSignatureClientIP sends a Datadog alert signed with the wrong secret
// through wrap(handler) and returns the client_ip the handler logged for the
// rejection. wrap lets a test put middleware in front of the handler.
func failedSignatureClientIP(t *testing.T, remoteAddr string, headers map[string]string, wrap func(http.Handler) http.Handler) string {
	t.Helper()
	t.Setenv("DD_WEBHOOK_SECRET", randomHex(t))

	core, logs := observer.New(zap.WarnLevel)
	logger := zap.New(core)
	reg := registry.NewRegistry(nil, logger)
	h := v1.NewHandler(reg, webhook.NewDispatcher(reg, logger), logger, "")

	body := []byte(`{"alert_id":"alert-1","title":"spike","severity":"P1","tags":["service:payments"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/inbound/datadog", bytes.NewReader(body))
	req.RemoteAddr = remoteAddr
	req.Header.Set("DD-Signature", signDatadogBody(t, randomHex(t), body))
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	w := httptest.NewRecorder()

	wrap(http.HandlerFunc(h.HandleDatadogInbound)).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a bad signature", w.Code)
	}
	entries := logs.FilterMessage("datadog inbound: signature verification failed").All()
	if len(entries) != 1 {
		t.Fatalf("logged %d signature failures, want exactly 1", len(entries))
	}
	clientIP, ok := entries[0].ContextMap()["client_ip"].(string)
	if !ok {
		t.Fatalf("signature failure logged no client_ip field: %v", entries[0].ContextMap())
	}
	return clientIP
}

func noMiddleware(next http.Handler) http.Handler { return next }

// TestHandleDatadogInbound_FailedSignatureAttributesTheConnectionPeer pins that
// the attribution logged for a rejected webhook is the connection peer, never a
// forwarded header: anyone can send a request with a bad signature, so the
// header is exactly the thing a prober would forge to point at someone else.
func TestHandleDatadogInbound_FailedSignatureAttributesTheConnectionPeer(t *testing.T) {
	tests := []struct {
		name    string
		remote  string
		headers map[string]string
		want    string
	}{
		{"no forwarding headers", "203.0.113.9:4000", nil, "203.0.113.9"},
		{"forged X-Forwarded-For", "203.0.113.9:4000", map[string]string{"X-Forwarded-For": "6.6.6.6"}, "203.0.113.9"},
		{"forged X-Real-IP", "203.0.113.9:4000", map[string]string{"X-Real-IP": "6.6.6.6"}, "203.0.113.9"},
		{"forged True-Client-IP", "203.0.113.9:4000", map[string]string{"True-Client-IP": "6.6.6.6"}, "203.0.113.9"},
		{"IPv6 peer", "[2001:db8::1]:4000", nil, "2001:db8::1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := failedSignatureClientIP(t, tc.remote, tc.headers, noMiddleware); got != tc.want {
				t.Errorf("client_ip = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHandleDatadogInbound_FailedSignatureBehindATrustedProxy covers the
// deployed shape: the proxy's X-Forwarded-For entry is logged, not the proxy's
// own address and not any entry the caller prepended; and a direct caller
// naming a trusted address in the header gains nothing.
func TestHandleDatadogInbound_FailedSignatureBehindATrustedProxy(t *testing.T) {
	trust, err := clientip.ParseTrust("10.0.0.0/8")
	if err != nil {
		t.Fatalf("ParseTrust: %v", err)
	}
	tests := []struct {
		name      string
		remote    string
		forwarded string
		want      string
	}{
		{"client seen by the proxy", "10.1.2.3:4000", "198.51.100.7", "198.51.100.7"},
		{"caller prepended entries", "10.1.2.3:4000", "6.6.6.6, 198.51.100.7", "198.51.100.7"},
		{"direct caller naming a trusted address", "203.0.113.9:4000", "10.1.2.3", "203.0.113.9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := failedSignatureClientIP(t, tc.remote, map[string]string{"X-Forwarded-For": tc.forwarded}, trust.Middleware)

			if got != tc.want {
				t.Errorf("client_ip = %q, want %q", got, tc.want)
			}
		})
	}
}
