package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// slowHandler returns a handler that sleeps for d before responding 200 —
// simulating a request held up behind a saturated downstream resource (e.g.
// the DB connection pool this middleware exists to protect).
func slowHandler(d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(d)
		w.WriteHeader(http.StatusOK)
	})
}

// TestLoadShed_RejectsSomeRequestsWhenConcurrencyExceedsFixedLimit pins the
// adaptive limiter's min/max/initial limit all to 1, so the limit cannot
// adapt away from 1 for the duration of the test. It then fires many
// concurrent requests at a handler that holds its single permit for a
// noticeable duration. TryAcquirePermit never queues, so any request that
// arrives while the one in-flight request is still running must be
// rejected immediately with 503 + Retry-After.
//
// This is inherently timing-sensitive: we use a generous handler delay (100ms)
// and a generous request count (20) and generous concurrency (fired via
// goroutines released simultaneously through a start barrier) so that,
// bar an extraordinarily overloaded CI runner, several requests are
// guaranteed to overlap the first one's execution window. We assert "at
// least one 503 occurred" rather than an exact count, per this repo's
// testing rules for probabilistic adaptive-limiter behavior.
func TestLoadShed_RejectsSomeRequestsWhenConcurrencyExceedsFixedLimit(t *testing.T) {
	m := NewLoadShedMiddleware(LoadShedConfig{MinLimit: 1, MaxLimit: 1, InitialLimit: 1}, zap.NewNop())
	handler := m.LoadShed(slowHandler(100 * time.Millisecond))

	const numRequests = 20
	var (
		wg           sync.WaitGroup
		okCount      atomic.Int32
		shedCount    atomic.Int32
		otherCount   atomic.Int32
		startBarrier = make(chan struct{})
	)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier // release all goroutines at once to maximize overlap

			req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			switch rec.Code {
			case http.StatusOK:
				okCount.Add(1)
			case http.StatusServiceUnavailable:
				shedCount.Add(1)
				if ra := rec.Header().Get("Retry-After"); ra == "" {
					t.Errorf("expected Retry-After header on 503 response")
				}
			default:
				otherCount.Add(1)
			}
		}()
	}

	close(startBarrier)
	wg.Wait()

	if got := otherCount.Load(); got != 0 {
		t.Fatalf("expected only 200 or 503 responses, got %d requests with other status codes", got)
	}
	if got := okCount.Load(); got == 0 {
		t.Fatalf("expected at least one request to succeed, got 0")
	}
	if got := shedCount.Load(); got == 0 {
		t.Fatalf("expected at least one request to be shed (503) under %d-way concurrency against a fixed limit of 1, got 0", numRequests)
	}
	t.Logf("ok=%d shed=%d (of %d concurrent requests)", okCount.Load(), shedCount.Load(), numRequests)
}

// TestLoadShed_AllowsAllRequestsUnderLightSequentialLoad confirms that when
// requests arrive one at a time (each completing before the next starts),
// none are shed — the limiter's single permit is always free by the time the
// next request asks for one.
func TestLoadShed_AllowsAllRequestsUnderLightSequentialLoad(t *testing.T) {
	m := NewLoadShedMiddleware(LoadShedConfig{MinLimit: 1, MaxLimit: 1, InitialLimit: 1}, zap.NewNop())
	handler := m.LoadShed(slowHandler(1 * time.Millisecond))

	const numRequests = 10
	for i := 0; i < numRequests; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 under light sequential load, got %d", i, rec.Code)
		}
	}
}

// TestLoadShed_HealthEndpointAlwaysExempt confirms /health is never shed,
// even while the single permit is held by an in-flight request — a shed
// health check would make an overloaded-but-still-serving instance look
// fully dead to its orchestrator.
func TestLoadShed_HealthEndpointAlwaysExempt(t *testing.T) {
	m := NewLoadShedMiddleware(LoadShedConfig{MinLimit: 1, MaxLimit: 1, InitialLimit: 1}, zap.NewNop())
	handler := m.LoadShed(slowHandler(50 * time.Millisecond))

	// Occupy the single permit with an in-flight request.
	occupied := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
		rec := httptest.NewRecorder()
		close(occupied)
		handler.ServeHTTP(rec, req)
	}()
	<-occupied
	time.Sleep(5 * time.Millisecond) // let the in-flight request actually acquire its permit

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected /health to always bypass load shedding, got %d", rec.Code)
	}
}

// TestLoadShed_PermitReleasedOnPanic confirms a panicking handler still
// releases its permit (via Drop, in the deferred recover) rather than
// leaking it — a leak would permanently shrink the effective concurrency
// limit by one slot per panic.
func TestLoadShed_PermitReleasedOnPanic(t *testing.T) {
	m := NewLoadShedMiddleware(LoadShedConfig{MinLimit: 1, MaxLimit: 1, InitialLimit: 1}, zap.NewNop())
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated handler panic")
	})
	handler := m.LoadShed(panicHandler)

	func() {
		defer func() {
			if rec := recover(); rec == nil {
				t.Fatalf("expected panic to propagate past LoadShed middleware")
			}
		}()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/flags", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}()

	if !m.limiter.CanAcquirePermit() {
		t.Fatalf("expected permit to be released back to the limiter after a panic, but limiter reports full")
	}
}
