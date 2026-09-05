package circuit

import (
	"context"
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
