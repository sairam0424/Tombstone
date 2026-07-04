// Package middleware: loadshed.go wraps failsafe-go's AdaptiveLimiter (a
// modified TCP-Vegas concurrency-limiting algorithm, the same family as
// Netflix's concurrency-limits and Uber's Cinnamon auto-tuner) around request
// handling as a NEW, SEPARATE layer of defense from rate limiting.
//
// Rate limiting (ratelimit.go, Phase 5) rejects callers who have exceeded
// THEIR OWN quota, regardless of overall service health. Load shedding here
// instead reacts to the SERVICE ITSELF being saturated — e.g. the Postgres
// connection pool (deliberately capped at SetMaxOpenConns(5) for the Neon
// free tier) being exhausted — REGARDLESS of any individual caller's quota
// standing. Today, if that pool is exhausted, requests just block waiting for
// a connection instead of being proactively rejected; under sustained load
// that queues requests indefinitely and produces exactly the kind of
// cascading, slow-to-notice outage this middleware exists to prevent.
//
// The AdaptiveLimiter tracks recent request execution times against a
// weighted moving average baseline: when recent latencies trend up relative
// to baseline, it concludes the service is more loaded and shrinks its
// concurrency limit; when latencies trend down, it grows the limit back up.
// Requests in excess of the current limit are rejected immediately with a
// 503 rather than being queued or allowed to pile up behind a saturated
// resource.
//
// This mirrors the failsafe-go usage conventions established in Phase 2's
// httpclient/resilient_client.go (per-call-site config struct with a
// DefaultConfig(), a builder chain, and zap logging on state transitions),
// duplicated per-service rather than factored into a shared module, matching
// this repo's existing convention (see otel.go, duplicated byte-for-byte
// across services).
package middleware

import (
	"net/http"
	"strconv"

	"github.com/failsafe-go/failsafe-go/adaptivelimiter"
	"go.uber.org/zap"
)

// loadShedRetryAfterSeconds is the static Retry-After value returned to shed
// requests. The adaptive limiter re-evaluates its limit continuously, so a
// short fixed wait is a reasonable default hint — failsafe-go's adaptive
// limiter does not expose a per-rejection suggested-wait value the way a
// token-bucket rate limiter's Reserve().Delay() does.
const loadShedRetryAfterSeconds = 5

// LoadShedConfig is per-call-site, mirroring httpclient.ResilientClientConfig's
// shape from Phase 2.
type LoadShedConfig struct {
	MinLimit     uint // floor on the concurrency limit, e.g. 1
	MaxLimit     uint // ceiling on the concurrency limit, e.g. 200
	InitialLimit uint // starting concurrency limit, e.g. 20
}

// DefaultLoadShedConfig returns sane defaults matching failsafe-go's own
// AdaptiveLimiter defaults (min 1, max 200, initial 20), which the library
// documents as fitting typical single-instance service capacity discovery.
func DefaultLoadShedConfig() LoadShedConfig {
	return LoadShedConfig{
		MinLimit:     1,
		MaxLimit:     200,
		InitialLimit: 20,
	}
}

// LoadShedMiddleware wraps an adaptivelimiter.AdaptiveLimiter as HTTP
// middleware. One instance should be constructed per service and reused
// across all requests so the limiter's recent/baseline latency windows
// accumulate correctly.
type LoadShedMiddleware struct {
	limiter adaptivelimiter.AdaptiveLimiter[any]
	logger  *zap.Logger
}

// NewLoadShedMiddleware builds a LoadShedMiddleware from cfg. If logger is
// nil, a no-op logger is used.
func NewLoadShedMiddleware(cfg LoadShedConfig, logger *zap.Logger) *LoadShedMiddleware {
	if logger == nil {
		logger = zap.NewNop()
	}

	limiter := adaptivelimiter.NewBuilder[any]().
		WithLimits(cfg.MinLimit, cfg.MaxLimit, cfg.InitialLimit).
		OnLimitChanged(func(e adaptivelimiter.LimitChangedEvent) {
			logger.Info("load shed: concurrency limit adjusted",
				zap.Uint("old_limit", e.OldLimit),
				zap.Uint("new_limit", e.NewLimit))
		}).
		Build()

	return &LoadShedMiddleware{limiter: limiter, logger: logger}
}

// LoadShed returns an http.Handler middleware. Exempt path: /health (exact
// match) — health checks must never be shed, since a shed health check would
// make an overloaded-but-still-serving instance look fully dead to its
// orchestrator.
//
// On acquiring a permit: calls next.ServeHTTP, then reports the observed
// latency back to the limiter via Permit.Record() so it can keep adapting.
// On failing to acquire a permit (service judged overloaded): responds
// immediately with 503 + Retry-After, WITHOUT calling next.ServeHTTP.
func (m *LoadShedMiddleware) LoadShed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		permit, ok := m.limiter.TryAcquirePermit()
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(loadShedRetryAfterSeconds))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"service overloaded","retry_after":` + strconv.Itoa(loadShedRetryAfterSeconds) + `}`))
			m.logger.Warn("load shed: rejected request — service overloaded",
				zap.String("path", r.URL.Path),
				zap.Int("limit", m.limiter.Limit()),
				zap.Int("inflight", m.limiter.Inflight()))
			return
		}

		// Always release the permit back to the limiter exactly once, even if
		// next.ServeHTTP panics further down the chain — a leaked permit would
		// permanently shrink effective capacity by one slot. A panic's
		// "latency" is not a meaningful load signal, so on panic we Drop()
		// (excluded from adaptation) rather than Record() (included), then
		// re-panic so the outer chiMiddleware.Recoverer still converts it
		// into a 500 response.
		defer func() {
			if rec := recover(); rec != nil {
				permit.Drop()
				panic(rec)
			}
		}()

		next.ServeHTTP(w, r)
		permit.Record()
	})
}
