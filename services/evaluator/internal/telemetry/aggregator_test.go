package telemetry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/tombstone/evaluator/internal/circuit"
	"go.uber.org/zap"
)

func newTestAggregator(t *testing.T) (*Aggregator, *redis.Client, *circuit.Breaker) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	breaker := circuit.NewBreaker(rdb, zap.NewNop())
	return NewAggregator(breaker, rdb, zap.NewNop()), rdb, breaker
}

// recordErrorBurst records n requests for flagKey/env, the first
// errorCount of which are errors -- enough volume (n >= breaker's default
// MinRequests=100) and error rate (errorCount/n > 5%) to trip on its own.
func recordErrorBurst(agg *Aggregator, flagKey, env string, n, errorCount int) {
	for i := 0; i < n; i++ {
		agg.Record(TelemetryEvent{FlagKey: flagKey, Environment: env, IsError: i < errorCount})
	}
}

// alwaysSucceeds is an OnRolloutChange stub recording every call and
// returning true (the API call succeeded).
type rolloutCall struct {
	flagKey, env string
	targetPct    int
	errorRate    float64
	phase        circuit.RolloutPhase
}

func recordingCallback(calls *[]rolloutCall, mu *sync.Mutex) func(string, string, int, float64, circuit.RolloutPhase) bool {
	return func(flagKey, env string, targetPct int, errorRate float64, phase circuit.RolloutPhase) bool {
		mu.Lock()
		*calls = append(*calls, rolloutCall{flagKey, env, targetPct, errorRate, phase})
		mu.Unlock()
		return true
	}
}

// TestFlush_FiresRolloutChangeWhenThresholdExceeded is the baseline
// regression test for Aggregator.Flush's CLOSED->OPEN trip transition.
func TestFlush_FiresRolloutChangeWhenThresholdExceeded(t *testing.T) {
	agg, _, _ := newTestAggregator(t)
	var mu sync.Mutex
	var calls []rolloutCall
	agg.OnRolloutChange = recordingCallback(&calls, &mu)

	recordErrorBurst(agg, "checkout", "production", 100, 10)
	agg.Flush(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("OnRolloutChange fired %d times, want 1", len(calls))
	}
	c := calls[0]
	if c.flagKey != "checkout" || c.env != "production" {
		t.Errorf("callback(%q, %q, ...), want (%q, %q)", c.flagKey, c.env, "checkout", "production")
	}
	if c.errorRate != 0.1 {
		t.Errorf("errorRate = %v, want 0.1", c.errorRate)
	}
	if c.targetPct != 50 {
		t.Errorf("targetPct = %d, want 50 (the ladder's first step down from 100)", c.targetPct)
	}
	if c.phase != circuit.PhaseTripped {
		t.Errorf("phase = %q, want %q", c.phase, circuit.PhaseTripped)
	}
}

// TestFlush_DoesNotFireBelowThreshold verifies a window that never
// crosses ShouldTrip's thresholds (checked via breaker_test.go's own
// TestShouldTrip in isolation) also never fires OnRolloutChange through
// the full Flush path.
func TestFlush_DoesNotFireBelowThreshold(t *testing.T) {
	agg, _, _ := newTestAggregator(t)
	fired := false
	agg.OnRolloutChange = func(string, string, int, float64, circuit.RolloutPhase) bool { fired = true; return true }

	recordErrorBurst(agg, "checkout", "production", 100, 2) // 2% — below 5% threshold
	agg.Flush(context.Background())

	if fired {
		t.Error("OnRolloutChange fired for a 2% error rate, want no trip (below the 5% threshold)")
	}
}

// TestFlush_ResetsWindowAfterFlush verifies counters don't carry over
// between flush cycles -- otherwise a flag that was busy once would stay
// permanently eligible to trip even after traffic (and errors) stop.
func TestFlush_ResetsWindowAfterFlush(t *testing.T) {
	agg, _, _ := newTestAggregator(t)
	recordErrorBurst(agg, "checkout", "production", 100, 10)
	agg.Flush(context.Background())

	agg.mu.Lock()
	n := len(agg.windows)
	agg.mu.Unlock()
	if n != 0 {
		t.Errorf("windows map has %d entries after Flush, want 0 (reset for the next window)", n)
	}
}

