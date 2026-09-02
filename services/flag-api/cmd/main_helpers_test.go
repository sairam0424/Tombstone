package main

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TestSplitCommaList is the direct regression proof for SEC-5's AllowedDomains
// wiring gap: SSOConfig.AllowedDomains was always read and enforced by
// isAllowedDomain, but nothing in main.go ever populated it from an env var,
// so every deployment's domain allowlist silently had zero effect.
func TestSplitCommaList(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty string returns nil (unrestricted)", "", nil},
		{"single domain", "example.com", []string{"example.com"}},
		{"multiple domains", "example.com,other.com", []string{"example.com", "other.com"}},
		{"whitespace around entries is trimmed", " example.com , other.com ", []string{"example.com", "other.com"}},
		{"empty entries from stray commas are dropped", "example.com,,other.com,", []string{"example.com", "other.com"}},
		{"whitespace-only input returns nil", "   ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitCommaList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitCommaList(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestSSOConfigWiresAllowedDomains is a structural regression guard,
// mirroring TestEveryAPIRouteIsPermissionGated's source-parsing technique
// (authz_routes_test.go): unit-testing the wiring inside main() directly
// isn't possible without a live DB/server, so this parses main.go's source
// and fails if the SSOConfig{...} literal ever stops setting
// AllowedDomains — the exact class of silent regression (TestSplitCommaList
// proves the parser works; nothing proves anything still calls it here)
// that let this gap go unnoticed for as long as it did.
func TestSSOConfigWiresAllowedDomains(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	block := ssoConfigBlock(t, string(src))
	if !regexp.MustCompile(`AllowedDomains:\s*allowedDomains`).MatchString(block) {
		t.Errorf("SSOConfig{...} literal no longer sets AllowedDomains — this silently reopens the exact gap "+
			"SEC-5 fixed (any successfully-authenticated OIDC user from any domain could log in):\n%s", block)
	}
}

// ssoConfigBlock returns the body of the middleware.SSOConfig{...} composite
// literal by brace-matching from its opening "{" to the matching close.
func ssoConfigBlock(t *testing.T, src string) string {
	t.Helper()
	const marker = "middleware.SSOConfig{"
	start := strings.Index(src, marker)
	if start == -1 {
		t.Fatalf("could not find %q in main.go", marker)
	}
	open := start + len(marker) - 1 // index of the literal's opening "{"

	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open : i+1]
			}
		}
	}
	t.Fatalf("unbalanced braces in SSOConfig{...} literal")
	return ""
}
