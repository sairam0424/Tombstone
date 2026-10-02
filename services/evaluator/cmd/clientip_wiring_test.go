package main

import (
	"os"
	"regexp"
	"testing"
)

// TestClientIPMiddlewareIsWired pins how main() installs the trusted-proxy
// client IP middleware.
//
// The clientip and rate-limiter tests build their own routers, so none of them
// notices when main.go drops the middleware, moves it behind the rate limiter,
// or turns a bad TRUSTED_PROXY_CIDRS into a warning. Each of those leaves every
// test green while every per-IP bucket keys on the proxy's address (or, for a
// re-added RealIP, on a header the caller wrote).
// TestOnlyThisPackageReadsForwardedHeaders in internal/clientip covers RealIP.
//
// It is a source-level check for the same reason as TestOBS1MetricsAreWired:
// the router is built inline in main(), which cannot run in a test without a
// live Redis and HTTP server.
func TestClientIPMiddlewareIsWired(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)

	install := regexp.MustCompile(`r\.Use\(\s*proxyTrust\.Middleware\s*\)`).FindAllStringIndex(body, -1)
	if len(install) != 1 {
		t.Fatalf("main.go installs proxyTrust.Middleware %d times, want exactly once", len(install))
	}

	// Each of these reads the client IP, so it must run after the middleware.
	readers := []struct{ name, pattern string }{
		{"the rate limiter", `r\.Use\(\s*rateMw\.RateLimit\s*\)`},
	}
	for _, reader := range readers {
		at := regexp.MustCompile(reader.pattern).FindStringIndex(body)
		if at == nil {
			t.Errorf("main.go no longer installs %s as %s; update this test if it is wired differently now",
				reader.name, reader.pattern)
			continue
		}
		if at[0] < install[0][0] {
			t.Errorf("%s is installed before proxyTrust.Middleware, so it would see the proxy, not the client", reader.name)
		}
	}

	failsStartup := regexp.MustCompile(`(?s)proxyTrust,\s*err\s*:?=\s*clientip\.TrustFromEnv\(\)\s*if err != nil \{\s*logger\.Fatal\(`)
	if !failsStartup.MatchString(body) {
		t.Error("main.go no longer stops startup when clientip.TrustFromEnv() fails; " +
			"a typo in TRUSTED_PROXY_CIDRS would silently trust less, or more, than the operator meant")
	}
}
