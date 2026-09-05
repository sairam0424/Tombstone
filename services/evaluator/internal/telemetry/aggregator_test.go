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

// TestFlush_HandleClosedRespectsAnAlreadyClaimedTrip is the regression
// test for a real finding from adversarial review of PR #219: two
// Aggregator instances (simulating two evaluator replicas -- "State is
// stored in Redis so multiple evaluator instances share it", per
// circuit.Breaker's own doc comment) racing Flush() for the same
// flag+environment could both observe StateClosed and both decide
// ShouldTrip. Without circuit.Breaker.TryTrip's atomic SETNX claim, both
// would fire OnRolloutChange for what is actually one underlying trip
// event.
//
// This drives the scenario deterministically (pre-claiming via TryTrip
// directly) rather than via two real goroutines, for the same reason
// TestFlush_HandleOpenRespectsAnAlreadyClaimedStep does -- an earlier
// version of this test launched agg1/agg2.Flush() concurrently and
// asserted tripCount==1, which flakes under genuine (not forced)
// goroutine scheduling: whichever replica's Flush completes first
// (TryTrip, callback, SetState, SetStep -- all the way through) can finish
// before the other even starts, so the "loser" reads POST-transition
// state (StateOpen, step=50) and calls handleOpen instead of
// handleClosed, legitimately advancing to a DIFFERENT, later step (25)
// rather than colliding on the SAME initial trip -- observed for real in
// CI on this exact test. TestTryTrip_OnlyOneCallerWins (circuit package)
// separately proves TryTrip's own SETNX claim is atomic under a true
// concurrent race with 20 racers.
func TestFlush_HandleClosedRespectsAnAlreadyClaimedTrip(t *testing.T) {
	agg, _, breaker := newTestAggregator(t)
	ctx := context.Background()

	// Simulates another replica having already won the initial trip claim
	// moments earlier.
	if !breaker.TryTrip(ctx, "checkout", "production") {
		t.Fatal("test setup: pre-claim should have succeeded on a fresh key")
	}

	fired := false
	agg.OnRolloutChange = func(string, string, int, float64, circuit.RolloutPhase) bool { fired = true; return true }

	recordErrorBurst(agg, "checkout", "production", 100, 10)
	agg.Flush(ctx)

	if fired {
		t.Error("OnRolloutChange fired despite the initial trip already being claimed -- handleClosed must respect TryTrip's result")
	}
	if got := breaker.GetState(ctx, "checkout", "production"); got != circuit.StateClosed {
		t.Errorf("state = %q, want unchanged CLOSED -- a respected claim must not advance local state either", got)
	}
}

