package circuit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// TestShouldTrip verifies the circuit breaker trip logic without Redis.
// ShouldTrip is pure logic — no network call — so it's fast and safe to unit test.
func TestShouldTrip(t *testing.T) {
	b := &Breaker{
		ErrorRateThreshold: 0.05,
		MinRequests:        100,
		WindowDuration:     10 * time.Second,
		ObservationWindow:  5 * time.Minute,
	}

	tests := []struct {
		name       string
		errors     int64
		total      int64
		shouldTrip bool
	}{
		{"below min requests — never trips", 50, 50, false},
		{"exactly min requests at threshold — trips", 6, 100, true},
		{"below threshold — no trip", 4, 100, false},
		{"zero errors — no trip", 0, 100, false},
		{"all errors — trips", 100, 100, true},
		{"zero total — no trip (avoid divide by zero)", 0, 0, false},
		{"99 requests below min — no trip even at 100% error", 99, 99, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := Window{
				FlagKey:    "test-flag",
				ErrorCount: tc.errors,
				TotalCount: tc.total,
			}
			got := b.ShouldTrip(w)
			if got != tc.shouldTrip {
				t.Errorf("ShouldTrip(%d/%d) = %v, want %v", tc.errors, tc.total, got, tc.shouldTrip)
			}
		})
	}
}

func TestErrorRate(t *testing.T) {
	b := &Breaker{ErrorRateThreshold: 0.05}

	tests := []struct {
		errors, total int64
		want          float64
	}{
		{5, 100, 0.05},
		{0, 100, 0.0},
		{100, 100, 1.0},
		{0, 0, 0.0}, // zero total returns 0
	}

	for _, tc := range tests {
		w := Window{ErrorCount: tc.errors, TotalCount: tc.total}
		got := b.ErrorRate(w)
		if got != tc.want {
			t.Errorf("ErrorRate(%d/%d) = %f, want %f", tc.errors, tc.total, got, tc.want)
		}
	}
}

// TestGetSetStateIsEnvironmentScoped is the regression test for EVAL-1: circuit
// state must be keyed per (flag, environment), so a trip in one environment never
// contaminates another environment's state for the same flag key.
func TestGetSetStateIsEnvironmentScoped(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	b := NewBreaker(redis.NewClient(&redis.Options{Addr: mr.Addr()}), zap.NewNop())
	ctx := context.Background()

	// Open the circuit for "checkout" in staging only.
	b.SetState(ctx, "checkout", "staging", StateOpen, time.Minute)

	// Production must be unaffected — before this fix both environments shared
	// key "circuit:checkout:state", so this returned OPEN (the contamination bug).
	if got := b.GetState(ctx, "checkout", "production"); got != StateClosed {
		t.Errorf("production state = %q after a staging-only trip, want CLOSED (cross-env contamination)", got)
	}
	if got := b.GetState(ctx, "checkout", "staging"); got != StateOpen {
		t.Errorf("staging state = %q, want OPEN", got)
	}
	if stateKey("checkout", "staging") == stateKey("checkout", "production") {
		t.Error("stateKey must differ by environment")
	}
}

// TestStateKeyDoesNotCollideAcrossAColonInEitherComponent is the regression
// test for a real vulnerability found by adversarial review of EVAL-2:
// stateKey used to join flagKey and env with a bare ':', so
// flagKey="checkout:v2", env="production" formatted to the IDENTICAL string
// as flagKey="checkout", env="v2:production" -- letting a crafted flag
// key/environment pair read or, worse, OVERWRITE another flag/environment
// pair's live circuit state (the same bug class INT-2 fixed in
// app/graph/builder.py's depgraph_key). Neither flagKey nor env is
// validated to exclude colons anywhere in this codebase.
func TestStateKeyDoesNotCollideAcrossAColonInEitherComponent(t *testing.T) {
	a := stateKey("checkout:v2", "production")
	b := stateKey("checkout", "v2:production")
	if a == b {
		t.Errorf("stateKey(%q, %q) == stateKey(%q, %q) == %q -- colon-split collision", "checkout:v2", "production", "checkout", "v2:production", a)
	}
}