// TestFlush_OnRolloutChangeFiresExactlyOnceAcrossRacingReplicas is the
// regression test for a real finding from adversarial review of PR #219:
// two Aggregator instances (simulating two evaluator replicas -- "State is
// stored in Redis so multiple evaluator instances share it", per
// circuit.Breaker's own doc comment) racing Flush() for the same
// flag+environment both observe StateClosed and both decide ShouldTrip.
// Without circuit.Breaker.TryTrip's atomic SETNX claim, both would fire
// OnRolloutChange for what is actually one underlying trip event.
func TestFlush_OnRolloutChangeFiresExactlyOnceAcrossRacingReplicas(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	breaker := circuit.NewBreaker(rdb, zap.NewNop())

	var mu sync.Mutex
	tripCount := 0
	onChange := func(flagKey, env string, targetPct int, errorRate float64, phase circuit.RolloutPhase) bool {
		mu.Lock()
		tripCount++
		mu.Unlock()
		return true
	}

	// Two independent Aggregators sharing one breaker/Redis, exactly as
	// two evaluator replica processes would.
	agg1 := NewAggregator(breaker, rdb, zap.NewNop())
	agg1.OnRolloutChange = onChange
	agg2 := NewAggregator(breaker, rdb, zap.NewNop())
	agg2.OnRolloutChange = onChange

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
		t.Errorf("OnRolloutChange fired %d times across 2 racing replicas for one trip event, want exactly 1", tripCount)
	}
}

// TestFlush_FailedInitialStepReleasesTripClaim verifies that when
// OnRolloutChange's first call (the initial trip) returns false (the API
// call failed), Flush does NOT commit StateOpen/SetStep, and releases the
// trip-lock so the very next Flush call (not 30s later) can retry --
// regression coverage for the fix to a real finding from adversarial
// review of PR #219.
func TestFlush_FailedInitialStepReleasesTripClaim(t *testing.T) {
	agg, _, breaker := newTestAggregator(t)
	callCount := 0
	agg.OnRolloutChange = func(string, string, int, float64, circuit.RolloutPhase) bool {
		callCount++
		return false
	}

	recordErrorBurst(agg, "checkout", "production", 100, 10)
	agg.Flush(context.Background())

	ctx := context.Background()
	if got := breaker.GetState(ctx, "checkout", "production"); got != circuit.StateClosed {
		t.Errorf("state = %q after a failed initial step, want CLOSED (never committed)", got)
	}
	if got := breaker.GetStep(ctx, "checkout", "production"); got != 100 {
		t.Errorf("step = %d after a failed initial step, want 100 (never committed)", got)
	}

	// Retry immediately (no sleep) -- a released trip claim should let
	// this succeed right away, not wait out tripLockTTL.
	recordErrorBurst(agg, "checkout", "production", 100, 10)
	agg.Flush(context.Background())
	if callCount != 2 {
		t.Errorf("OnRolloutChange called %d times, want 2 (the retry after release must reach the callback again)", callCount)
	}
}

