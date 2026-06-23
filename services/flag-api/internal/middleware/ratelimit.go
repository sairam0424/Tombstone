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
	// tokenRatePerMin is the sustained request rate per SDK token (requests/minute).
	tokenRatePerMin = 1000
	// tokenBurst is the burst capacity per SDK token.
	tokenBurst = 50
	// ipRatePerMin is the sustained request rate per IP fallback (requests/minute).
	ipRatePerMin = 200
	// ipBurst is the burst capacity per IP.
	ipBurst = 20
	// staleTTL is how long after last-seen before an entry is purged.
	staleTTL = 10 * time.Minute
	// cleanupInterval is how often the background cleaner runs.
	cleanupInterval = 5 * time.Minute
)

// entry holds a limiter together with the last time it was used.
type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimitMiddleware enforces per-token and per-IP rate limits using token buckets.
// Keys based on the Bearer token take priority; IP is the fallback.
type RateLimitMiddleware struct {
	tokenLimiters sync.Map // key: token string   -> *entry
	ipLimiters    sync.Map // key: IP string       -> *entry
	done          chan struct{}
}

// NewRateLimitMiddleware constructs the middleware and starts the background
// stale-entry cleaner.  Call Stop() when the process is shutting down (optional –
// the goroutine is also unblocked when done is closed).
func NewRateLimitMiddleware() *RateLimitMiddleware {
	m := &RateLimitMiddleware{done: make(chan struct{})}
	go m.cleanLoop()
	return m
}

// Stop signals the background cleaner to exit.
func (m *RateLimitMiddleware) Stop() {
	close(m.done)
}

// RateLimit returns an http.Handler middleware.
// Exempt path: /api/v1/health (exact match).
// On limit: HTTP 429 + Retry-After header.
// Fail-open: any panic is logged and the request is passed through.
func (m *RateLimitMiddleware) RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fail-open: recover from any unexpected panic so a bug here
		// never takes down the entire service.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("ratelimit: recovered panic: %v", rec)
				next.ServeHTTP(w, r)
			}
		}()

		// Exempt the health endpoint.
		if r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}

		// Determine the rate limit key.
		// Prefer the Bearer token; fall back to the client IP.
		var (
			lim     *rate.Limiter
			keyType string
		)
		if token := extractBearerToken(r); token != "" {
			lim = m.getLimiter(&m.tokenLimiters, token, tokenRatePerMin, tokenBurst)
			keyType = "token"
		} else {
			ip := extractIP(r)
			lim = m.getLimiter(&m.ipLimiters, ip, ipRatePerMin, ipBurst)
			keyType = "ip"
		}

		if !lim.Allow() {
			retryAfter := retryAfterSeconds(lim)
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":"rate limit exceeded","retry_after":%d,"key_type":%q}`,
				retryAfter, keyType)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// getLimiter retrieves an existing limiter or creates a new one for key.
func (m *RateLimitMiddleware) getLimiter(store *sync.Map, key string, ratePerMin float64, burst int) *rate.Limiter {
	now := time.Now()
	r := rate.Limit(ratePerMin / 60.0) // convert req/min → req/sec

	if v, ok := store.Load(key); ok {
		e := v.(*entry)
		e.lastSeen = now
		return e.limiter
	}

	// Race: two goroutines may create the same limiter; LoadOrStore ensures
	// only one wins, the other is discarded.
	e := &entry{
		limiter:  rate.NewLimiter(r, burst),
		lastSeen: now,
	}
	actual, _ := store.LoadOrStore(key, e)
	return actual.(*entry).limiter
}

// cleanLoop removes stale entries every cleanupInterval.
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
	for _, store := range []*sync.Map{&m.tokenLimiters, &m.ipLimiters} {
		store.Range(func(k, v any) bool {
			if v.(*entry).lastSeen.Before(cutoff) {
				store.Delete(k)
			}
			return true
		})
	}
}

// extractBearerToken returns the token from "Authorization: Bearer <token>",
// or "" if absent or malformed.
func extractBearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return ""
	}
	return parts[1]
}

// extractIP returns the most-specific client IP.
// Prefers X-Real-IP (set by RealIP middleware), then X-Forwarded-For first entry,
// then RemoteAddr.
func extractIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// X-Forwarded-For: client, proxy1, proxy2 — take the leftmost
		if idx := strings.Index(fwd, ","); idx != -1 {
			return strings.TrimSpace(fwd[:idx])
		}
		return strings.TrimSpace(fwd)
	}
	// Strip port from RemoteAddr ("1.2.3.4:5678" → "1.2.3.4")
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

// retryAfterSeconds returns the number of whole seconds the caller should wait
// before the next token is available.  We use a reservation that is immediately
// cancelled so we do not consume a real token.
func retryAfterSeconds(lim *rate.Limiter) int {
	r := lim.Reserve()
	d := r.Delay()
	r.Cancel() // release the reservation — does not penalise the caller
	secs := int(math.Ceil(d.Seconds()))
	if secs < 1 {
		return 1
	}
	return secs
}
