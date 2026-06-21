package circuit

import (
	"testing"
	"time"
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
