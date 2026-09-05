package telemetry

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/tombstone/evaluator/internal/circuit"
	"go.uber.org/zap"
)

// TelemetryEvent is a single evaluation result emitted by an SDK.
type TelemetryEvent struct {
	FlagKey     string    `json:"flag_key"`
	Environment string    `json:"environment"`
	IsError     bool      `json:"is_error"`
	Ts          time.Time `json:"ts"`
}

// windowState holds counters for the current 10s window.
type windowState struct {
	errors int64
	total  int64
}

// Aggregator accumulates SDK telemetry and drives the EVAL-4 circuit-breaker
// state machine: CLOSED -> (trip) -> OPEN, stepping down 100->50->25->0 one
// step per tick, then (after ObservationWindow's cooldown) -> HALF_OPEN,
// stepping up 10->25->50->100 with per-step verification, reverting to
// OPEN/0% immediately if a probe step's own error rate is still bad.
//
// The state-machine advancement above only runs for a flag+environment
// that has fresh telemetry in the CURRENT tick's snapshot -- a flag+env
// with zero events this tick is left exactly where it was. This is a
// deliberate scope choice, not an oversight: Record() is called from every
// real evaluate() call regardless of the flag's current enabled/rollout
// state (see @tombstone/core's TombstoneClient), so as long as the
// application keeps checking the flag on real traffic, telemetry keeps
// flowing even while it's disabled -- exactly the traffic needed to
// eventually verify a HALF_OPEN recovery probe. The only case this
// doesn't cover is a flag+env whose check site stops receiving traffic
// entirely while tripped, in which case never auto-advancing (staying at
// whatever percentage was last set) is the safer default anyway -- no
// signal to guess from.
type Aggregator struct {
	mu      sync.Mutex
	windows map[string]*windowState // flag_key:environment -> counts
	breaker *circuit.Breaker
	rdb     *redis.Client
	logger  *zap.Logger
	// OnRolloutChange fires whenever Flush decides to change a flag's
	// rollout percentage. The callback is responsible for actually
	// applying targetPct (e.g. via rollback.Executor.Execute for
	// targetPct==0, SetRolloutPct otherwise) and returns whether it
	// succeeded -- Flush only commits the new step/state to Redis on
	// success, so a failed API call is retried next tick from the SAME
	// position rather than silently advancing the ladder past a step that
	// was never actually applied.
	OnRolloutChange func(flagKey, environment string, targetPct int, errorRate float64, phase circuit.RolloutPhase) bool
}

func NewAggregator(breaker *circuit.Breaker, rdb *redis.Client, logger *zap.Logger) *Aggregator {
	return &Aggregator{
		windows: make(map[string]*windowState),
		breaker: breaker,
		rdb:     rdb,
		logger:  logger,
	}
}

// Record ingests a single telemetry event.
func (a *Aggregator) Record(event TelemetryEvent) {
	key := event.FlagKey + ":" + event.Environment
	a.mu.Lock()
	if a.windows[key] == nil {
		a.windows[key] = &windowState{}
	}
	a.windows[key].total++
	if event.IsError {
		a.windows[key].errors++
	}
	a.mu.Unlock()
}

// stateTTL bounds circuit state (CLOSED/OPEN/HALF_OPEN) in Redis -- a
// crashed evaluator that never resumes ticking self-heals to CLOSED after
// this long rather than staying OPEN forever. It bounds only this
// coordination layer's own bookkeeping, not the actual flag state
// flag-api persists (which stays wherever it was last set regardless).
const stateTTL = 10 * time.Minute

// Flush evaluates all current windows and advances the EVAL-4 circuit-
// breaker state machine for each, then resets counters. Should be called
// on a ticker (every 10 seconds).
func (a *Aggregator) Flush(ctx context.Context) {
	a.mu.Lock()
	snapshot := make(map[string]*windowState, len(a.windows))
	for k, v := range a.windows {
		snapshot[k] = &windowState{errors: v.errors, total: v.total}
	}
	// Reset counters for next window
	a.windows = make(map[string]*windowState)
	a.mu.Unlock()

	for key, w := range snapshot {
		// Parse "flagKey:environment" from key
		flagKey, env := splitKey(key)
		win := circuit.Window{
			FlagKey:    flagKey,
			ErrorCount: w.errors,
			TotalCount: w.total,
		}

		switch a.breaker.GetState(ctx, flagKey, env) {
		case circuit.StateClosed:
			a.handleClosed(ctx, flagKey, env, win)
		case circuit.StateOpen:
			a.handleOpen(ctx, flagKey, env, win)
		case circuit.StateHalfOpen:
			a.handleHalfOpen(ctx, flagKey, env, win)
		}
	}
}

