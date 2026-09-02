package main

import (
	"reflect"
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
