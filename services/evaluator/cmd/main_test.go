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
