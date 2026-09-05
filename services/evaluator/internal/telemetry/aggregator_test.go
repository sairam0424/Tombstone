package telemetry

import (
	"context"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/tombstone/evaluator/internal/circuit"
	"go.uber.org/zap"
)

func newTestAggregator(t *testing.T) (*Aggregator, *redis.Client) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	breaker := circuit.NewBreaker(rdb, zap.NewNop())
	return NewAggregator(breaker, rdb, zap.NewNop()), rdb
}

// recordErrorBurst records n requests for flagKey/env, the first
// errorCount of which are errors -- enough volume (n >= breaker's default
// MinRequests=100) and error rate (errorCount/n > 5%) to trip on its own.
func recordErrorBurst(agg *Aggregator, flagKey, env string, n, errorCount int) {
	for i := 0; i < n; i++ {
		agg.Record(TelemetryEvent{FlagKey: flagKey, Environment: env, IsError: i < errorCount})
	}
}

// TestFlush_FiresOnTripWhenThresholdExceeded is the baseline regression
// test for Aggregator.Flush -- previously untested at all (found by
// adversarial review of PR #219, alongside the concurrency gap below).
func TestFlush_FiresOnTripWhenThresholdExceeded(t *testing.T) {
	agg, _ := newTestAggregator(t)
	var gotFlagKey, gotEnv string
	var gotErrorRate float64
	tripped := false
	agg.OnTrip = func(flagKey, env string, errorRate float64) {
		tripped = true
		gotFlagKey, gotEnv, gotErrorRate = flagKey, env, errorRate
	}

	recordErrorBurst(agg, "checkout", "production", 100, 10)
	agg.Flush(context.Background())

	if !tripped {
		t.Fatal("OnTrip did not fire for a 10% error rate at 100 requests (above the 5% threshold and 100 MinRequests floor)")
	}
	if gotFlagKey != "checkout" || gotEnv != "production" {
		t.Errorf("OnTrip(%q, %q, ...), want (%q, %q)", gotFlagKey, gotEnv, "checkout", "production")
	}
	if gotErrorRate != 0.1 {
		t.Errorf("errorRate = %v, want 0.1", gotErrorRate)
	}
}

// TestFlush_DoesNotFireBelowThreshold verifies a window that never
// crosses ShouldTrip's thresholds (checked via breaker_test.go's own
// TestShouldTrip in isolation) also never fires OnTrip through the full
// Flush path.
func TestFlush_DoesNotFireBelowThreshold(t *testing.T) {
	agg, _ := newTestAggregator(t)
	tripped := false
	agg.OnTrip = func(flagKey, env string, errorRate float64) { tripped = true }

	recordErrorBurst(agg, "checkout", "production", 100, 2) // 2% — below 5% threshold
	agg.Flush(context.Background())

	if tripped {
		t.Error("OnTrip fired for a 2% error rate, want no trip (below the 5% threshold)")
	}
}

// TestFlush_ResetsWindowAfterFlush verifies counters don't carry over
// between flush cycles -- otherwise a flag that was busy once would stay
// permanently eligible to trip even after traffic (and errors) stop.
func TestFlush_ResetsWindowAfterFlush(t *testing.T) {
	agg, _ := newTestAggregator(t)
	recordErrorBurst(agg, "checkout", "production", 100, 10)
	agg.Flush(context.Background())

	agg.mu.Lock()
	n := len(agg.windows)
	agg.mu.Unlock()
	if n != 0 {
		t.Errorf("windows map has %d entries after Flush, want 0 (reset for the next window)", n)
	}
}

// TestFlush_OnTripFiresExactlyOnceAcrossRacingReplicas is the regression
// test for a real finding from adversarial review of PR #219: two
// Aggregator instances (simulating two evaluator replicas -- "State is
// stored in Redis so multiple evaluator instances share it", per
// circuit.Breaker's own doc comment) racing Flush() for the same
// flag+environment both observe StateClosed and both decide ShouldTrip.
// Without circuit.Breaker.TryTrip's atomic SETNX claim, both would fire
// OnTrip for what is actually one underlying trip event -- a duplicate
// rollback.Executor.Execute call (idempotent, harmless) AND, since
// EVAL-2, a duplicate Slack alert (visible, confusing noise implying two
// separate incidents).
func TestFlush_OnTripFiresExactlyOnceAcrossRacingReplicas(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	breaker := circuit.NewBreaker(rdb, zap.NewNop())

	var mu sync.Mutex
	tripCount := 0
	onTrip := func(flagKey, env string, errorRate float64) {
		mu.Lock()
		tripCount++
		mu.Unlock()
	}

	// Two independent Aggregators sharing one breaker/Redis, exactly as
	// two evaluator replica processes would.
	agg1 := NewAggregator(breaker, rdb, zap.NewNop())
	agg1.OnTrip = onTrip
	agg2 := NewAggregator(breaker, rdb, zap.NewNop())
	agg2.OnTrip = onTrip

	recordErrorBurst(agg1, "checkout", "production", 100, 10)
	recordErrorBurst(agg2, "checkout", "production", 100, 10)

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); agg1.Flush(ctx) }()
	go func() { defer wg.Done(); agg2.Flush(ctx) }()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if tripCount != 1 {
		t.Errorf("OnTrip fired %d times across 2 racing replicas for one trip event, want exactly 1", tripCount)
	}
}
