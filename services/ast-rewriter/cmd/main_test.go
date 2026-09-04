package main

import (
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
// ast-rewriter makes no outbound HTTP calls of its own, but its INBOUND
// spans (via otelhttp.NewHandler) still need this to correctly continue a
// caller's trace.
func TestInitTracerIsWired(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)

	if !regexp.MustCompile(`telemetry\.InitTracer\(`).MatchString(body) {
		t.Error("main.go no longer calls telemetry.InitTracer — distributed trace propagation (inbound spans via otelhttp.NewHandler) would silently stop working, since the global TextMapPropagator is only ever set inside InitTracer")
	}
	if !regexp.MustCompile(`shutdownTracer\(`).MatchString(body) {
		t.Error("main.go no longer calls the shutdown function InitTracer returns — the tracer provider (and any buffered spans) would never be flushed on shutdown")
	}
}
