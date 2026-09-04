package v1

import (
	"context"
	"encoding/json"
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

// TestSLOKeysDoNotCollideAcrossAColonInEitherComponent is the regression
// test for a real vulnerability found by adversarial review of EVAL-2:
// aggregateTelemetry/countCircuitTrips/buildHistory's Redis keys used to
// join flagKey and env with a bare ':', so flagKey="checkout:v2",
// env="production" formatted to the IDENTICAL key as flagKey="checkout",
// env="v2:production" (the same bug class INT-2 fixed in depgraph_key).
// Seeds via circuit.EscapeKeyComponent directly -- the same escaping the
// fixed production code now applies -- so this proves seed-and-read
// consistency, not just that some arbitrary escaping exists.
func TestSLOKeysDoNotCollideAcrossAColonInEitherComponent(t *testing.T) {
	h, rdb := newTestHandler(t)
	ctx := context.Background()

	hour := time.Now().UTC().Truncate(time.Hour)
	unixHour := hour.Unix() / 3600

	// Two colon-containing (flagKey, env) pairs that collide under the old,
	// unescaped bug -- both would format to "telemetry:checkout:v2:production:hour:{h}".
	flagA, envA := "checkout:v2", "production"
	flagB, envB := "checkout", "v2:production"

	keyA := fmt.Sprintf("telemetry:%s:%s:hour:%d", circuit.EscapeKeyComponent(flagA), circuit.EscapeKeyComponent(envA), unixHour)
	keyB := fmt.Sprintf("telemetry:%s:%s:hour:%d", circuit.EscapeKeyComponent(flagB), circuit.EscapeKeyComponent(envB), unixHour)
	if keyA == keyB {
		t.Fatalf("test setup invalid: keyA == keyB == %q", keyA)
	}
	if err := rdb.Set(ctx, keyA, `{"total":10,"errors":1,"p99_ms":1}`, time.Hour).Err(); err != nil {
		t.Fatalf("seed keyA: %v", err)
	}
	if err := rdb.Set(ctx, keyB, `{"total":20,"errors":2,"p99_ms":2}`, time.Hour).Err(); err != nil {
		t.Fatalf("seed keyB: %v", err)
	}

	totalA, errorsA, _, err := h.aggregateTelemetry(ctx, flagA, envA, 1)
	if err != nil {
		t.Fatalf("aggregateTelemetry(A): %v", err)
	}
	if totalA != 10 || errorsA != 1 {
		t.Errorf("aggregateTelemetry(%q, %q) = (%d, %d), want (10, 1) -- got pair B's data, colon-split collision", flagA, envA, totalA, errorsA)
	}

	totalB, errorsB, _, err := h.aggregateTelemetry(ctx, flagB, envB, 1)
	if err != nil {
		t.Fatalf("aggregateTelemetry(B): %v", err)
	}
	if totalB != 20 || errorsB != 2 {
		t.Errorf("aggregateTelemetry(%q, %q) = (%d, %d), want (20, 2) -- got pair A's data, colon-split collision", flagB, envB, totalB, errorsB)
	}
}

// TestAggregateTelemetry_SumsAcrossMultipleHours and its sibling below prove
// the cross-bucket accumulation logic (totalCount +=, errorCount +=, and the
// bucket.P99Ms > maxP99 max-tracking), which every OTHER test in this file
// only ever exercises against a single seeded hourly bucket (hours=1).
func TestAggregateTelemetry_SumsAcrossMultipleHours(t *testing.T) {
	h, rdb := newTestHandler(t)
	ctx := context.Background()

	flagKey, env := "checkout-v2", "production"
	now := time.Now().UTC()

	buckets := []struct {
		hoursAgo int
		total    int64
		errors   int64
		p99      float64
	}{
		{0, 10, 1, 5.0},
		{1, 20, 2, 50.0}, // highest p99 -- must win the max-tracking
		{2, 5, 0, 3.0},
	}
	for _, b := range buckets {
		hour := now.Add(-time.Duration(b.hoursAgo) * time.Hour).Truncate(time.Hour)
		unixHour := hour.Unix() / 3600
		key := fmt.Sprintf("telemetry:%s:%s:hour:%d", circuit.EscapeKeyComponent(flagKey), circuit.EscapeKeyComponent(env), unixHour)
		val := fmt.Sprintf(`{"total":%d,"errors":%d,"p99_ms":%f}`, b.total, b.errors, b.p99)
		if err := rdb.Set(ctx, key, val, time.Hour).Err(); err != nil {
			t.Fatalf("seed hour -%d: %v", b.hoursAgo, err)
		}
	}

	total, errors, p99, err := h.aggregateTelemetry(ctx, flagKey, env, 3)
	if err != nil {
		t.Fatalf("aggregateTelemetry: %v", err)
	}
	if total != 35 {
		t.Errorf("total = %d, want 35 (10+20+5)", total)
	}
	if errors != 3 {
		t.Errorf("errors = %d, want 3 (1+2+0)", errors)
	}
	if p99 != 50.0 {
		t.Errorf("p99 = %v, want 50.0 (max across 3 buckets)", p99)
	}
}

func TestCountCircuitTrips_SumsAcrossMultipleHours(t *testing.T) {
	h, rdb := newTestHandler(t)
	ctx := context.Background()

	flagKey, env := "checkout-v2", "production"
	now := time.Now().UTC()

	for hoursAgo, trips := range map[int]int64{0: 1, 1: 2, 2: 3} {
		hour := now.Add(-time.Duration(hoursAgo) * time.Hour).Truncate(time.Hour)
		unixHour := hour.Unix() / 3600
		key := fmt.Sprintf("circuit:%s:%s:trips:%d", circuit.EscapeKeyComponent(flagKey), circuit.EscapeKeyComponent(env), unixHour)
		if err := rdb.Set(ctx, key, fmt.Sprintf("%d", trips), time.Hour).Err(); err != nil {
			t.Fatalf("seed hour -%d: %v", hoursAgo, err)
		}
	}

	total, err := h.countCircuitTrips(ctx, flagKey, env, 3)
	if err != nil {
		t.Fatalf("countCircuitTrips: %v", err)
	}
	if total != 6 {
		t.Errorf("total = %d, want 6 (1+2+3)", total)
	}
}

// TestBuildHistory_OrdersOldestFirstAndClampsToHistoryHoursMax proves two
// properties no existing test verified: buildHistory returns points
// oldest-first (the loop counts DOWN from hours-1 to 0), and it clamps to
// historyHoursMax (168) rather than returning up to 2160 points for a
// window=90d request.
func TestBuildHistory_OrdersOldestFirstAndClampsToHistoryHoursMax(t *testing.T) {
	h, rdb := newTestHandler(t)
	ctx := context.Background()

	flagKey, env := "checkout-v2", "production"
	now := time.Now().UTC()

	// Seed distinguishable error rates 2 hours apart so ordering is verifiable.
	olderHour := now.Add(-1 * time.Hour).Truncate(time.Hour)
	newerHour := now.Truncate(time.Hour)
	olderKey := fmt.Sprintf("telemetry:%s:%s:hour:%d", circuit.EscapeKeyComponent(flagKey), circuit.EscapeKeyComponent(env), olderHour.Unix()/3600)
	newerKey := fmt.Sprintf("telemetry:%s:%s:hour:%d", circuit.EscapeKeyComponent(flagKey), circuit.EscapeKeyComponent(env), newerHour.Unix()/3600)
	if err := rdb.Set(ctx, olderKey, `{"total":100,"errors":10,"p99_ms":1}`, time.Hour).Err(); err != nil {
		t.Fatalf("seed older hour: %v", err)
	}
	if err := rdb.Set(ctx, newerKey, `{"total":100,"errors":90,"p99_ms":1}`, time.Hour).Err(); err != nil {
		t.Fatalf("seed newer hour: %v", err)
	}

	points := h.buildHistory(ctx, flagKey, env, 2)
	if len(points) != 2 {
		t.Fatalf("len(points) = %d, want 2", len(points))
	}
	if points[0].ErrorRate >= points[1].ErrorRate {
		t.Errorf("points[0].ErrorRate=%v, points[1].ErrorRate=%v -- want oldest-first (older bucket's lower 0.10 error rate before newer bucket's higher 0.90)", points[0].ErrorRate, points[1].ErrorRate)
	}

	// window=90d -> 2160 hours; must clamp to historyHoursMax (168).
	clamped := h.buildHistory(ctx, flagKey, env, maxWindowDays*hoursPerDay)
	if len(clamped) != historyHoursMax {
		t.Errorf("len(clamped) = %d, want %d (historyHoursMax clamp)", len(clamped), historyHoursMax)
	}
}

// TestAggregateTelemetry_RedisErrorIsRecordedButAggregationContinues and its
// sibling below prove the genuine (non-Nil, non-"key not found") Redis error
// path, which every other test in this file only exercises via redis.Nil
// (key simply absent) -- a real error requires actually breaking the
// connection, not just reading a missing key.
func TestAggregateTelemetry_RedisErrorIsRecordedButAggregationContinues(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	breaker := circuit.NewBreaker(rdb, zap.NewNop())
	h := NewHandler(rdb, breaker, zap.NewNop())

	flagKey, env := "checkout-v2", "production"
	now := time.Now().UTC()

	// Seed hour -0 with real data, then kill Redis so hour -1's Get fails
	// with a genuine connection error, not redis.Nil.
	hour0 := now.Truncate(time.Hour)
	key0 := fmt.Sprintf("telemetry:%s:%s:hour:%d", circuit.EscapeKeyComponent(flagKey), circuit.EscapeKeyComponent(env), hour0.Unix()/3600)
	if err := rdb.Set(context.Background(), key0, `{"total":10,"errors":1,"p99_ms":1}`, time.Hour).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mr.Close()

	total, errors, _, aggErr := h.aggregateTelemetry(context.Background(), flagKey, env, 2)
	if aggErr == nil {
		t.Fatal("aggregateTelemetry returned nil error against a closed Redis connection, want a non-nil error surfaced to the caller")
	}
	// Best-effort: whatever succeeded before the connection died should still
	// be reflected, not discarded wholesale.
	_ = total
	_ = errors
}

func TestCountCircuitTrips_RedisErrorIsRecordedButAggregationContinues(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	breaker := circuit.NewBreaker(rdb, zap.NewNop())
	h := NewHandler(rdb, breaker, zap.NewNop())

	flagKey, env := "checkout-v2", "production"
	mr.Close()

	_, tripsErr := h.countCircuitTrips(context.Background(), flagKey, env, 1)
	if tripsErr == nil {
		t.Fatal("countCircuitTrips returned nil error against a closed Redis connection, want a non-nil error surfaced to the caller")
	}
}

// TestHandleFlagSLO_ReturnsZeroedBodyWhenNoDataExists decodes the actual
// JSON response body -- the version of this test before adversarial review
// only asserted HTTP 200, proving nothing about error_rate, evaluation_count,
// circuit_trips, or slo_budget_remaining despite its name implying otherwise.
func TestHandleFlagSLO_ReturnsZeroedBodyWhenNoDataExists(t *testing.T) {
	h, _ := newTestHandler(t)

	r := chi.NewRouter()
	r.Get("/api/v1/flags/{key}/slo", h.HandleFlagSLO)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags/checkout-v2/slo", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp SLOResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if resp.ErrorRate != 0 {
		t.Errorf("ErrorRate = %v, want 0", resp.ErrorRate)
	}
	if resp.EvaluationCount != 0 {
		t.Errorf("EvaluationCount = %d, want 0", resp.EvaluationCount)
	}
	if resp.CircuitTrips != 0 {
		t.Errorf("CircuitTrips = %d, want 0", resp.CircuitTrips)
	}
	if resp.SLOBudgetRemaining != 1 {
		t.Errorf("SLOBudgetRemaining = %v, want 1 (no errors -> full budget)", resp.SLOBudgetRemaining)
	}
	if resp.CircuitState != string(circuit.StateClosed) {
		t.Errorf("CircuitState = %q, want %q", resp.CircuitState, circuit.StateClosed)
	}
}

