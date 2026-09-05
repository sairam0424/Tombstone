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

// tripLockKey builds the Redis key for TryTrip's short-lived claim guard,
// distinct from stateKey (which two racing callers would each read as
// StateClosed before either has a chance to write StateOpen).
func tripLockKey(flagKey, env string) string {
	return "circuit:" + EscapeKeyComponent(flagKey) + ":" + EscapeKeyComponent(env) + ":trip-lock"
}

// tripLockTTL only needs to outlast the brief window in which multiple
// evaluator replicas can all observe StateClosed before any of them
// commits StateOpen (stateKey's own TTL, set by SetState to 10 minutes,
// is what actually prevents re-tripping after that point).
const tripLockTTL = 30 * time.Second

// GetState returns the current circuit state for a flag in an environment.
func (b *Breaker) GetState(ctx context.Context, flagKey, env string) State {
	val, err := b.rdb.Get(ctx, stateKey(flagKey, env)).Result()
	if err != nil {
		return StateClosed
	}
	return State(val)
}

// TryTrip atomically claims the right to trip the circuit for flagKey in
// env, returning true for exactly one caller among any that race it for
// the same flag+environment. "State is stored in Redis so multiple
// evaluator instances share it" (see the Breaker doc comment) means
// Aggregator.Flush's own GetState-then-ShouldTrip-then-SetState sequence
// is a check-then-act race across replicas: two replicas can each read
// StateClosed via GetState, both decide to trip, and both fire OnTrip --
// which, since EVAL-2, means both execute a real rollback AND both post a
// Slack alert for what is actually one underlying trip event. SETNX makes
// the claim itself atomic; the caller must still call SetState afterward
// to make the OPEN state visible to GetState for the full 10-minute
// cooldown.
//
// On a Redis error, this fails OPEN (returns true): TryTrip exists to
// deduplicate a trip across replicas, not to gate whether tripping is
// allowed at all -- refusing to trip because the coordination mechanism
// itself is unavailable would silently disable the entire auto-rollback
// safety net during a Redis outage, which is strictly worse than an
// occasional duplicate (idempotent kill-switch call, duplicate but
// harmless Slack alert).
func (b *Breaker) TryTrip(ctx context.Context, flagKey, env string) bool {
	ok, err := b.rdb.SetNX(ctx, tripLockKey(flagKey, env), "1", tripLockTTL).Result()
	if err != nil {
		return true
	}
	return ok
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
