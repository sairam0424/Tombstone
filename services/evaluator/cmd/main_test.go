package main

import (
	"errors"
	"os"
	"regexp"
	"testing"
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

// TestShouldNotifySlack directly unit-tests agg.OnTrip's success/failure
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
