package circuit

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type State string

const (
	StateClosed   State = "CLOSED"
	StateOpen     State = "OPEN"
	StateHalfOpen State = "HALF_OPEN"
)

// Breaker is a per-flag circuit breaker.
// State is stored in Redis so multiple evaluator instances share it.
type Breaker struct {
	rdb    *redis.Client
	logger *zap.Logger
	mu     sync.Mutex
	// Configurable thresholds
	ErrorRateThreshold float64       // default 0.05 (5%)
	MinRequests        int64         // default 100
	WindowDuration     time.Duration // default 10s
	ObservationWindow  time.Duration // default 5m (for HALF_OPEN)
}

func NewBreaker(rdb *redis.Client, logger *zap.Logger) *Breaker {
	return &Breaker{
		rdb:                rdb,
		logger:             logger,
		ErrorRateThreshold: 0.05,
		MinRequests:        100,
		WindowDuration:     10 * time.Second,
		ObservationWindow:  5 * time.Minute,
	}
}

// GetState returns the current circuit state for a flag.
func (b *Breaker) GetState(ctx context.Context, flagKey string) State {
	val, err := b.rdb.Get(ctx, "circuit:"+flagKey+":state").Result()
	if err != nil {
		return StateClosed
	}
	return State(val)
}

// SetState updates the circuit state for a flag in Redis.
func (b *Breaker) SetState(ctx context.Context, flagKey string, state State, ttl time.Duration) {
	_ = b.rdb.Set(ctx, "circuit:"+flagKey+":state", string(state), ttl).Err()
	b.logger.Info("circuit breaker state change",
		zap.String("flag", flagKey), zap.String("state", string(state)))
}

// Window holds an aggregated error rate window for a flag.
type Window struct {
	FlagKey     string
	ErrorCount  int64
	TotalCount  int64
	WindowStart time.Time
}

// ErrorRate returns the fraction of errors in this window.
func (b *Breaker) ErrorRate(w Window) float64 {
	if w.TotalCount == 0 {
		return 0
	}
	return float64(w.ErrorCount) / float64(w.TotalCount)
}

// ShouldTrip returns true if the circuit should open based on observed window.
func (b *Breaker) ShouldTrip(w Window) bool {
	if w.TotalCount < b.MinRequests {
		return false
	}
	return b.ErrorRate(w) > b.ErrorRateThreshold
}
