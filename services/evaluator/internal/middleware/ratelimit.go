package middleware

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// telemetryRatePerMin is the sustained rate for /api/v1/telemetry (req/min).
	telemetryRatePerMin = 5000
	// telemetryBurst is the burst capacity for the telemetry endpoint.
	telemetryBurst = 200
	// defaultRatePerMin is the rate for all other routes (req/min).
	defaultRatePerMin = 200
	// defaultBurst is the burst capacity for non-telemetry routes.
	defaultBurst = 20
	// staleTTL is how long after last-seen before an entry is evicted.
	staleTTL = 10 * time.Minute
	// cleanupInterval is the period between eviction passes.
	cleanupInterval = 5 * time.Minute

	// telemetryPath is the high-throughput ingest endpoint.
	telemetryPath = "/api/v1/telemetry"
)

// entry holds a limiter together with the last time it was used.
type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimitMiddleware enforces per-IP rate limits on the evaluator service.
// The telemetry ingest endpoint has a higher limit than all other routes.
type RateLimitMiddleware struct {
	// Two separate stores because telemetry and non-telemetry paths have
	// different bucket sizes; keys are client IPs.
	telemetryLimiters sync.Map
	defaultLimiters   sync.Map
	done              chan struct{}
}

// NewRateLimitMiddleware constructs the middleware and starts the background
// stale-entry cleaner.  Call Stop() during shutdown.
func NewRateLimitMiddleware() *RateLimitMiddleware {
	m := &RateLimitMiddleware{done: make(chan struct{})}
	go m.cleanLoop()
	return m
}

// Stop signals the background cleaner to exit.
func (m *RateLimitMiddleware) Stop() {
	close(m.done)
}

// RateLimit returns an http.Handler middleware that applies route-specific limits.
// Exempt path: /health (exact match).
// On limit: HTTP 429 + Retry-After header.
// Fail-open: panics are logged and the request is passed through.
func (m *RateLimitMiddleware) RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("evaluator ratelimit: recovered panic: %v", rec)
				next.ServeHTTP(w, r)
			}
		}()

		// Health check is always exempt.
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		ip := extractIP(r)

		var lim *rate.Limiter
		if r.URL.Path == telemetryPath {
			lim = m.getLimiter(&m.telemetryLimiters, ip, telemetryRatePerMin, telemetryBurst)
		} else {
			lim = m.getLimiter(&m.defaultLimiters, ip, defaultRatePerMin, defaultBurst)
		}

		if !lim.Allow() {
			retryAfter := retryAfterSeconds(lim)
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprintf(w, `{"error":"rate limit exceeded","retry_after":%d}`, retryAfter)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// getLimiter retrieves or creates a per-key limiter from store.
func (m *RateLimitMiddleware) getLimiter(store *sync.Map, key string, ratePerMin float64, burst int) *rate.Limiter {
	now := time.Now()
	r := rate.Limit(ratePerMin / 60.0)

	if v, ok := store.Load(key); ok {
		e := v.(*entry)
		e.lastSeen = now
		return e.limiter
	}

	e := &entry{
		limiter:  rate.NewLimiter(r, burst),
		lastSeen: now,
	}
	actual, _ := store.LoadOrStore(key, e)
	return actual.(*entry).limiter
}

// cleanLoop evicts stale entries every cleanupInterval.
func (m *RateLimitMiddleware) cleanLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.evict()
		case <-m.done:
			return
		}
	}
}

func (m *RateLimitMiddleware) evict() {
	cutoff := time.Now().Add(-staleTTL)
	for _, store := range []*sync.Map{&m.telemetryLimiters, &m.defaultLimiters} {
		store.Range(func(k, v any) bool {
			if v.(*entry).lastSeen.Before(cutoff) {
				store.Delete(k)
			}
			return true
		})
	}
}

// extractIP returns the most-specific client IP from the request.
func extractIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if idx := strings.Index(fwd, ","); idx != -1 {
			return strings.TrimSpace(fwd[:idx])
		}
		return strings.TrimSpace(fwd)
	}
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

// retryAfterSeconds calculates how many seconds until the next token is
// available without consuming one.
func retryAfterSeconds(lim *rate.Limiter) int {
	res := lim.Reserve()
	d := res.Delay()
	res.Cancel()
	secs := int(math.Ceil(d.Seconds()))
	if secs < 1 {
		return 1
	}
	return secs
}
