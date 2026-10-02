package main

import (
	"os"
	"strings"
	"testing"
)

// TestRateLimiterIsWiredWithTokenHasher pins the one production call site of
// the rate limiter.
//
// Without middleware.WithCredentialHasher the limiter falls back to an unkeyed
// SHA-256 for its Redis bucket keys. That still keeps the raw credential out of
// Redis, but a weak token (the documented dev token, SCIM_TOKEN) can then be
// confirmed from a key by anyone who can read Redis. The fallback is
// deliberate for the middleware package's own tests, so nothing there notices
// when main.go stops passing the hasher.
//
// It is a source-level check for the same reason as
// TestEveryAPIRouteIsPermissionGated: the limiter is built inline in main()
// and main() cannot run in a test without a live DB, Redis, and OPA policy dir.
func TestRateLimiterIsWiredWithTokenHasher(t *testing.T) {
	const (
		constructor = "middleware.NewRateLimitMiddleware("
		hasherOpt   = "middleware.WithCredentialHasher(tokenHasher)"
	)

	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	calls := callArguments(string(src), constructor)
	if len(calls) == 0 {
		t.Fatalf("found no %s call in main.go; update this test if the limiter is now built elsewhere", constructor)
	}

	for _, args := range calls {
		if !strings.Contains(args, hasherOpt) {
			t.Errorf("%s%s) is missing %s; the limiter would key buckets by an unkeyed SHA-256",
				constructor, strings.TrimSpace(args), hasherOpt)
		}
	}
}

// callArguments returns the text between the parentheses of every call that
// starts with opener (which must end in "("), found by paren matching. A call
// whose parentheses never balance is dropped.
func callArguments(src, opener string) []string {
	var calls []string
	for from := 0; ; {
		idx := strings.Index(src[from:], opener)
		if idx < 0 {
			return calls
		}
		start := from + idx + len(opener)
		depth, end := 1, start
		for ; end < len(src) && depth > 0; end++ {
			switch src[end] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth != 0 {
			return calls
		}
		calls = append(calls, src[start:end-1])
		from = end
	}
}