// TestFlush_HandleOpenRespectsAnAlreadyClaimedStep is the regression test
// for a real finding from adversarial review of PR #221:
// TestFlush_OnRolloutChangeFiresExactlyOnceAcrossRacingReplicas above only
// covers the INITIAL trip (TryTrip's own claim) -- every LATER step-down
// transition had no equivalent dedup before Breaker.TryStep existed, so
// two replicas racing the SAME already-OPEN flag+env could each
// independently commit the SAME step-down, each producing its own
// audit_log row, SSE broadcast, and (at the terminal step) Slack alert
// for what is really one transition.
//
// This drives the scenario deterministically rather than via two real
// goroutines: launching two truly concurrent Flush() calls and hoping
// they collide on the exact same target is inherently nondeterministic
// (whichever goroutine's OS thread runs first can complete its ENTIRE
// Flush -- including the SetStep that advances the ladder -- before the
// other even starts, so the "loser" ends up computing a different,
// legitimately later target instead of colliding on the same one; an
// earlier version of this test asserted count==1 and flaked exactly this
// way). Pre-claiming the target with TryStep directly reproduces the
// state a genuinely racing replica would have already committed,
// deterministically, on every run -- TestTryStep_OnlyOneCallerWins
// separately proves TryStep's own SETNX claim is atomic under a true
// concurrent race.
func TestFlush_HandleOpenRespectsAnAlreadyClaimedStep(t *testing.T) {
	agg, _, breaker := newTestAggregator(t)
	ctx := context.Background()

	breaker.SetState(ctx, "checkout", "production", circuit.StateOpen, time.Minute)
	breaker.SetStep(ctx, "checkout", "production", 50)
	// Simulates another replica having already won the claim for this
	// exact transition moments earlier.
	if !breaker.TryStep(ctx, "checkout", "production", "down", 25) {
		t.Fatal("test setup: pre-claim should have succeeded on a fresh key")
	}

	fired := false
	agg.OnRolloutChange = func(string, string, int, float64, circuit.RolloutPhase) bool { fired = true; return true }

	agg.Record(TelemetryEvent{FlagKey: "checkout", Environment: "production", IsError: false})
	agg.Flush(ctx)

	if fired {
		t.Error("OnRolloutChange fired despite the target step already being claimed -- handleOpen must respect TryStep's result")
	}
	if got, found := breaker.GetStep(ctx, "checkout", "production"); !found || got != 50 {
		t.Errorf("step = (%d, %v), want unchanged (50, true) -- a respected claim must not advance local state either", got, found)
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
	if _, found := breaker.GetStep(ctx, "checkout", "production"); found {
		t.Error("step recorded after a failed initial step, want not found (never committed)")
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

	if got, found := breaker.GetStep(ctx, "checkout", "production"); !found || got != 0 {
		t.Errorf("final step = (%d, %v), want (0, true)", got, found)
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
		if got, found := breaker.GetStep(ctx, "checkout", "production"); !found || got != 10 {
			t.Errorf("step = (%d, %v), want (10, true) (the recovery ladder's first probe)", got, found)
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
	if got, found := breaker.GetStep(ctx, "checkout", "production"); !found || got != 0 {
		t.Errorf("step = (%d, %v), want (0, true)", got, found)
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
	if got, found := breaker.GetStep(ctx, "checkout", "production"); !found || got != 25 {
		t.Errorf("step = (%d, %v), want unchanged (25, true)", got, found)
	}
}

// TestFlush_NilOnRolloutChangeDoesNotPanicWhileOpen and its HALF_OPEN
// counterpart below close a real test-coverage gap found by adversarial
// review of PR #221: every other test in this file explicitly assigns
// OnRolloutChange before driving OPEN/HALF_OPEN through Flush, so a
// regression removing one of handleOpen/handleHalfOpen's nil guards would
// have gone completely undetected here -- and because Aggregator.Run
// drives Flush from a background goroutine outside chi's Recoverer
// middleware (which only catches panics inside HTTP handler execution), a
// nil-pointer call panicking there crashes the entire evaluator process,
// not just one request.
func TestFlush_NilOnRolloutChangeDoesNotPanicWhileOpen(t *testing.T) {
	agg, _, breaker := newTestAggregator(t)
	ctx := context.Background()
	breaker.SetState(ctx, "checkout", "production", circuit.StateOpen, time.Minute)
	breaker.SetStep(ctx, "checkout", "production", 50)
	// agg.OnRolloutChange deliberately left nil.

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Flush panicked with OnRolloutChange nil while OPEN: %v", r)
		}
	}()
	agg.Record(TelemetryEvent{FlagKey: "checkout", Environment: "production", IsError: false})
	agg.Flush(ctx)
}

// TestFlush_NilOnRolloutChangeDoesNotPanicWhileHalfOpen mirrors the OPEN
// case above for HALF_OPEN's own two OnRolloutChange call sites (the
// revert branch and the advance branch).
func TestFlush_NilOnRolloutChangeDoesNotPanicWhileHalfOpen(t *testing.T) {
	agg, _, breaker := newTestAggregator(t)
	ctx := context.Background()
	breaker.SetState(ctx, "checkout", "production", circuit.StateHalfOpen, time.Minute)
	breaker.SetStep(ctx, "checkout", "production", 25)
	// agg.OnRolloutChange deliberately left nil.

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Flush panicked with OnRolloutChange nil while HALF_OPEN: %v", r)
		}
	}()

	t.Run("advance branch (healthy window)", func(t *testing.T) {
		recordErrorBurst(agg, "checkout", "production", 100, 0)
		agg.Flush(ctx)
	})
	t.Run("revert branch (bad window)", func(t *testing.T) {
		recordErrorBurst(agg, "checkout", "production", 100, 10)
		agg.Flush(ctx)
	})
}