// handleClosed decides whether to trip: ShouldTrip gates on this window's
// own error rate/volume; TryTrip's SETNX then deduplicates the trip across
// racing evaluator replicas that all observed the same StateClosed window
// (multiple replicas share this Redis-backed state, so GetState-then-
// ShouldTrip is a check-then-act race without it -- see TryTrip's own doc
// comment). Only on a SUCCESSFUL first step-down does this commit
// StateOpen -- a failed callback releases the trip claim so the next tick
// retries promptly instead of waiting out the full trip-lock TTL blind
// (found by adversarial review of PR #219).
func (a *Aggregator) handleClosed(ctx context.Context, flagKey, env string, win circuit.Window) {
	if !a.breaker.ShouldTrip(win) {
		return
	}
	if !a.breaker.TryTrip(ctx, flagKey, env) {
		return
	}
	errorRate := a.breaker.ErrorRate(win)
	target, _ := circuit.NextRollbackStep(100)
	a.logger.Warn("circuit breaker tripped",
		zap.String("flag", flagKey), zap.String("env", env),
		zap.Float64("error_rate", errorRate), zap.Int("target_pct", target))
	if a.OnRolloutChange == nil || !a.OnRolloutChange(flagKey, env, target, errorRate, circuit.PhaseTripped) {
		a.breaker.ReleaseTrip(ctx, flagKey, env)
		a.logger.Error("initial rollback step failed; trip claim released for a prompt retry",
			zap.String("flag", flagKey), zap.String("env", env))
		return
	}
	a.breaker.SetState(ctx, flagKey, env, circuit.StateOpen, stateTTL)
	a.breaker.SetStep(ctx, flagKey, env, target)
}

// handleOpen drives BOTH halves of the OPEN state: while step>0, an
// unconditional, deterministic descent (this is the safety-critical kill
// path -- a single good-looking window mid-descent is not a reason to stop
// cutting exposure); once step reaches 0, waiting out ObservationWindow's
// cooldown before attempting a HALF_OPEN recovery probe.
func (a *Aggregator) handleOpen(ctx context.Context, flagKey, env string, win circuit.Window) {
	currentStep := a.breaker.GetStep(ctx, flagKey, env)
	errorRate := a.breaker.ErrorRate(win)

	if currentStep > 0 {
		target, done := circuit.NextRollbackStep(currentStep)
		phase := circuit.PhaseStepped
		if done {
			phase = circuit.PhaseKilled
		}
		if a.OnRolloutChange == nil || !a.OnRolloutChange(flagKey, env, target, errorRate, phase) {
			a.logger.Error("rollback step failed; will retry next tick",
				zap.String("flag", flagKey), zap.String("env", env), zap.Int("target_pct", target))
			return
		}
		a.breaker.SetStep(ctx, flagKey, env, target)
		if done {
			a.breaker.SetOpenedAt(ctx, flagKey, env, time.Now())
		}
		return
	}

	openedAt, ok := a.breaker.GetOpenedAt(ctx, flagKey, env)
	if !ok {
		// Reached 0% before this bookkeeping key existed, or it expired --
		// start the cooldown clock now rather than assuming it already
		// elapsed (which would let a HALF_OPEN probe fire immediately).
		a.breaker.SetOpenedAt(ctx, flagKey, env, time.Now())
		return
	}
	if time.Since(openedAt) < a.breaker.ObservationWindow {
		return
	}
	target, _ := circuit.NextRecoveryStep(0)
	if a.OnRolloutChange == nil || !a.OnRolloutChange(flagKey, env, target, errorRate, circuit.PhaseRecovering) {
		a.logger.Error("HALF_OPEN recovery probe failed to start; will retry next tick",
			zap.String("flag", flagKey), zap.String("env", env))
		return
	}
	a.breaker.SetState(ctx, flagKey, env, circuit.StateHalfOpen, stateTTL)
	a.breaker.SetStep(ctx, flagKey, env, target)
}

// handleHalfOpen is where "post-rollback error-rate verification" actually
// happens: ShouldTrip on THIS probe step's own window decides whether to
// keep climbing the recovery ladder, revert immediately, or -- when there
// isn't yet enough traffic at this probe level to trust either way (below
// Breaker.MinRequests) -- hold at the current step and wait for more data
// rather than guessing.
func (a *Aggregator) handleHalfOpen(ctx context.Context, flagKey, env string, win circuit.Window) {
	currentStep := a.breaker.GetStep(ctx, flagKey, env)
	errorRate := a.breaker.ErrorRate(win)

	if a.breaker.ShouldTrip(win) {
		if a.OnRolloutChange == nil || !a.OnRolloutChange(flagKey, env, 0, errorRate, circuit.PhaseRevertedDuringRecovery) {
			a.logger.Error("failed to revert a failed recovery probe; will retry next tick",
				zap.String("flag", flagKey), zap.String("env", env))
			return
		}
		a.breaker.SetState(ctx, flagKey, env, circuit.StateOpen, stateTTL)
		a.breaker.SetStep(ctx, flagKey, env, 0)
		a.breaker.SetOpenedAt(ctx, flagKey, env, time.Now())
		return
	}

	if win.TotalCount < a.breaker.MinRequests {
		return
	}

	target, recovered := circuit.NextRecoveryStep(currentStep)
	phase := circuit.PhaseRecovering
	if recovered {
		phase = circuit.PhaseRecovered
	}
	if a.OnRolloutChange == nil || !a.OnRolloutChange(flagKey, env, target, errorRate, phase) {
		a.logger.Error("recovery step failed; will retry next tick",
			zap.String("flag", flagKey), zap.String("env", env), zap.Int("target_pct", target))
		return
	}
	if recovered {
		a.breaker.SetState(ctx, flagKey, env, circuit.StateClosed, stateTTL)
		return
	}
	a.breaker.SetStep(ctx, flagKey, env, target)
}

// Run starts the aggregator flush loop. Blocks until ctx is cancelled.
func (a *Aggregator) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.Flush(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func splitKey(key string) (flagKey, environment string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == ':' {
			return key[:i], key[i+1:]
		}
	}
	return key, "production"
}
