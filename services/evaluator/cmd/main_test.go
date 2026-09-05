package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/evaluator/internal/circuit"
	"github.com/tombstone/evaluator/internal/rollback"
	"github.com/tombstone/evaluator/internal/telemetry"
)

// TestOBS1MetricsAreWired is a structural regression guard, mirroring
// flag-api/gateway's own TestOBS1MetricsAreWired: unit-testing the wiring
// inside main() directly isn't possible without a live Redis/HTTP server,
// so this parses main.go's source instead. Without a guard like this, a
// future refactor of the middleware chain or route block could silently
// drop either piece — RED metrics stop being recorded service-wide, or the
// Prometheus scrape endpoint starts 404ing — with nothing but a blank
// dashboard panel to notice.
func TestOBS1MetricsAreWired(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)

	if !regexp.MustCompile(`r\.Use\(httpMetrics\)`).MatchString(body) {
		t.Error("main.go no longer registers r.Use(httpMetrics) — RED metrics would silently stop being recorded for every request")
	}
	if !regexp.MustCompile(`r\.Get\("/metrics",\s*metricsHandler\.ServeHTTP\)`).MatchString(body) {
		t.Error("main.go no longer routes GET /metrics to metricsHandler — the Prometheus scrape endpoint would start 404ing")
	}
}

// TestInitTracerIsWired is a structural regression guard, mirroring
// TestOBS1MetricsAreWired's own technique: unit-testing whether main()
// actually calls telemetry.InitTracer isn't possible without a live
// server, so this parses main.go's source instead. Without this,
// internal/telemetry/otel_test.go proving InitTracer sets the global
// TextMapPropagator correctly WHEN CALLED gives no signal at all about
// whether main() still calls it — a future refactor could drop the call
// entirely and every existing test, including that one, would stay green.
func TestInitTracerIsWired(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)

	if !regexp.MustCompile(`telemetry\.InitTracer\(`).MatchString(body) {
		t.Error("main.go no longer calls telemetry.InitTracer — distributed trace propagation (both inbound spans via otelhttp.NewHandler and outbound via ResilientClient) would silently stop working, since the global TextMapPropagator is only ever set inside InitTracer")
	}
	if !regexp.MustCompile(`shutdownTracer\(`).MatchString(body) {
		t.Error("main.go no longer calls the shutdown function InitTracer returns — the tracer provider (and any buffered spans) would never be flushed on shutdown")
	}
}

// TestIsAuthorizedManualRollback directly unit-tests the EVAL-4 auth guard
// on POST /api/v1/rollback -- previously this endpoint had NO authentication
// at all, reachable by anyone who could reach the evaluator's port.
func TestIsAuthorizedManualRollback(t *testing.T) {
	t.Run("correct bearer token is authorized", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/rollback", nil)
		req.Header.Set("Authorization", "Bearer my-secret-token")
		if !isAuthorizedManualRollback(req, "my-secret-token") {
			t.Error("isAuthorizedManualRollback = false with the correct token, want true")
		}
	})

	t.Run("wrong bearer token is rejected", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/rollback", nil)
		req.Header.Set("Authorization", "Bearer wrong-token")
		if isAuthorizedManualRollback(req, "my-secret-token") {
			t.Error("isAuthorizedManualRollback = true with the wrong token, want false")
		}
	})

	t.Run("missing Authorization header is rejected", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/rollback", nil)
		if isAuthorizedManualRollback(req, "my-secret-token") {
			t.Error("isAuthorizedManualRollback = true with no Authorization header, want false")
		}
	})

	t.Run("non-Bearer scheme is rejected", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/rollback", nil)
		req.Header.Set("Authorization", "Basic my-secret-token")
		if isAuthorizedManualRollback(req, "my-secret-token") {
			t.Error("isAuthorizedManualRollback = true with a non-Bearer scheme, want false")
		}
	})

	t.Run("empty expected token fails closed, never authorizes anything", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/rollback", nil)
		req.Header.Set("Authorization", "Bearer ")
		if isAuthorizedManualRollback(req, "") {
			t.Error("isAuthorizedManualRollback = true with an empty expected token, want false -- an unconfigured FLAG_API_TOKEN must never mean \"anyone is authorized\"")
		}
	})
}

