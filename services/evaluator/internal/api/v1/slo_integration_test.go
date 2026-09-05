package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/tombstone/evaluator/internal/circuit"
	"github.com/tombstone/evaluator/internal/telemetry"
)

// TestHandleFlagSLO_ReadsRealAggregatorData is the end-to-end regression
// test for EVAL-3's telemetry-persistence fix: this handler's own
// aggregateTelemetry/countCircuitTrips/buildHistory doc comments have
// always named the exact Redis key shapes they expect
// (telemetry:{flag}:{env}:hour:{unix_hour}, circuit:{flag}:{env}:
// trips:{unix_hour}, circuit:{flag}:{env}:state:{unix_hour}) with an
// explicit admission that "the aggregator does not yet write these keys" --
// so this endpoint has been silently returning all-zero history for real
// production data since it was built. This test wires a REAL
// telemetry.Aggregator (not a stub) sharing the same Redis as the
// handler, drives a real trip through it, and asserts the SLO response
// reflects that real data -- closing the exact gap: a handler-only test
// stubbing Redis values directly (as every other test in this file does)
// would stay green even if the aggregator's writer were removed entirely.
func TestHandleFlagSLO_ReadsRealAggregatorData(t *testing.T) {
	h, rdb := newTestHandler(t)
	agg := telemetry.NewAggregator(h.breaker, rdb, zap.NewNop())
	agg.OnRolloutChange = func(string, string, int, float64, circuit.RolloutPhase) bool { return true }

	ctx := context.Background()
	const flagKey, env = "checkout-v2", "production"

	// A real trip: 100 requests, 10 errors (10% -- above the 5% default
	// threshold), which should both accumulate into this hour's telemetry
	// bucket AND increment this hour's trip counter.
	for i := 0; i < 100; i++ {
		agg.Record(telemetry.TelemetryEvent{FlagKey: flagKey, Environment: env, IsError: i < 10})
	}
	agg.Flush(ctx)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags/"+flagKey+"/slo?window=1d&environment="+env, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("key", flagKey)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleFlagSLO(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp SLOResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.EvaluationCount != 100 {
		t.Errorf("EvaluationCount = %d, want 100 -- telemetry bucket was not persisted/read correctly", resp.EvaluationCount)
	}
	if resp.ErrorRate != 0.1 {
		t.Errorf("ErrorRate = %v, want 0.1", resp.ErrorRate)
	}
	if resp.CircuitTrips != 1 {
		t.Errorf("CircuitTrips = %d, want 1 -- trip counter was not persisted/read correctly", resp.CircuitTrips)
	}
	if resp.CircuitState != string(circuit.StateOpen) {
		t.Errorf("CircuitState = %q, want %q", resp.CircuitState, circuit.StateOpen)
	}
	if len(resp.History) == 0 {
		t.Fatal("History is empty, want at least one hourly point")
	}
	last := resp.History[len(resp.History)-1]
	if last.ErrorRate != 0.1 {
		t.Errorf("last history point ErrorRate = %v, want 0.1", last.ErrorRate)
	}
	if last.CircuitState != string(circuit.StateOpen) {
		t.Errorf("last history point CircuitState = %q, want %q -- the per-hour state snapshot was not persisted/read correctly", last.CircuitState, circuit.StateOpen)
	}
}
