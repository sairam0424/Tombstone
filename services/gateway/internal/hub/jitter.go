package hub

import (
	"math/rand"
	"time"
)

// JitterBackoff returns d adjusted by +/-20% random jitter. Applied to the
// sleep duration (not the stored backoff value) in reconnect loops so that
// many gateway replicas backing off in lockstep after a shared Redis/upstream
// outage don't all retry at the exact same instant (thundering herd).
func JitterBackoff(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	delta := int64(float64(d) * 0.4)
	if delta <= 0 {
		return d
	}
	return d - time.Duration(delta/2) + time.Duration(rand.Int63n(delta))
}