// TestManualRollbackEndpointIsGuarded is a structural regression guard,
// mirroring TestOBS1MetricsAreWired's own technique: unit-testing whether
// the /api/v1/rollback handler actually calls isAuthorizedManualRollback
// isn't practical without a live server, so this parses main.go's source
// instead. Without this guard, a future refactor of that handler could
// silently drop the auth check -- isAuthorizedManualRollback's own tests
// above would stay green regardless, since they test the function in
// isolation, not whether the route still calls it.
func TestManualRollbackEndpointIsGuarded(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)

	rollbackRoute := regexp.MustCompile(`(?s)r\.Post\("/api/v1/rollback".*?\n\t\}\)`).FindString(body)
	if rollbackRoute == "" {
		t.Fatal("main.go no longer registers POST /api/v1/rollback")
	}
	if !regexp.MustCompile(`isAuthorizedManualRollback\(`).MatchString(rollbackRoute) {
		t.Error("POST /api/v1/rollback no longer calls isAuthorizedManualRollback — this endpoint would become reachable by anyone who can reach the evaluator's port, with no authentication at all")
	}
}

// TestShouldNotifySlack directly unit-tests agg.OnRolloutChange's success/failure
// Slack-alert branching, extracted into shouldNotifySlack precisely so this
// logic doesn't need a live Redis/HTTP server to exercise -- unlike the
// OBS-1/tracer wiring above, which has no equivalent extraction. A future
// refactor that starts alerting on rollback failure (or stops alerting on
// success) fails here directly, rather than only being detectable via a
// production incident.
func TestShouldNotifySlack(t *testing.T) {
	t.Run("execution failed", func(t *testing.T) {
		shouldNotify, rollbackURL := shouldNotifySlack(errors.New("flag-api unreachable"), "https://dashboard.example.com", "my-flag")
		if shouldNotify {
			t.Error("shouldNotify = true on a failed rollback, want false — NotifyRollback's message claims the flag was disabled, which would be false")
		}
		if rollbackURL != "" {
			t.Errorf("rollbackURL = %q on a failed rollback, want empty", rollbackURL)
		}
	})

	t.Run("execution succeeded", func(t *testing.T) {
		shouldNotify, rollbackURL := shouldNotifySlack(nil, "https://dashboard.example.com", "my-flag")
		if !shouldNotify {
			t.Error("shouldNotify = false on a successful rollback, want true")
		}
		want := "https://dashboard.example.com/flags/my-flag"
		if rollbackURL != want {
			t.Errorf("rollbackURL = %q, want %q", rollbackURL, want)
		}
	})

	t.Run("flag key requiring percent-encoding", func(t *testing.T) {
		_, rollbackURL := shouldNotifySlack(nil, "https://dashboard.example.com", "team/feature x")
		want := "https://dashboard.example.com/flags/team%2Ffeature%20x"
		if rollbackURL != want {
			t.Errorf("rollbackURL = %q, want %q", rollbackURL, want)
		}
	})

	t.Run("empty dashboard URL produces a relative link", func(t *testing.T) {
		_, rollbackURL := shouldNotifySlack(nil, "", "my-flag")
		want := "/flags/my-flag"
		if rollbackURL != want {
			t.Errorf("rollbackURL = %q, want %q", rollbackURL, want)
		}
	})
}

// TestRollbackSlackNotificationIsDispatchedAsync is a structural regression
// guard, mirroring TestOBS1MetricsAreWired/TestInitTracerIsWired's own
// technique: unit-testing whether OnTrip's goroutine dispatch actually
// prevents blocking isn't practical without a live, deliberately slow
// webhook server wired through the whole Aggregator.Flush call chain, so
// this parses main.go's source instead. Without this guard, a future edit
// could silently move slackNotifier.NotifyRollback back onto the
// synchronous rollback path -- reintroducing the bug where a slow/
// unreachable SLACK_WEBHOOK_URL host stacks up to 10s of blocking time per
// tripped flag in front of the next flag's real kill-switch call.
func TestRollbackSlackNotificationIsDispatchedAsync(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)

	if !regexp.MustCompile(`go func\(\)\s*\{[^}]*slackNotifier\.NotifyRollback\(`).MatchString(body) {
		t.Error("main.go no longer dispatches slackNotifier.NotifyRollback inside a goroutine — this reintroduces blocking the circuit-breaker's hot rollback path on a slow/unreachable Slack webhook")
	}
}

