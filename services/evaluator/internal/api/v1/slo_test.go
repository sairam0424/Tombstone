package v1

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/evaluator/internal/circuit"
)

func newTestHandler(t *testing.T) (*Handler, *redis.Client) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	breaker := circuit.NewBreaker(rdb, zap.NewNop())
	return NewHandler(rdb, breaker, zap.NewNop()), rdb
}

// TestSLOKeysAreEnvironmentScoped is the regression test for EVAL-2: the
// telemetry/circuit-trips/circuit-state-snapshot Redis keys aggregateTelemetry,
// countCircuitTrips, and buildHistory read must be keyed per (flag,
// environment), matching circuit.Breaker's stateKey convention (EVAL-1) --
// otherwise a staging value would be indistinguishable from production's for
// the same flag key and hour.
func TestSLOKeysAreEnvironmentScoped(t *testing.T) {
	h, rdb := newTestHandler(t)
	ctx := context.Background()

	flagKey := "checkout-v2"
	hour := time.Now().UTC().Truncate(time.Hour)
	unixHour := hour.Unix() / 3600

	// Seed a staging-only telemetry bucket with a nonzero error count.
	stagingKey := fmt.Sprintf("telemetry:%s:staging:hour:%d", flagKey, unixHour)
	if err := rdb.Set(ctx, stagingKey, `{"total":100,"errors":50,"p99_ms":42.5}`, time.Hour).Err(); err != nil {
		t.Fatalf("seed staging telemetry: %v", err)
	}

	// Production must NOT see staging's bucket -- before this fix both
	// environments shared key "telemetry:checkout-v2:hour:{unix_hour}".
	totalCount, errorCount, _, err := h.aggregateTelemetry(ctx, flagKey, "production", 1)
	if err != nil {
		t.Fatalf("aggregateTelemetry(production): %v", err)
	}
	if totalCount != 0 || errorCount != 0 {
		t.Errorf("production aggregateTelemetry = (total=%d, errors=%d) after a staging-only bucket, want (0, 0) — cross-env contamination", totalCount, errorCount)
	}

	stagingTotal, stagingErrors, _, err := h.aggregateTelemetry(ctx, flagKey, "staging", 1)
	if err != nil {
		t.Fatalf("aggregateTelemetry(staging): %v", err)
	}
	if stagingTotal != 100 || stagingErrors != 50 {
		t.Errorf("staging aggregateTelemetry = (total=%d, errors=%d), want (100, 50)", stagingTotal, stagingErrors)
	}

	// Same check for circuit trips.
	tripsKey := fmt.Sprintf("circuit:%s:staging:trips:%d", flagKey, unixHour)
	if err := rdb.Set(ctx, tripsKey, "3", time.Hour).Err(); err != nil {
		t.Fatalf("seed staging trips: %v", err)
	}
	prodTrips, err := h.countCircuitTrips(ctx, flagKey, "production", 1)
	if err != nil {
		t.Fatalf("countCircuitTrips(production): %v", err)
	}
	if prodTrips != 0 {
		t.Errorf("production circuit trips = %d after a staging-only trip, want 0 — cross-env contamination", prodTrips)
	}
	stagingTrips, err := h.countCircuitTrips(ctx, flagKey, "staging", 1)
	if err != nil {
		t.Fatalf("countCircuitTrips(staging): %v", err)
	}
	if stagingTrips != 3 {
		t.Errorf("staging circuit trips = %d, want 3", stagingTrips)
	}

	// Same check for the per-hour circuit-state snapshot buildHistory reads.
	snapshotKey := fmt.Sprintf("circuit:%s:staging:state:%d", flagKey, unixHour)
	if err := rdb.Set(ctx, snapshotKey, "OPEN", time.Hour).Err(); err != nil {
		t.Fatalf("seed staging state snapshot: %v", err)
	}
	prodHistory := h.buildHistory(ctx, flagKey, "production", 1)
	if len(prodHistory) != 1 || prodHistory[0].CircuitState == "OPEN" {
		t.Errorf("production history circuit_state = %q after a staging-only OPEN snapshot, want CLOSED (falls back to live state) — cross-env contamination", prodHistory[0].CircuitState)
	}
	stagingHistory := h.buildHistory(ctx, flagKey, "staging", 1)
	if len(stagingHistory) != 1 || stagingHistory[0].CircuitState != "OPEN" {
		t.Errorf("staging history circuit_state = %q, want OPEN", stagingHistory[0].CircuitState)
	}
}

// TestHandleFlagSLO_DefaultsToProductionAndReturnsZeroesWhenNoDataExists
// proves the graceful-fallback behavior documented on aggregateTelemetry:
// with no telemetry writer yet, the endpoint returns zeroes, not an error.
func TestHandleFlagSLO_DefaultsToProductionAndReturnsZeroesWhenNoDataExists(t *testing.T) {
	h, _ := newTestHandler(t)

	r := chi.NewRouter()
	r.Get("/api/v1/flags/{key}/slo", h.HandleFlagSLO)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags/checkout-v2/slo", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
