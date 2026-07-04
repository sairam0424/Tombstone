package hub

import (
	"testing"
	"time"
)

// TestJitterBackoff_WithinExpectedBand verifies the jittered duration stays
// within +/-20% of the input, and that repeated calls aren't all identical
// (i.e. jitter is actually being applied, not a no-op).
func TestJitterBackoff_WithinExpectedBand(t *testing.T) {
	const d = 10 * time.Second
	lower := d - d/5 // -20%
	upper := d + d/5 // +20%

	seen := make(map[time.Duration]bool)
	for i := 0; i < 50; i++ {
		got := JitterBackoff(d)
		if got < lower || got > upper {
			t.Fatalf("JitterBackoff(%v) = %v, want within [%v, %v]", d, got, lower, upper)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Fatalf("JitterBackoff(%v) returned the same value %d times in a row — jitter is not being applied", d, 50)
	}
}

// TestJitterBackoff_ZeroAndNegative verifies degenerate inputs pass through
// unchanged rather than panicking (rand.Int63n panics on n<=0).
func TestJitterBackoff_ZeroAndNegative(t *testing.T) {
	if got := JitterBackoff(0); got != 0 {
		t.Fatalf("JitterBackoff(0) = %v, want 0", got)
	}
	if got := JitterBackoff(-time.Second); got != -time.Second {
		t.Fatalf("JitterBackoff(-1s) = %v, want -1s", got)
	}
}
