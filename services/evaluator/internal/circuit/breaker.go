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
	// EVAL-3: a per-hour snapshot, read by services/evaluator/internal/
	// api/v1/slo.go's buildHistory to render "what was the circuit state
	// during THIS hour" in the SLO history graph -- that reader has always
	// expected this exact key shape (circuit:{flag}:{env}:state:{unix_hour}),
	// but nothing ever wrote it (found alongside the identical gap for the
	// telemetry:{flag}:{env}:hour:{unix_hour} bucket blast radius also
	// depends on). Written here, not by each SetState caller individually,
	// so every transition Flush drives gets one for free; multiple
	// transitions within the same hour just overwrite this hour's value
	// (last-write-wins), which is the right granularity for an hourly
	// history point.
	unixHour := time.Now().UTC().Truncate(time.Hour).Unix() / 3600
	_ = b.rdb.Set(ctx, stateSnapshotKey(flagKey, env, unixHour), string(state), TelemetryRetention).Err()
	b.logger.Info("circuit breaker state change",
		zap.String("flag", flagKey), zap.String("env", env), zap.String("state", string(state)))
}

// stateSnapshotKey builds the Redis key for SetState's per-hour state
// snapshot (see its own doc comment above for why this exists).
func stateSnapshotKey(flagKey, env string, unixHour int64) string {
	return "circuit:" + EscapeKeyComponent(flagKey) + ":" + EscapeKeyComponent(env) + ":state:" + strconv.FormatInt(unixHour, 10)
}

// TelemetryRetention bounds how long hourly telemetry/circuit-trip/state-
// snapshot buckets are kept -- generously longer than slo.go's own
// maxWindowDays (90d) so the longest window it can ever request is never
// missing data purely because this TTL expired first. Exported so
// aggregator.go's own telemetry/trip-counter bucket writes use the
// identical retention rather than a second, independently-drifting value.
const TelemetryRetention = 91 * 24 * time.Hour

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
// forever. Losing the key mid-ladder is still a real, if rare, event
// (transient Redis error on SetStep's own write, individual-key eviction
// under memory pressure, or simply this TTL elapsing on an unusually long
// incident) -- GetStep's found=false return lets its two callers
// (aggregator.go's handleOpen/handleHalfOpen) apply a context-appropriate,
// SAFE recovery instead of a single baked-in default: handleOpen assumes
// "already fully killed" (0), never "nothing stepped down yet" (100),
// because assuming less descent than may have already happened could
// re-request an INCREASE that flag-api's RollbackFlagEnvironment CAS guard
// would then correctly reject, permanently stalling the ladder; handleOpen
// then also re-arms the ObservationWindow cooldown from now rather than
// guessing it already elapsed. handleHalfOpen assumes the recovery
// ladder's floor (0, so NextRecoveryStep resumes at the safe first rung),
// never its ceiling (100), because assuming more progress than may have
// actually happened could skip real verification rungs entirely (found by
// adversarial review of PR #221).
const stepTTL = 24 * time.Hour

// GetStep returns the last rollout percentage this breaker itself set for
// flagKey/env via the stepped ladder, and whether that value is actually
// on record (false means CLOSED, key expired, or a transient Redis error --
// callers must not treat a bare int as trustworthy without checking this).
func (b *Breaker) GetStep(ctx context.Context, flagKey, env string) (step int, found bool) {
	val, err := b.rdb.Get(ctx, stepKey(flagKey, env)).Result()
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, false
	}
	return n, true
}

// SetStep records the rollout percentage this breaker just set.
func (b *Breaker) SetStep(ctx context.Context, flagKey, env string, pct int) {
	_ = b.rdb.Set(ctx, stepKey(flagKey, env), strconv.Itoa(pct), stepTTL).Err()
}

// stepClaimKey builds the Redis key for TryStep's per-transition claim
// guard -- TryTrip only deduplicates the INITIAL trip across racing
// evaluator replicas; every LATER step-down/step-up transition had no
// equivalent claim (found by adversarial review of PR #221), so two
// replicas both observing the same OPEN/HALF_OPEN position for the same
// flag+env could each independently call OnRolloutChange and commit
// SetStep for the same target, each producing its own audit_log row, SSE
// broadcast, and (at the terminal 0% step) Slack alert for what is really
// one transition. Scoped by (target step, direction), not just flag+env+
// target: rollbackSteps and recoverySteps overlap in value (25 and 50 each
// appear in both ladders), so a claim keyed on the bare target alone would
// let the descent's own claim for 25 spuriously block the recovery
// ladder's later, entirely unrelated claim for the SAME number reached
// from the opposite direction -- caught empirically by
// TestEndToEndDescentThenRecoveryCycle before this fix, which is exactly
// why that test exists.
func stepClaimKey(flagKey, env, direction string, targetStep int) string {
	return "circuit:" + EscapeKeyComponent(flagKey) + ":" + EscapeKeyComponent(env) + ":step-claim:" + direction + ":" + strconv.Itoa(targetStep)
}

// stepClaimTTL only needs to outlast one Flush tick interval (10s) -- long
// enough that two replicas ticking at roughly the same cadence collide on
// the SAME claim window, short enough that a claim from an earlier,
// unrelated pass through this exact target (e.g. a HALF_OPEN probe that
// reverted and later climbs back through the same rung) never blocks a
// later, legitimate attempt.
const stepClaimTTL = 15 * time.Second

// TryStep atomically claims the right to apply targetStep, in the given
// direction ("down" for the rollback ladder, "up" for the recovery
// ladder), for flagKey/env -- returning true for exactly one caller among
// any that race it for the same (flagKey, env, direction, targetStep)
// quadruple. Mirrors TryTrip's own SETNX technique and fail-open-on-
// Redis-error posture (see TryTrip's doc comment for the full reasoning --
// refusing to act because the dedup mechanism itself is down would be
// strictly worse than an occasional harmless duplicate).
func (b *Breaker) TryStep(ctx context.Context, flagKey, env, direction string, targetStep int) bool {
	ok, err := b.rdb.SetNX(ctx, stepClaimKey(flagKey, env, direction, targetStep), "1", stepClaimTTL).Result()
	if err != nil {
		return true
	}
	return ok
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