// TestTryTrip_OnlyOneCallerWins is the regression test for a real finding
// from adversarial review of PR #219: Aggregator.Flush's own
// GetState-then-ShouldTrip-then-SetState sequence is a check-then-act race
// across evaluator replicas sharing this Redis-backed breaker state.
// TryTrip's SETNX-based claim must let exactly one racing caller through.
func TestTryTrip_OnlyOneCallerWins(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	b := NewBreaker(redis.NewClient(&redis.Options{Addr: mr.Addr()}), zap.NewNop())
	ctx := context.Background()

	const racers = 20
	var wonCount atomic.Int32
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			if b.TryTrip(ctx, "checkout", "production") {
				wonCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if wonCount.Load() != 1 {
		t.Errorf("TryTrip: %d of %d racing callers won the claim, want exactly 1", wonCount.Load(), racers)
	}
}

// TestTryTrip_IsEnvironmentScoped mirrors TestGetSetStateIsEnvironmentScoped:
// a trip claim for one environment must never block a trip claim for
// another environment on the same flag key.
func TestTryTrip_IsEnvironmentScoped(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	b := NewBreaker(redis.NewClient(&redis.Options{Addr: mr.Addr()}), zap.NewNop())
	ctx := context.Background()

	if !b.TryTrip(ctx, "checkout", "staging") {
		t.Fatal("first TryTrip for staging should win the claim")
	}
	if !b.TryTrip(ctx, "checkout", "production") {
		t.Error("TryTrip for production was blocked by staging's claim -- trip locks must be environment-scoped")
	}
	if b.TryTrip(ctx, "checkout", "staging") {
		t.Error("a second TryTrip for staging won the claim again -- SETNX should have refused it")
	}
}

// TestTryTrip_FailsOpenOnRedisError verifies TryTrip returns true (allows
// the trip) when Redis is unreachable -- refusing to trip because the
// dedup mechanism itself is down would silently disable the entire
// auto-rollback safety net during a Redis outage, worse than an
// occasional harmless duplicate.
func TestTryTrip_FailsOpenOnRedisError(t *testing.T) {
	b := NewBreaker(redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"}), zap.NewNop())
	if !b.TryTrip(context.Background(), "checkout", "production") {
		t.Error("TryTrip = false on a Redis error, want true (fail open)")
	}
}

// TestNextRollbackStep pins the EVAL-4 step-down ladder (100->50->25->0).
func TestNextRollbackStep(t *testing.T) {
	tests := []struct {
		current  int
		wantNext int
		wantDone bool
	}{
		{100, 50, false},
		{50, 25, false},
		{25, 0, true},
		{0, 0, true}, // already at the bottom -- stays done, doesn't go negative
	}
	for _, tc := range tests {
		next, done := NextRollbackStep(tc.current)
		if next != tc.wantNext || done != tc.wantDone {
			t.Errorf("NextRollbackStep(%d) = (%d, %v), want (%d, %v)", tc.current, next, done, tc.wantNext, tc.wantDone)
		}
	}
}

// TestNextRecoveryStep pins the EVAL-4 step-up ladder (10->25->50->100).
func TestNextRecoveryStep(t *testing.T) {
	tests := []struct {
		current       int
		wantNext      int
		wantRecovered bool
	}{
		{0, 10, false},
		{10, 25, false},
		{25, 50, false},
		{50, 100, true},
		{100, 100, true}, // already fully recovered -- stays recovered
	}
	for _, tc := range tests {
		next, recovered := NextRecoveryStep(tc.current)
		if next != tc.wantNext || recovered != tc.wantRecovered {
			t.Errorf("NextRecoveryStep(%d) = (%d, %v), want (%d, %v)", tc.current, next, recovered, tc.wantNext, tc.wantRecovered)
		}
	}
}

// TestStepAndOpenedAtRoundTripThroughRedis verifies the two new Redis
// accessor pairs behave correctly both when set and when absent (the
// "never happened yet" defaults NextRollbackStep/the HALF_OPEN transition
// logic in aggregator.go rely on).
func TestStepAndOpenedAtRoundTripThroughRedis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	b := NewBreaker(redis.NewClient(&redis.Options{Addr: mr.Addr()}), zap.NewNop())
	ctx := context.Background()

	if got := b.GetStep(ctx, "checkout", "production"); got != 100 {
		t.Errorf("GetStep with nothing set = %d, want 100 (nothing stepped down from yet)", got)
	}
	b.SetStep(ctx, "checkout", "production", 50)
	if got := b.GetStep(ctx, "checkout", "production"); got != 50 {
		t.Errorf("GetStep after SetStep(50) = %d, want 50", got)
	}
	// Environment-scoped, matching stateKey/tripLockKey's own convention.
	if got := b.GetStep(ctx, "checkout", "staging"); got != 100 {
		t.Errorf("GetStep for a different environment = %d after production's SetStep, want 100 (unaffected)", got)
	}

	if _, ok := b.GetOpenedAt(ctx, "checkout", "production"); ok {
		t.Error("GetOpenedAt with nothing set: ok = true, want false")
	}
	now := time.Unix(1_700_000_000, 0)
	b.SetOpenedAt(ctx, "checkout", "production", now)
	got, ok := b.GetOpenedAt(ctx, "checkout", "production")
	if !ok || !got.Equal(now) {
		t.Errorf("GetOpenedAt after SetOpenedAt = (%v, %v), want (%v, true)", got, ok, now)
	}
}

// TestReleaseTrip verifies a released trip-lock lets an immediate
// subsequent TryTrip win the claim again, rather than waiting out the full
// tripLockTTL -- the fix for a real finding from adversarial review of
// PR #219 (a failed SetState after a successful TryTrip previously stalled
// every retry for up to 30s with no way to recover sooner).
func TestReleaseTrip(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	b := NewBreaker(redis.NewClient(&redis.Options{Addr: mr.Addr()}), zap.NewNop())
	ctx := context.Background()

	if !b.TryTrip(ctx, "checkout", "production") {
		t.Fatal("first TryTrip should win the claim")
	}
	if b.TryTrip(ctx, "checkout", "production") {
		t.Fatal("second TryTrip before release should be refused")
	}
	b.ReleaseTrip(ctx, "checkout", "production")
	if !b.TryTrip(ctx, "checkout", "production") {
		t.Error("TryTrip after ReleaseTrip should win the claim again immediately, not wait out tripLockTTL")
	}
}

// TestStateConstants verifies the state strings are exactly what the Redis keys expect.
// If these change the gateway broadcaster will misparse channel names.
func TestStateConstants(t *testing.T) {
	if StateClosed != "CLOSED" {
		t.Errorf("StateClosed = %q, want CLOSED", StateClosed)
	}
	if StateOpen != "OPEN" {
		t.Errorf("StateOpen = %q, want OPEN", StateOpen)
	}
	if StateHalfOpen != "HALF_OPEN" {
		t.Errorf("StateHalfOpen = %q, want HALF_OPEN", StateHalfOpen)
	}
}
