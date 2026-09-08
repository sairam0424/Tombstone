package v1_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