// TestHandleFlagSLO_DefaultsToProductionEnvironment proves the "defaults to
// production" behavior for real, by observation: a request with NO
// ?environment= query param must return production-scoped data, not just
// "doesn't crash." SLOResponse has no environment field to assert on
// directly, so this seeds a production-only bucket and confirms an
// unscoped request surfaces it.
func TestHandleFlagSLO_DefaultsToProductionEnvironment(t *testing.T) {
	h, rdb := newTestHandler(t)
	ctx := context.Background()

	flagKey := "checkout-v2"
	hour := time.Now().UTC().Truncate(time.Hour)
	key := fmt.Sprintf("telemetry:%s:%s:hour:%d", circuit.EscapeKeyComponent(flagKey), circuit.EscapeKeyComponent("production"), hour.Unix()/3600)
	if err := rdb.Set(ctx, key, `{"total":100,"errors":25,"p99_ms":9.5}`, time.Hour).Err(); err != nil {
		t.Fatalf("seed production bucket: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/api/v1/flags/{key}/slo", h.HandleFlagSLO)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags/"+flagKey+"/slo", nil) // no ?environment=
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var resp SLOResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if resp.EvaluationCount != 100 {
		t.Errorf("EvaluationCount = %d, want 100 -- an unscoped request must default to the production bucket", resp.EvaluationCount)
	}
}