// TestFlush_StepsDownTheFullLadderAcrossTicks proves the deterministic
// 100->50->25->0 descent: once tripped, each subsequent Flush tick
// advances to the next step regardless of that tick's own error rate,
// terminating at PhaseKilled.
func TestFlush_StepsDownTheFullLadderAcrossTicks(t *testing.T) {
	agg, _, breaker := newTestAggregator(t)
	var mu sync.Mutex
	var calls []rolloutCall
	agg.OnRolloutChange = recordingCallback(&calls, &mu)
	ctx := context.Background()

	// Tick 1: trip -> step to 50.
	recordErrorBurst(agg, "checkout", "production", 100, 10)
	agg.Flush(ctx)

	// Tick 2: still OPEN/step=50 -- even a CLEAN window (no errors at all)
	// must still advance to 25, since the descent is unconditional once
	// tripped.
	agg.Record(TelemetryEvent{FlagKey: "checkout", Environment: "production", IsError: false})
	agg.Flush(ctx)

	// Tick 3: step=25 -> 0 (terminal).
	agg.Record(TelemetryEvent{FlagKey: "checkout", Environment: "production", IsError: false})
	agg.Flush(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("OnRolloutChange fired %d times across 3 ticks, want 3", len(calls))
	}
	wantPcts := []int{50, 25, 0}
	wantPhases := []circuit.RolloutPhase{circuit.PhaseTripped, circuit.PhaseStepped, circuit.PhaseKilled}
	for i, c := range calls {
		if c.targetPct != wantPcts[i] {
			t.Errorf("call %d: targetPct = %d, want %d", i, c.targetPct, wantPcts[i])
		}
		if c.phase != wantPhases[i] {
			t.Errorf("call %d: phase = %q, want %q", i, c.phase, wantPhases[i])
		}
	}

	if got := breaker.GetStep(ctx, "checkout", "production"); got != 0 {
		t.Errorf("final step = %d, want 0", got)
	}
	if _, ok := breaker.GetOpenedAt(ctx, "checkout", "production"); !ok {
		t.Error("openedAt not recorded after reaching the ladder's terminal 0% step")
	}
}

// TestFlush_HalfOpenTransitionWaitsOutObservationWindow verifies Flush
// does not attempt a HALF_OPEN recovery probe before ObservationWindow's
// cooldown has elapsed since reaching 0%, and does attempt one once it
// has. Uses a backdated openedAt rather than a real sleep to decide
// "elapsed" -- this test environment's own miniredis/goroutine-scheduling
// overhead was observed to reach ~900ms between two adjacent Redis calls
// under load, which made any real-time race (even a generous one) flaky;
// comparing against a fixed, already-past timestamp is deterministic
// regardless of how slow the test process itself runs.
func TestFlush_HalfOpenTransitionWaitsOutObservationWindow(t *testing.T) {
	agg, _, breaker := newTestAggregator(t)
	breaker.ObservationWindow = 5 * time.Minute // production default
	ctx := context.Background()

	breaker.SetState(ctx, "checkout", "production", circuit.StateOpen, time.Hour)
	breaker.SetStep(ctx, "checkout", "production", 0)

	fired := false
	agg.OnRolloutChange = func(string, string, int, float64, circuit.RolloutPhase) bool { fired = true; return true }

	t.Run("not yet elapsed", func(t *testing.T) {
		breaker.SetOpenedAt(ctx, "checkout", "production", time.Now())
		agg.Record(TelemetryEvent{FlagKey: "checkout", Environment: "production", IsError: false})
		agg.Flush(ctx)
		if fired {
			t.Error("HALF_OPEN probe attempted before ObservationWindow elapsed")
		}
		if got := breaker.GetState(ctx, "checkout", "production"); got != circuit.StateOpen {
			t.Errorf("state = %q before cooldown elapsed, want OPEN", got)
		}
	})

	t.Run("elapsed", func(t *testing.T) {
		breaker.SetOpenedAt(ctx, "checkout", "production", time.Now().Add(-10*time.Minute))
		agg.Record(TelemetryEvent{FlagKey: "checkout", Environment: "production", IsError: false})
		agg.Flush(ctx)
		if !fired {
			t.Fatal("HALF_OPEN probe not attempted after ObservationWindow elapsed")
		}
		if got := breaker.GetState(ctx, "checkout", "production"); got != circuit.StateHalfOpen {
			t.Errorf("state = %q after cooldown elapsed, want HALF_OPEN", got)
		}
		if got := breaker.GetStep(ctx, "checkout", "production"); got != 10 {
			t.Errorf("step = %d, want 10 (the recovery ladder's first probe)", got)
		}
	})
}

