package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// nginxConfPath is the Oracle host's reverse-proxy config, relative to this
// package.
const nginxConfPath = "../../../infra/oracle/nginx.conf"

var (
	nginxComment       = regexp.MustCompile(`(?m)#.*$`)
	nginxLocationStart = regexp.MustCompile(`\blocation\s+[^{;]*\{`)
	nginxOverwritesXFF = regexp.MustCompile(`proxy_set_header\s+X-Forwarded-For\s+\$remote_addr\s*;`)
)

// TestNginxOverwritesForwardedForInEveryLocation pins the proxy half of the
// trusted-proxy contract. The services walk a trusted peer's X-Forwarded-For
// from the right and take the entry the proxy wrote, so a location that does
// not overwrite the header passes the caller's own chain straight through and
// the caller picks the IP that is rate-limited and audited. nginx inherits
// proxy_set_header from the server block only into a location that sets none
// of its own, so a location added later without the directive fails silently
// and nothing else in the repo would notice.
func TestNginxOverwritesForwardedForInEveryLocation(t *testing.T) {
	raw, err := os.ReadFile(nginxConfPath)
	if err != nil {
		t.Fatalf("read %s: %v", nginxConfPath, err)
	}
	conf := nginxComment.ReplaceAllString(string(raw), "")

	if strings.Contains(conf, "$proxy_add_x_forwarded_for") {
		t.Error("nginx.conf uses $proxy_add_x_forwarded_for, which keeps the chain the client sent; " +
			"set X-Forwarded-For to $remote_addr instead")
	}

	proxied := 0
	for _, loc := range nginxLocations(conf) {
		if !strings.Contains(loc.body, "proxy_pass") {
			continue
		}
		proxied++
		if !nginxOverwritesXFF.MatchString(loc.body) {
			t.Errorf("location %s proxies without `proxy_set_header X-Forwarded-For $remote_addr;`, "+
				"so a client-sent X-Forwarded-For reaches the service", loc.name)
		}
	}
	if proxied == 0 {
		t.Fatalf("found no proxied location in %s; the check would pass vacuously", nginxConfPath)
	}
}

type nginxLocation struct {
	name string
	body string
}

// nginxLocations returns every location block in conf, each ending at the brace
// that closes it, so a one-line block and a multi-line block with nested
// braces are read the same way.
func nginxLocations(conf string) []nginxLocation {
	var locations []nginxLocation
	for _, start := range nginxLocationStart.FindAllStringIndex(conf, -1) {
		opening := conf[start[0]:start[1]]
		depth := 1
		end := start[1]
		for ; end < len(conf) && depth > 0; end++ {
			switch conf[end] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		locations = append(locations, nginxLocation{
			name: strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(opening, "location"), "{")),
			body: conf[start[1]:end],
		})
	}
	return locations
}
