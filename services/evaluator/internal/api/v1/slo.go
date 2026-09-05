package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/evaluator/internal/circuit"
)

const (
	defaultSLOThreshold = 0.05 // 5% error rate = SLO breach
	hoursPerDay         = 24
	defaultWindowDays   = 7
	maxWindowDays       = 90
	historyHoursMax     = 168 // 7 days * 24 hours
)

// SLOHistoryPoint is a single hourly data point for the SLO history.
type SLOHistoryPoint struct {
	Ts           string  `json:"ts"`
	ErrorRate    float64 `json:"error_rate"`
	CircuitState string  `json:"circuit_state"`
}

// SLOResponse is the response body for GET /api/v1/flags/{key}/slo.
type SLOResponse struct {
	FlagKey            string            `json:"flag_key"`
	Window             string            `json:"window"`
	ErrorRate          float64           `json:"error_rate"`
	P99LatencyMs       float64           `json:"p99_latency_ms"`
	EvaluationCount    int64             `json:"evaluation_count"`
	CircuitTrips       int64             `json:"circuit_trips"`
	SLOBudgetRemaining float64           `json:"slo_budget_remaining"`
	CircuitState       string            `json:"circuit_state"`
	History            []SLOHistoryPoint `json:"history"`
}

// Handler holds dependencies for the SLO handler.
type Handler struct {
	rdb     *redis.Client
	breaker *circuit.Breaker
	logger  *zap.Logger
}

// NewHandler constructs an SLO handler.
func NewHandler(rdb *redis.Client, breaker *circuit.Breaker, logger *zap.Logger) *Handler {
	return &Handler{rdb: rdb, breaker: breaker, logger: logger}
}

// HandleFlagSLO serves GET /api/v1/flags/{key}/slo?window=7d
func (h *Handler) HandleFlagSLO(w http.ResponseWriter, r *http.Request) {
	flagKey := chi.URLParam(r, "key")
	if flagKey == "" {
		http.Error(w, `{"error":"missing flag key"}`, http.StatusBadRequest)
		return
	}

	windowDays := defaultWindowDays
	if qs := r.URL.Query().Get("window"); qs != "" {
		// Accept formats: "7d", "30d", "90d"
		if len(qs) > 1 && qs[len(qs)-1] == 'd' {
			if n, err := strconv.Atoi(qs[:len(qs)-1]); err == nil && n > 0 {
				if n > maxWindowDays {
					n = maxWindowDays
				}
				windowDays = n
			}
		}
	}

	ctx := r.Context()

	// Circuit state is environment-scoped; select via ?environment= (default production).
	env := r.URL.Query().Get("environment")
	if env == "" {
		env = "production"
	}

	circuitState := h.breaker.GetState(ctx, flagKey, env)
	windowLabel := fmt.Sprintf("%dd", windowDays)
	windowHours := windowDays * hoursPerDay

	totalCount, errorCount, p99, err := h.aggregateTelemetry(ctx, flagKey, env, windowHours)
	if err != nil {
		h.logger.Warn("failed to aggregate telemetry", zap.String("flag", flagKey), zap.Error(err))
	}

	errorRate := 0.0
	if totalCount > 0 {
		errorRate = float64(errorCount) / float64(totalCount)
	}
	errorRate = roundFloat(errorRate, 6)

	// SLO budget remaining: 1 - (error_rate / slo_threshold), clamped to [0, 1]
	budgetRemaining := 1.0
	if defaultSLOThreshold > 0 {
		budgetRemaining = 1.0 - (errorRate / defaultSLOThreshold)
		if budgetRemaining < 0 {
			budgetRemaining = 0
		}
		if budgetRemaining > 1 {
			budgetRemaining = 1
		}
	}
	budgetRemaining = roundFloat(budgetRemaining, 4)

	circuitTrips, err := h.countCircuitTrips(ctx, flagKey, env, windowHours)
	if err != nil {
		h.logger.Warn("failed to count circuit trips", zap.String("flag", flagKey), zap.Error(err))
	}
	history := h.buildHistory(ctx, flagKey, env, windowHours)

	resp := SLOResponse{
		FlagKey:            flagKey,
		Window:             windowLabel,
		ErrorRate:          errorRate,
		P99LatencyMs:       p99,
		EvaluationCount:    totalCount,
		CircuitTrips:       circuitTrips,
		SLOBudgetRemaining: budgetRemaining,
		CircuitState:       string(circuitState),
		History:            history,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Error("encode slo response", zap.Error(err))
	}
}