// TestFlush_HalfOpenAdvancesOnAHealthyWindow verifies a HALF_OPEN probe
// with a clean error rate and enough traffic climbs to the next recovery
// step, eventually reaching PhaseRecovered and CLOSED at 100%.
func TestFlush_HalfOpenAdvancesOnAHealthyWindow(t *testing.T) {
	agg, _, breaker := newTestAggregator(t)
	ctx := context.Background()
	breaker.SetState(ctx, "checkout", "production", circuit.StateHalfOpen, time.Minute)
	breaker.SetStep(ctx, "checkout", "production", 50) // one step away from fully recovered

	var mu sync.Mutex
	var calls []rolloutCall
	agg.OnRolloutChange = recordingCallback(&calls, &mu)

	recordErrorBurst(agg, "checkout", "production", 100, 0) // clean window, plenty of traffic
	agg.Flush(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("OnRolloutChange fired %d times, want 1", len(calls))
	}
	if calls[0].targetPct != 100 || calls[0].phase != circuit.PhaseRecovered {
		t.Errorf("call = %+v, want targetPct=100 phase=%q", calls[0], circuit.PhaseRecovered)
	}
	if got := breaker.GetState(ctx, "checkout", "production"); got != circuit.StateClosed {
		t.Errorf("state = %q, want CLOSED", got)
	}
}

// TestFlush_HalfOpenRevertsOnABadWindow verifies a HALF_OPEN probe whose
// own window still trips the breaker reverts immediately to OPEN/0%,
// rather than continuing to climb -- this is the actual "post-rollback
// error-rate verification" EVAL-4's plan calls for.
func TestFlush_HalfOpenRevertsOnABadWindow(t *testing.T) {
	agg, _, breaker := newTestAggregator(t)
	ctx := context.Background()
	breaker.SetState(ctx, "checkout", "production", circuit.StateHalfOpen, time.Minute)
	breaker.SetStep(ctx, "checkout", "production", 25)

	var mu sync.Mutex
	var calls []rolloutCall
	agg.OnRolloutChange = recordingCallback(&calls, &mu)

	recordErrorBurst(agg, "checkout", "production", 100, 10) // still bad -- 10%
	agg.Flush(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("OnRolloutChange fired %d times, want 1", len(calls))
	}
	if calls[0].targetPct != 0 || calls[0].phase != circuit.PhaseRevertedDuringRecovery {
		t.Errorf("call = %+v, want targetPct=0 phase=%q", calls[0], circuit.PhaseRevertedDuringRecovery)
	}
	if got := breaker.GetState(ctx, "checkout", "production"); got != circuit.StateOpen {
		t.Errorf("state = %q, want OPEN", got)
	}
	if got := breaker.GetStep(ctx, "checkout", "production"); got != 0 {
		t.Errorf("step = %d, want 0", got)
	}
	if _, ok := breaker.GetOpenedAt(ctx, "checkout", "production"); !ok {
		t.Error("openedAt not reset after a failed recovery probe -- the next HALF_OPEN attempt must wait out a fresh cooldown")
	}
}

// TestFlush_HalfOpenHoldsWithInsufficientTraffic verifies a HALF_OPEN
// probe window with too little traffic to trust either way (below
// Breaker.MinRequests) neither advances nor reverts -- it holds at the
// current step and waits for more data.
func TestFlush_HalfOpenHoldsWithInsufficientTraffic(t *testing.T) {
	agg, _, breaker := newTestAggregator(t)
	ctx := context.Background()
	breaker.SetState(ctx, "checkout", "production", circuit.StateHalfOpen, time.Minute)
	breaker.SetStep(ctx, "checkout", "production", 25)

	fired := false
	agg.OnRolloutChange = func(string, string, int, float64, circuit.RolloutPhase) bool { fired = true; return true }

	// Well under MinRequests (100).
	recordErrorBurst(agg, "checkout", "production", 5, 0)
	agg.Flush(ctx)

	if fired {
		t.Error("OnRolloutChange fired despite insufficient traffic to verify the probe either way")
	}
	if got := breaker.GetState(ctx, "checkout", "production"); got != circuit.StateHalfOpen {
		t.Errorf("state = %q, want unchanged HALF_OPEN", got)
	}
	if got := breaker.GetStep(ctx, "checkout", "production"); got != 25 {
		t.Errorf("step = %d, want unchanged 25", got)
	}
}
