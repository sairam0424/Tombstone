package v1

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
)

const defaultMaxSSEClientsPerEnv = 5000

// connLimiter tracks the number of live SSE connections per environment.
// It is a package-level singleton shared by all SSEHandler instances.
var connLimiter = newSSEConnLimiter()

type sseConnLimiter struct {
	counters sync.Map // key: environment string -> *atomic.Int64
	maxPerEnv int64
}

func newSSEConnLimiter() *sseConnLimiter {
	max := int64(defaultMaxSSEClientsPerEnv)
	if v := os.Getenv("MAX_SSE_CLIENTS_PER_ENV"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			max = n
		}
	}
	return &sseConnLimiter{maxPerEnv: max}
}

// counter returns (creating if needed) the atomic counter for an environment.
func (l *sseConnLimiter) counter(env string) *atomic.Int64 {
	if v, ok := l.counters.Load(env); ok {
		return v.(*atomic.Int64)
	}
	c := new(atomic.Int64)
	actual, _ := l.counters.LoadOrStore(env, c)
	return actual.(*atomic.Int64)
}

// tryAcquire increments the counter for env and returns true when the new count
// is within the limit.  The caller MUST call release(env) on connection close
// (using defer) if and only if tryAcquire returned true.
func (l *sseConnLimiter) tryAcquire(env string) bool {
	c := l.counter(env)
	newVal := c.Add(1)
	if newVal > l.maxPerEnv {
		c.Add(-1) // undo — we are over the limit
		return false
	}
	return true
}

// release decrements the counter for env.  Must only be called after a
// successful tryAcquire.
func (l *sseConnLimiter) release(env string) {
	l.counter(env).Add(-1)
}

// checkSSEConnLimit enforces the per-environment SSE connection limit.
// Returns false and writes a 429 if the limit is exceeded; returns true otherwise.
// On success the caller MUST defer releaseSSEConn(environment) to decrement.
func checkSSEConnLimit(w http.ResponseWriter, environment string) bool {
	if !connLimiter.tryAcquire(environment) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, `{"error":"SSE connection limit reached for environment","environment":%q,"limit":%d}`,
			environment, connLimiter.maxPerEnv)
		return false
	}
	return true
}

// releaseSSEConn decrements the connection counter.  Intended for use in defer.
func releaseSSEConn(environment string) {
	connLimiter.release(environment)
}
