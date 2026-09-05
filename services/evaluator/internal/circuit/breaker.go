package circuit

import (
	"context"
	"net/url"
	"strconv"
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

// RolloutPhase describes WHY Aggregator.Flush is asking a caller to change
// a flag's rollout percentage -- lets a caller (e.g. main.go's Slack
// alerting) react differently to "an incident just started" versus "still
// walking the ladder down" without re-deriving that from targetPct alone.
type RolloutPhase string

const (
	PhaseTripped                RolloutPhase = "TRIPPED"                  // first step down from CLOSED
	PhaseStepped                RolloutPhase = "STEPPED"                  // further step down, not yet at 0%
	PhaseKilled                 RolloutPhase = "KILLED"                   // reached the ladder's terminal 0%
	PhaseRecovering             RolloutPhase = "RECOVERING"               // HALF_OPEN, stepping up
	PhaseRecovered              RolloutPhase = "RECOVERED"                // HALF_OPEN reached 100%, back to CLOSED
	PhaseRevertedDuringRecovery RolloutPhase = "REVERTED_DURING_RECOVERY" // HALF_OPEN probe failed, back to OPEN/0%
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

// ReleaseTrip deletes the trip-lock claim early, for use when the caller's
// own follow-up action (e.g. SetState) fails after a successful TryTrip.
// Without this, a failed follow-up leaves the lock held for the FULL
// tripLockTTL even though the state it was meant to gate was never
// actually written -- stalling every subsequent Flush tick's retry for up
// to that long, worse than the occasional harmless duplicate TryTrip
// exists to prevent (found by adversarial review of PR #219).
func (b *Breaker) ReleaseTrip(ctx context.Context, flagKey, env string) {
	_ = b.rdb.Del(ctx, tripLockKey(flagKey, env)).Err()
}

// SetState updates the circuit state for a flag in an environment in Redis.
func (b *Breaker) SetState(ctx context.Context, flagKey, env string, state State, ttl time.Duration) {
	_ = b.rdb.Set(ctx, stateKey(flagKey, env), string(state), ttl).Err()
	b.logger.Info("circuit breaker state change",
		zap.String("flag", flagKey), zap.String("env", env), zap.String("state", string(state)))
}

// rollbackSteps is the ladder Aggregator.Flush walks DOWN once tripped,
// each step a lower rollout percentage than the last (100% is implicit --
// nothing has been stepped down yet). Deterministic and unconditional:
// once ShouldTrip has fired, every subsequent tick advances to the next
// step regardless of that tick's own error rate, all the way to 0% --
// this is a safety-critical kill path, not a place to second-guess a
// single good-looking window mid-descent. recoverySteps is the mirror
// ladder Flush walks UP during HALF_OPEN, WITH per-step verification
// (see NextRecoveryStep's caller in aggregator.go) -- unlike the descent,
// each step up is gated on that step's own error rate looking healthy.
var rollbackSteps = []int{50, 25, 0}
var recoverySteps = []int{10, 25, 50, 100}

// NextRollbackStep returns the next lower percentage in rollbackSteps
// given the current one (100 if nothing has been stepped down from yet).
// done reports whether next is the ladder's terminal 0% step.
func NextRollbackStep(current int) (next int, done bool) {
	for _, s := range rollbackSteps {
		if s < current {
			return s, s == 0
		}
	}
	return 0, true
}

// NextRecoveryStep returns the next higher percentage in recoverySteps
// given the current probe percentage. recovered reports whether next is
// the ladder's terminal 100% step (fully restored -- the caller should
// transition back to CLOSED rather than staying HALF_OPEN at 100%).
func NextRecoveryStep(current int) (next int, recovered bool) {
	for _, s := range recoverySteps {
		if s > current {
			return s, s == 100
		}
	}
	return 100, true
}

// stepKey builds the Redis key tracking the last rollout percentage this
// breaker itself set via the stepped ladder (either direction) -- distinct
// from stateKey's own CLOSED/OPEN/HALF_OPEN value, since a single state can
// span several distinct percentages over its lifetime.
func stepKey(flagKey, env string) string {
	return "circuit:" + EscapeKeyComponent(flagKey) + ":" + EscapeKeyComponent(env) + ":step"
}

// openedAtKey builds the Redis key recording when a flag last reached the
// rollback ladder's terminal 0% step -- the moment ObservationWindow's
// cooldown starts counting down before Flush attempts a HALF_OPEN recovery
// probe. Reset (re-set to now) every time a HALF_OPEN probe reverts back to
// OPEN, so a flag that keeps failing recovery never probes more often than
// once per ObservationWindow.
func openedAtKey(flagKey, env string) string {
	return "circuit:" + EscapeKeyComponent(flagKey) + ":" + EscapeKeyComponent(env) + ":opened-at"
}

// stepTTL bounds both stepKey and openedAtKey -- generously longer than any
// plausible full descent-cooldown-recovery cycle, so a crashed evaluator
// replica's abandoned mid-ladder state self-heals rather than persisting
// forever. GetStep/GetOpenedAt's own "not found" defaults (100 and "not
// set", respectively) are exactly the right values to resume a fresh
// CLOSED-state assessment from, so an expired key is never a correctness
// problem -- only ever a (harmless) loss of ladder-position memory.
const stepTTL = 24 * time.Hour

// GetStep returns the last rollout percentage this breaker itself set for
// flagKey/env via the stepped ladder, or 100 if none is recorded (CLOSED,
// or the key expired) -- 100 is the correct "nothing stepped down yet"
// starting point for NextRollbackStep.
func (b *Breaker) GetStep(ctx context.Context, flagKey, env string) int {
	val, err := b.rdb.Get(ctx, stepKey(flagKey, env)).Result()
	if err != nil {
		return 100
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 100
	}
	return n
}

// SetStep records the rollout percentage this breaker just set.
func (b *Breaker) SetStep(ctx context.Context, flagKey, env string, pct int) {
	_ = b.rdb.Set(ctx, stepKey(flagKey, env), strconv.Itoa(pct), stepTTL).Err()
}

// GetOpenedAt returns when flagKey/env last reached the rollback ladder's
// terminal 0% step, and whether that time is actually recorded (false
// means "never reached 0%, or the record expired" -- the caller must not
// attempt a HALF_OPEN transition without a real timestamp to measure
// ObservationWindow against).
func (b *Breaker) GetOpenedAt(ctx context.Context, flagKey, env string) (time.Time, bool) {
	val, err := b.rdb.Get(ctx, openedAtKey(flagKey, env)).Result()
	if err != nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

// SetOpenedAt records t as the moment flagKey/env reached (or re-reached,
// after a failed recovery probe) the rollback ladder's terminal 0% step.
func (b *Breaker) SetOpenedAt(ctx context.Context, flagKey, env string, t time.Time) {
	_ = b.rdb.Set(ctx, openedAtKey(flagKey, env), strconv.FormatInt(t.Unix(), 10), stepTTL).Err()
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