// TestRolloutActionFor directly unit-tests the dispatch decision that was
// WRONG before the fix for a HIGH finding from adversarial review of
// PR #221: routing every non-zero rollout change through rollback-step
// (which can never increase exposure) meant the HALF_OPEN recovery ladder
// could never actually climb -- every probe step was unconditionally
// rejected by flag-api. Extracted into its own function specifically for
// this direct coverage, matching this file's shouldNotifySlack/
// isAuthorizedManualRollback convention.
func TestRolloutActionFor(t *testing.T) {
	tests := []struct {
		targetPct int
		phase     circuit.RolloutPhase
		want      rolloutAction
	}{
		{50, circuit.PhaseTripped, rolloutActionDecrease},
		{25, circuit.PhaseStepped, rolloutActionDecrease},
		{0, circuit.PhaseKilled, rolloutActionKill},
		{0, circuit.PhaseRevertedDuringRecovery, rolloutActionKill},
		{10, circuit.PhaseRecovering, rolloutActionIncrease},
		{100, circuit.PhaseRecovered, rolloutActionIncrease},
	}
	for _, tc := range tests {
		if got := rolloutActionFor(tc.targetPct, tc.phase); got != tc.want {
			t.Errorf("rolloutActionFor(%d, %q) = %v, want %v", tc.targetPct, tc.phase, got, tc.want)
		}
	}
}

// fakeFlagAPIState is the in-memory state a fakeFlagAPI test server tracks
// for a single flag+environment, mirroring flag_environments' own
// enabled/rollout_pct columns closely enough to faithfully enforce
// RollbackStep/RecoveryStep's real invariants.
type fakeFlagAPIState struct {
	mu      sync.Mutex
	enabled bool
	pct     int
}

