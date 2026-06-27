package telemetry

import (
	"context"
	"sync"
	"time"

	"github.com/sairam0424/Tombstone/services/evaluator/internal/circuit"
	"github.com/redis/go-redis/v9"
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

// Aggregator accumulates SDK telemetry and fires circuit breaker callbacks.
type Aggregator struct {
	mu      sync.Mutex
	windows map[string]*windowState // flag_key:environment -> counts
	breaker *circuit.Breaker
	rdb     *redis.Client
	logger  *zap.Logger
	OnTrip  func(flagKey, environment string, errorRate float64) // called when circuit trips
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

// Flush evaluates all current windows, checks circuit breaker thresholds,
// fires OnTrip callbacks, then resets counters.
// Should be called on a ticker (every 10 seconds).
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

		state := a.breaker.GetState(ctx, flagKey)
		if state == circuit.StateClosed && a.breaker.ShouldTrip(win) {
			a.breaker.SetState(ctx, flagKey, circuit.StateOpen, 10*time.Minute)
			if a.OnTrip != nil {
				errorRate := a.breaker.ErrorRate(win)
				a.logger.Warn("circuit breaker tripped",
					zap.String("flag", flagKey),
					zap.String("env", env),
					zap.Float64("error_rate", errorRate))
				a.OnTrip(flagKey, env, errorRate)
			}
		}
	}
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
