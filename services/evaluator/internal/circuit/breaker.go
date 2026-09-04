package circuit

import (
	"context"
	"net/url"
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

// EscapeKeyComponent percent-encodes a single Redis-key component (via
// url.QueryEscape, which — unlike url.PathEscape — escapes ':') so that
// joining two components with a bare ':' can never let a crafted pair
// collide with a different pair (e.g. flagKey="checkout:v2", env=
// "production" colliding with flagKey="checkout", env="v2:production").
// Neither flagKey (a client-chosen string with no character restriction —
// see flags.key's plain TEXT column) nor env (an unvalidated query
// parameter) can be assumed colon-free. This is the same bug class found
// and fixed in services/intelligence/app/graph/builder.py's depgraph_key
// (INT-2) — exported so callers outside this package (e.g. slo.go's own
// env-scoped keys) can use the identical escaping.
func EscapeKeyComponent(s string) string {
	return url.QueryEscape(s)
}

// stateKey builds the Redis key for a flag's live circuit state, scoped by
// environment. Env scoping is a correctness requirement: without it a trip in
// one environment (e.g. staging) overwrites another environment's state (e.g.
// production) for the same flag key, so a staging failure could fail-open or
// fail-closed production traffic — or mask a real production trip.
func stateKey(flagKey, env string) string {
	return "circuit:" + EscapeKeyComponent(flagKey) + ":" + EscapeKeyComponent(env) + ":state"
}

// GetState returns the current circuit state for a flag in an environment.
func (b *Breaker) GetState(ctx context.Context, flagKey, env string) State {
	val, err := b.rdb.Get(ctx, stateKey(flagKey, env)).Result()
	if err != nil {
		return StateClosed
	}
	return State(val)
}

// SetState updates the circuit state for a flag in an environment in Redis.
func (b *Breaker) SetState(ctx context.Context, flagKey, env string, state State, ttl time.Duration) {
	_ = b.rdb.Set(ctx, stateKey(flagKey, env), string(state), ttl).Err()
	b.logger.Info("circuit breaker state change",
		zap.String("flag", flagKey), zap.String("env", env), zap.String("state", string(state)))
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