// newFakeFlagAPI starts an httptest.Server that faithfully reproduces
// flag-api's real EVAL-4 endpoints' enforcement rules -- specifically,
// that rollback-step can only ever DECREASE effective exposure and
// recovery-step can only ever INCREASE it -- against the given shared
// state. This is what closes the actual gap adversarial review of PR #221
// found: every existing aggregator_test.go test stubs OnRolloutChange
// directly, so none of them exercise whether the REAL rollback.Executor,
// calling a server that enforces these real rules, can actually complete
// a full trip/descent/recovery cycle end to end.
func newFakeFlagAPI(t *testing.T, state *fakeFlagAPIState) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RolloutPct *int `json:"rollout_pct"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		state.mu.Lock()
		defer state.mu.Unlock()
		currentExposure := 0
		if state.enabled {
			currentExposure = state.pct
		}

		switch {
		case regexp.MustCompile(`/kill$`).MatchString(r.URL.Path):
			state.enabled = false
			state.pct = 0
			w.WriteHeader(http.StatusOK)
		case regexp.MustCompile(`/rollback-step$`).MatchString(r.URL.Path):
			if body.RolloutPct == nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			target := *body.RolloutPct
			if target > currentExposure {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			state.enabled = target > 0
			state.pct = target
			w.WriteHeader(http.StatusOK)
		case regexp.MustCompile(`/recovery-step$`).MatchString(r.URL.Path):
			if body.RolloutPct == nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			target := *body.RolloutPct
			if target < currentExposure {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			state.enabled = target > 0
			state.pct = target
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestEndToEndDescentThenRecoveryCycle wires the REAL Aggregator, Breaker,
// and rollback.Executor together (exactly as main()'s own agg.OnRolloutChange
// closure does, via the same rolloutActionFor dispatch) against a fake
// flag-api server that enforces the real endpoints' real invariants. This
// is the direct regression test for the HIGH finding from adversarial
// review of PR #221: before the fix, this test's recovery half would have
// failed at the very first probe step, since rollback-step (the only
// endpoint the old code called for every non-zero target) unconditionally
// rejects an increase.
func TestEndToEndDescentThenRecoveryCycle(t *testing.T) {
	state := &fakeFlagAPIState{enabled: true, pct: 100}
	srv := newFakeFlagAPI(t, state)
	defer srv.Close()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	breaker := circuit.NewBreaker(rdb, zap.NewNop())
	exec := rollback.NewExecutor(srv.URL, "test-token", rdb, zap.NewNop())
	agg := telemetry.NewAggregator(breaker, rdb, zap.NewNop())
	agg.OnRolloutChange = func(flagKey, env string, targetPct int, errorRate float64, phase circuit.RolloutPhase) bool {
		ctx := context.Background()
		var err error
		switch rolloutActionFor(targetPct, phase) {
		case rolloutActionKill:
			err = exec.Execute(ctx, rollback.RollbackRequest{
				FlagKey: flagKey, Environment: env, Reason: "circuit_breaker",
				ErrorRate: errorRate, TriggeredBy: "circuit_breaker",
			})
		case rolloutActionIncrease:
			err = exec.IncreaseRolloutPct(ctx, flagKey, env, targetPct, "circuit_breaker")
		case rolloutActionDecrease:
			err = exec.SetRolloutPct(ctx, flagKey, env, targetPct, "circuit_breaker")
		}
		return err == nil
	}

	ctx := context.Background()
	const flagKey, env = "checkout", "production"
	recordBurst := func(n, errs int) {
		for i := 0; i < n; i++ {
			agg.Record(telemetry.TelemetryEvent{FlagKey: flagKey, Environment: env, IsError: i < errs})
		}
	}
	assertState := func(t *testing.T, wantEnabled bool, wantPct int) {
		t.Helper()
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.enabled != wantEnabled || state.pct != wantPct {
			t.Fatalf("fake flag-api state = (enabled=%v, pct=%d), want (enabled=%v, pct=%d)", state.enabled, state.pct, wantEnabled, wantPct)
		}
	}

	// Descent: trip, then two more ticks of the deterministic, unconditional
	// step-down (any telemetry at all is enough -- the descent doesn't
	// re-check error rate mid-ladder).
	recordBurst(100, 10) // 10% error rate -- trips
	agg.Flush(ctx)
	assertState(t, true, 50)

	recordBurst(1, 0)
	agg.Flush(ctx)
	assertState(t, true, 25)

	recordBurst(1, 0)
	agg.Flush(ctx)
	assertState(t, false, 0) // terminal step -- real kill switch, not rollback-step

	if got := breaker.GetState(ctx, flagKey, env); got != circuit.StateOpen {
		t.Fatalf("state after full descent = %q, want OPEN", got)
	}

	// Force the cooldown to have elapsed so the very next tick attempts a
	// HALF_OPEN probe.
	breaker.SetOpenedAt(ctx, flagKey, env, time.Now().Add(-10*time.Minute))
	recordBurst(1, 0)
	agg.Flush(ctx)
	if got := breaker.GetState(ctx, flagKey, env); got != circuit.StateHalfOpen {
		t.Fatalf("state after cooldown elapsed = %q, want HALF_OPEN", got)
	}
	// This is the exact step the original bug made impossible: an INCREASE
	// from 0 to 10, which rollback-step (the only endpoint the pre-fix code
	// called) would have unconditionally rejected as HTTP 400, making
	// OnRolloutChange return false and the probe never start at all.
	assertState(t, true, 10)

	// Recovery: climb 10->25->50->100 with a clean, sufficiently large
	// window at every step (HALF_OPEN's advance IS gated on verification,
	// unlike the descent).
	for _, wantPct := range []int{25, 50, 100} {
		recordBurst(100, 0)
		agg.Flush(ctx)
		assertState(t, true, wantPct)
	}

	if got := breaker.GetState(ctx, flagKey, env); got != circuit.StateClosed {
		t.Fatalf("final state = %q, want CLOSED", got)
	}
}
