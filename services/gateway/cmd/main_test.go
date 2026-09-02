package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestMainWiresConsumerGroupLifecycle is a structural regression guard,
// mirroring flag-api's TestSSOConfigWiresAllowedDomains/TestOBS1MetricsAreWired
// technique (services/flag-api/cmd/main_helpers_test.go): unit-testing the
// wiring inside main() directly isn't possible without a live Redis/HTTP
// server, so this parses main.go's source instead. Without a guard like
// this, a future refactor of main()'s startup/shutdown sequence could
// silently drop any of GW-1's consumer-group lifecycle wiring — seeding on
// startup, the periodic reclaim/GC sweeps, or the graceful-shutdown
// destroy — with nothing but an abandoned group's PEL quietly accumulating
// in Redis to notice, days or weeks later.
func TestMainWiresConsumerGroupLifecycle(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)

	checks := []struct {
		name string
		re   string
	}{
		{"seeds this replica's own consumer group on startup",
			`hub\.CreateConsumerGroups\(ctx,\s*rdb,\s*knownEnvs,\s*broadcaster\.Group\(\),\s*logger\)`},
		{"starts one RunStreamConsumer goroutine per known environment",
			`broadcaster\.RunStreamConsumer\(ctx,\s*env\)`},
		{"starts the periodic reclaim sweep",
			`go runReclaimLoop\(ctx,\s*broadcaster,\s*knownEnvs,\s*logger\)`},
		{"starts the periodic idle-group GC sweep",
			`go runGroupGCLoop\(ctx,\s*rdb,\s*knownEnvs,\s*logger\)`},
		{"waits for stream-consumer goroutines to exit before destroying their group",
			`streamConsumersWG\.Wait\(\)`},
		{"destroys this replica's own consumer group on every known environment's stream at shutdown",
			`rdb\.XGroupDestroy\(shutdownDestroyCtx,\s*hub\.StreamKey\(env\),\s*broadcaster\.Group\(\)\)`},
	}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !regexp.MustCompile(c.re).MatchString(body) {
				t.Errorf("main.go no longer matches expected wiring pattern (%s): %s", c.name, c.re)
			}
		})
	}

	// The group-destroy loop must run in its own goroutine (concurrently
	// with srv.Shutdown), not synchronously ahead of it — regressing this
	// back to a plain `for` loop with no `go func` would reintroduce up to
	// 5s of pure sequential delay ahead of connection draining, contrary to
	// its own "must never block or delay the rest of shutdown" intent.
	//
	// This must be scoped to ONLY the destroy block's own text, not searched
	// across the whole file: main.go has an earlier, unrelated
	// `go func() { ... srv.ListenAndServe() ... }()` (the HTTP server-start
	// goroutine) that a loose `go func\(\)...XGroupDestroy` search spanning
	// the entire file would also match, regardless of whether the destroy
	// loop itself is wrapped in its own goroutine or not — which would make
	// this check pass even after reverting exactly the regression it exists
	// to catch. shutdownDestroyBlock isolates the substring between
	// streamConsumersWG.Wait() and srv.Shutdown's own setup, so only text
	// belonging to the destroy step itself is searched.
	destroyBlock := shutdownDestroyBlock(t, body)
	if !regexp.MustCompile(`go func\(\)\s*\{[\s\S]*?XGroupDestroy`).MatchString(destroyBlock) {
		t.Error("main.go's consumer-group destroy loop no longer runs inside its own goroutine — this must stay concurrent with srv.Shutdown, not ahead of it")
	}
}

// shutdownDestroyBlock returns the substring of main.go's source between
// streamConsumersWG.Wait() (the end of the stream-consumer shutdown
// synchronization) and the srv.Shutdown setup (shutdownCtx, shutdownCancel
// := ...) — i.e. just the graceful-shutdown consumer-group destroy step,
// with nothing from earlier in the file (like the unrelated HTTP
// server-start goroutine) included.
func shutdownDestroyBlock(t *testing.T, src string) string {
	t.Helper()
	const startMarker = "streamConsumersWG.Wait()"
	const endMarker = "shutdownCtx, shutdownCancel :="

	start := strings.Index(src, startMarker)
	if start == -1 {
		t.Fatalf("could not find %q in main.go", startMarker)
	}
	start += len(startMarker)

	end := strings.Index(src[start:], endMarker)
	if end == -1 {
		t.Fatalf("could not find %q in main.go after streamConsumersWG.Wait()", endMarker)
	}
	return src[start : start+end]
}