// aggregateTelemetry reads windowed telemetry buckets from Redis and returns
// aggregate totals + estimated p99 latency.
//
// Redis key convention (written by the aggregator flush loop), scoped by
// environment for the same reason circuit.Breaker's stateKey is (EVAL-1):
// without it, staging and production telemetry for the same flag key would
// collide in the same hourly bucket. Both components are percent-encoded
// via circuit.EscapeKeyComponent -- without it, a colon inside flagKey OR
// env could make two DIFFERENT (flagKey, env) pairs format to the
// IDENTICAL key (the same bug class INT-2 fixed in depgraph_key; found by
// adversarial review of this same PR).
//
//	telemetry:{flagKey}:{env}:hour:{unix_hour}  → JSON: {"total":N,"errors":E,"p99_ms":F}
//
// The aggregator does not yet write these keys — we read them with graceful
// fallback so the endpoint returns zeroes rather than 500.
func (h *Handler) aggregateTelemetry(ctx context.Context, flagKey, env string, hours int) (totalCount, errorCount int64, p99 float64, err error) {
	now := time.Now().UTC()
	var maxP99 float64
	ek, ee := circuit.EscapeKeyComponent(flagKey), circuit.EscapeKeyComponent(env)

	for i := 0; i < hours; i++ {
		hour := now.Add(-time.Duration(i) * time.Hour).Truncate(time.Hour)
		unixHour := hour.Unix() / 3600
		key := fmt.Sprintf("telemetry:%s:%s:hour:%d", ek, ee, unixHour)

		val, redisErr := h.rdb.Get(ctx, key).Result()
		if redisErr == redis.Nil {
			continue
		}
		if redisErr != nil {
			err = redisErr
			continue
		}

		var bucket struct {
			Total  int64   `json:"total"`
			Errors int64   `json:"errors"`
			P99Ms  float64 `json:"p99_ms"`
		}
		if jsonErr := json.Unmarshal([]byte(val), &bucket); jsonErr != nil {
			continue
		}
		totalCount += bucket.Total
		errorCount += bucket.Errors
		if bucket.P99Ms > maxP99 {
			maxP99 = bucket.P99Ms
		}
	}

	return totalCount, errorCount, roundFloat(maxP99, 2), err
}

// countCircuitTrips reads circuit trip events recorded in Redis.
//
// Redis key convention, env-scoped and escaped for the same reason as
// aggregateTelemetry above:
//
//	circuit:{flagKey}:{env}:trips:{unix_hour} → integer count of trips in that hour
//
// On a genuine (non-Nil) Redis error for one hour, continues aggregating
// the remaining hours rather than aborting the whole window -- matching
// aggregateTelemetry's best-effort behavior (a partial SLO answer is more
// useful than none) instead of the prior inconsistency where this function
// discarded every other hour's data on the first error while
// aggregateTelemetry kept going.
func (h *Handler) countCircuitTrips(ctx context.Context, flagKey, env string, hours int) (total int64, err error) {
	now := time.Now().UTC()
	ek, ee := circuit.EscapeKeyComponent(flagKey), circuit.EscapeKeyComponent(env)

	for i := 0; i < hours; i++ {
		hour := now.Add(-time.Duration(i) * time.Hour).Truncate(time.Hour)
		unixHour := hour.Unix() / 3600
		key := fmt.Sprintf("circuit:%s:%s:trips:%d", ek, ee, unixHour)

		n, redisErr := h.rdb.Get(ctx, key).Int64()
		if redisErr == redis.Nil {
			continue
		}
		if redisErr != nil {
			err = redisErr
			continue
		}
		total += n
	}
	return total, err
}

// buildHistory returns at most historyHoursMax hourly data points (oldest first).
// Hours with no Redis data are filled with zero error rate + current circuit state.
func (h *Handler) buildHistory(ctx context.Context, flagKey, env string, hours int) []SLOHistoryPoint {
	if hours > historyHoursMax {
		hours = historyHoursMax
	}

	now := time.Now().UTC()
	points := make([]SLOHistoryPoint, 0, hours)
	ek, ee := circuit.EscapeKeyComponent(flagKey), circuit.EscapeKeyComponent(env)

	for i := hours - 1; i >= 0; i-- {
		hour := now.Add(-time.Duration(i) * time.Hour).Truncate(time.Hour)
		unixHour := hour.Unix() / 3600

		errorRate := 0.0
		telKey := fmt.Sprintf("telemetry:%s:%s:hour:%d", ek, ee, unixHour)
		if val, err := h.rdb.Get(ctx, telKey).Result(); err == nil {
			var bucket struct {
				Total  int64 `json:"total"`
				Errors int64 `json:"errors"`
			}
			if json.Unmarshal([]byte(val), &bucket) == nil && bucket.Total > 0 {
				errorRate = roundFloat(float64(bucket.Errors)/float64(bucket.Total), 6)
			}
		}

		// Determine circuit state for this hour from snapshot key.
		// Falls back to current state when no snapshot is available.
		cStateKey := fmt.Sprintf("circuit:%s:%s:state:%d", ek, ee, unixHour)
		cStateVal, err := h.rdb.Get(ctx, cStateKey).Result()
		if err != nil || cStateVal == "" {
			cStateVal = string(h.breaker.GetState(ctx, flagKey, env))
		}

		points = append(points, SLOHistoryPoint{
			Ts:           hour.Format(time.RFC3339),
			ErrorRate:    errorRate,
			CircuitState: cStateVal,
		})
	}
	return points
}

func roundFloat(v float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return math.Round(v*pow) / pow
}
