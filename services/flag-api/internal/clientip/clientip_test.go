package clientip_test

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/tombstone/flag-api/internal/clientip"
)

const (
	// trustedProxyCIDRs is the trust list most cases run against: a private
	// range standing in for the proxies in front of the service.
	trustedProxyCIDRs = "10.0.0.0/8"
	// trustedPeer and trustedHop are inside that range.
	trustedPeer = "10.1.2.3:4444"
	// untrustedPeer is a public address, i.e. a client connecting directly.
	untrustedPeer = "203.0.113.9:5555"
)

func mustParseTrust(t *testing.T, raw string) clientip.Trust {
	t.Helper()
	trust, err := clientip.ParseTrust(raw)
	if err != nil {
		t.Fatalf("ParseTrust(%q): %v", raw, err)
	}
	return trust
}

// resolve sends one request through trust's middleware and returns what the
// next handler sees as the client IP. Headers are added, not set, so a test
// can send the same header on several lines.
func resolve(t *testing.T, trust clientip.Trust, remoteAddr string, headers map[string][]string) string {
	t.Helper()
	var got string
	handler := trust.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = clientip.ClientIP(r)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	handler.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func xff(values ...string) map[string][]string {
	return map[string][]string{"X-Forwarded-For": values}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		trusted    string
		remoteAddr string
		headers    map[string][]string
		want       string
	}{
		// A client connecting directly: nothing it sends can name another client.
		{"direct peer forging X-Forwarded-For", trustedProxyCIDRs, untrustedPeer, xff("6.6.6.6"), "203.0.113.9"},
		{"direct peer forging X-Real-IP", trustedProxyCIDRs, untrustedPeer, map[string][]string{"X-Real-Ip": {"6.6.6.6"}}, "203.0.113.9"},
		{"direct peer forging True-Client-IP", trustedProxyCIDRs, untrustedPeer, map[string][]string{"True-Client-Ip": {"6.6.6.6"}}, "203.0.113.9"},
		{"direct peer forging every header at once", trustedProxyCIDRs, untrustedPeer, map[string][]string{
			"X-Forwarded-For": {"6.6.6.6"}, "X-Real-Ip": {"7.7.7.7"}, "True-Client-Ip": {"8.8.8.8"},
		}, "203.0.113.9"},
		{"direct peer forging a chain that ends in a trusted hop", trustedProxyCIDRs, untrustedPeer, xff("6.6.6.6, 10.9.9.9"), "203.0.113.9"},
		{"empty trust list ignores X-Forwarded-For even from a private peer", "", trustedPeer, xff("6.6.6.6"), "10.1.2.3"},

		// A trusted proxy: the chain is walked right to left to the first untrusted entry.
		{"trusted peer, single hop", trustedProxyCIDRs, trustedPeer, xff("198.51.100.7"), "198.51.100.7"},
		{"trusted peer, attacker-prepended spoof", trustedProxyCIDRs, trustedPeer, xff("6.6.6.6, 198.51.100.7"), "198.51.100.7"},
		{"trusted peer, multi-hop chain", trustedProxyCIDRs, trustedPeer, xff("198.51.100.7, 10.9.9.9"), "198.51.100.7"},
		{"trusted peer, no X-Forwarded-For", trustedProxyCIDRs, trustedPeer, nil, "10.1.2.3"},
		{"trusted peer, empty X-Forwarded-For", trustedProxyCIDRs, trustedPeer, xff(""), "10.1.2.3"},
		{"trusted peer, unparseable rightmost entry fails closed to the peer", trustedProxyCIDRs, trustedPeer, xff("198.51.100.7, garbage"), "10.1.2.3"},
		{"trusted peer, unparseable entry left of a trusted hop fails closed", trustedProxyCIDRs, trustedPeer, xff("198.51.100.7, garbage, 10.9.9.9"), "10.1.2.3"},
		{"trusted peer, every hop trusted falls back to the peer", trustedProxyCIDRs, trustedPeer, xff("10.8.8.8, 10.9.9.9"), "10.1.2.3"},
		{"trusted peer, v4-mapped alias of a trusted hop is still a hop", trustedProxyCIDRs, trustedPeer, xff("198.51.100.7, ::ffff:10.9.9.9"), "198.51.100.7"},
		{"trusted peer, v4-mapped client folds to IPv4", trustedProxyCIDRs, trustedPeer, xff("::ffff:198.51.100.7"), "198.51.100.7"},
		{"trusted peer, IPv6 client", trustedProxyCIDRs, trustedPeer, xff("2001:db8::55"), "2001:db8::55"},
		{"trusted peer, IPv6 zone on a client is dropped", trustedProxyCIDRs, trustedPeer, xff("fe80::1%eth0"), "fe80::1"},
		{"trusted peer, empty entries skipped", trustedProxyCIDRs, trustedPeer, xff("198.51.100.7,, 10.9.9.9"), "198.51.100.7"},
		{"trusted peer, several X-Forwarded-For lines are one chain", trustedProxyCIDRs, trustedPeer, xff("6.6.6.6", "198.51.100.7, 10.9.9.9"), "198.51.100.7"},
		{"trusted peer, X-Real-IP and True-Client-IP are never read", trustedProxyCIDRs, trustedPeer, map[string][]string{
			"X-Real-Ip": {"6.6.6.6"}, "True-Client-Ip": {"7.7.7.7"},
		}, "10.1.2.3"},
		{"trusted peer, X-Real-IP does not override the chain", trustedProxyCIDRs, trustedPeer, map[string][]string{
			"X-Forwarded-For": {"198.51.100.7"}, "X-Real-Ip": {"6.6.6.6"},
		}, "198.51.100.7"},

		// How the connection peer is written.
		{"IPv4 peer, port stripped", "", "203.0.113.9:5555", nil, "203.0.113.9"},
		{"bracketed IPv6 peer, port stripped", "", "[2001:db8::1]:4444", nil, "2001:db8::1"},
		{"bare IPv4 peer", "", "203.0.113.9", nil, "203.0.113.9"},
		{"bare IPv6 peer", "", "2001:db8::1", nil, "2001:db8::1"},
		{"bracketed IPv6 peer without a port", "", "[2001:db8::1]", nil, "2001:db8::1"},
		{"v4-mapped IPv6 peer folds to IPv4", "", "[::ffff:203.0.113.9]:4444", nil, "203.0.113.9"},
		{"IPv6 zone on the peer is dropped", "", "[fe80::1%eth0]:4444", nil, "fe80::1"},
		{"trusted bare IPv4 peer", trustedProxyCIDRs, "10.1.2.3", xff("198.51.100.7"), "198.51.100.7"},
		{"trusted v4-mapped peer is unmapped before the gate", trustedProxyCIDRs, "[::ffff:10.1.2.3]:4444", xff("198.51.100.7"), "198.51.100.7"},
		{"trusted IPv6 peer", "2001:db8::/32", "[2001:db8::1]:4444", xff("198.51.100.7"), "198.51.100.7"},
		{"several trusted ranges, mixed families", "10.0.0.0/8, 2001:db8::/32", "[2001:db8::7]:4444", xff("198.51.100.7, 10.2.2.2"), "198.51.100.7"},
		{"a hop in the second trusted range is skipped", "10.0.0.0/8, 2001:db8::/32", trustedPeer, xff("198.51.100.7, 2001:db8::9"), "198.51.100.7"},
		{"bare v4-mapped peer is unmapped before the gate", trustedProxyCIDRs, "::ffff:10.1.2.3", xff("198.51.100.7"), "198.51.100.7"},
		{"bracketed v4-mapped peer without a port is unmapped before the gate", trustedProxyCIDRs, "[::ffff:10.1.2.3]", xff("198.51.100.7"), "198.51.100.7"},
		{"zoned peer with a port loses its zone before the gate", "fe80::/10", "[fe80::1%eth0]:4444", xff("198.51.100.7"), "198.51.100.7"},
		{"bare zoned peer loses its zone before the gate", "fe80::/10", "fe80::1%eth0", xff("198.51.100.7"), "198.51.100.7"},
		{"garbage peer yields nothing and headers are not consulted", trustedProxyCIDRs, "garbage", xff("6.6.6.6"), ""},
		{"empty peer yields nothing", trustedProxyCIDRs, "", xff("6.6.6.6"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolve(t, mustParseTrust(t, tc.trusted), tc.remoteAddr, tc.headers)

			if got != tc.want {
				t.Fatalf("client IP = %q, want %q", got, tc.want)
			}
			if tc.want == "" {
				return
			}
			if _, err := netip.ParseAddr(got); err != nil {
				t.Errorf("client IP %q is not a valid IP: %v", got, err)
			}
		})
	}
}

// TestClientIP_WithoutMiddlewareUsesThePeer pins the fail-safe: code that
// reads the client IP on a request the middleware never saw (a unit test, a
// handler mounted outside the router) gets the peer, not a header.
func TestClientIP_WithoutMiddlewareUsesThePeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	req.Header.Set("X-Real-IP", "7.7.7.7")
	req.Header.Set("True-Client-IP", "8.8.8.8")

	if got := clientip.ClientIP(req); got != "203.0.113.9" {
		t.Errorf("ClientIP() = %q, want the peer 203.0.113.9", got)
	}
}

// TestMiddleware_LeavesTheRequestUntouched pins the difference from chi's
// deprecated RealIP: the derived IP lives in the context only, so a later
// reader of RemoteAddr or the headers still sees what actually arrived.
func TestMiddleware_LeavesTheRequestUntouched(t *testing.T) {
	trust := mustParseTrust(t, trustedProxyCIDRs)
	var seen *http.Request
	handler := trust.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r }))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags?x=1", nil)
	req.RemoteAddr = trustedPeer
	req.Header.Set("X-Forwarded-For", "198.51.100.7")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen == nil {
		t.Fatal("next handler was not called")
	}
	if seen.RemoteAddr != trustedPeer {
		t.Errorf("RemoteAddr = %q, want it unchanged (%q)", seen.RemoteAddr, trustedPeer)
	}
	if got := seen.Header.Get("X-Forwarded-For"); got != "198.51.100.7" {
		t.Errorf("X-Forwarded-For = %q, want it unchanged", got)
	}
	if seen.URL.Path != "/api/v1/flags" || seen.Method != http.MethodGet {
		t.Errorf("request was altered: %s %s", seen.Method, seen.URL.Path)
	}
}

func TestZeroTrustTrustsNothing(t *testing.T) {
	var trust clientip.Trust

	got := resolve(t, trust, trustedPeer, xff("6.6.6.6"))

	if got != "10.1.2.3" {
		t.Errorf("client IP = %q, want the peer 10.1.2.3", got)
	}
	if trust.Mode() != clientip.ModePeerOnly || len(trust.CIDRs()) != 0 {
		t.Errorf("zero Trust reports mode %q and CIDRs %q, want peer-only and none", trust.Mode(), trust.CIDRs())
	}
}

func TestMiddleware_ConcurrentRequests(t *testing.T) {
	trust := mustParseTrust(t, trustedProxyCIDRs)
	const clients = 64
	var wg sync.WaitGroup
	results := make([]string, clients)
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}).String()
			results[i] = resolve(t, trust, trustedPeer, xff("6.6.6.6, "+client))
		}()
	}
	wg.Wait()

	for i, got := range results {
		if want := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}).String(); got != want {
			t.Errorf("request %d resolved to %q, want %q", i, got, want)
		}
	}
}

func TestParseTrust(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantMode string
		want     []string
	}{
		{"empty trusts nothing", "", clientip.ModePeerOnly, nil},
		{"whitespace only", "  \t ", clientip.ModePeerOnly, nil},
		{"only separators", " , ,, ", clientip.ModePeerOnly, nil},
		{"one CIDR", "10.0.0.0/8", clientip.ModeTrustedProxies, []string{"10.0.0.0/8"}},
		{"several CIDRs keep their order", "10.0.0.0/8,192.168.0.0/16,2001:db8::/32", clientip.ModeTrustedProxies,
			[]string{"10.0.0.0/8", "192.168.0.0/16", "2001:db8::/32"}},
		{"whitespace around entries", "  10.0.0.0/8 ,\t192.168.0.0/16  ", clientip.ModeTrustedProxies,
			[]string{"10.0.0.0/8", "192.168.0.0/16"}},
		{"trailing comma", "10.0.0.0/8,", clientip.ModeTrustedProxies, []string{"10.0.0.0/8"}},
		{"leading and doubled commas", ",10.0.0.0/8,,192.168.0.0/16,", clientip.ModeTrustedProxies,
			[]string{"10.0.0.0/8", "192.168.0.0/16"}},
		{"single-host CIDR", "10.1.2.3/32", clientip.ModeTrustedProxies, []string{"10.1.2.3/32"}},
		{"host bits are masked", "10.1.2.3/8", clientip.ModeTrustedProxies, []string{"10.0.0.0/8"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			trust, err := clientip.ParseTrust(tc.raw)
			if err != nil {
				t.Fatalf("ParseTrust(%q): %v", tc.raw, err)
			}
			if trust.Mode() != tc.wantMode {
				t.Errorf("Mode() = %q, want %q", trust.Mode(), tc.wantMode)
			}
			if got := strings.Join(trust.CIDRs(), " "); got != strings.Join(tc.want, " ") {
				t.Errorf("CIDRs() = %q, want %q", trust.CIDRs(), tc.want)
			}
		})
	}
}

// TestParseTrust_RejectsInvalidEntries pins that a typo fails startup with an
// error naming the variable and the entry, rather than panicking (chi's own
// constructor does) or quietly trusting less than the operator meant.
func TestParseTrust_RejectsInvalidEntries(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		badText string
	}{
		{"not a CIDR", "not-a-cidr", "not-a-cidr"},
		{"bare address without a prefix length", "10.0.0.1", "10.0.0.1"},
		{"prefix length out of range", "10.0.0.0/33", "10.0.0.0/33"},
		{"IPv6 prefix length out of range", "2001:db8::/129", "2001:db8::/129"},
		{"one bad entry among good ones", "10.0.0.0/8, bogus ,192.168.0.0/16", "bogus"},
		{"zone in a prefix", "fe80::1%eth0/64", "fe80::1%eth0/64"},
		{"hostname", "proxy.internal", "proxy.internal"},
		// A /0 prefix matches every peer, so it would make every caller a
		// trusted proxy: the vulnerability this package exists to close.
		{"zero-length IPv4 prefix", "0.0.0.0/0", "0.0.0.0/0"},
		{"zero-length IPv6 prefix", "::/0", "::/0"},
		{"zero-length prefix among good ones", "10.0.0.0/8, 0.0.0.0/0", "0.0.0.0/0"},
		// Peers are unmapped to IPv4 before the check, so an IPv4-mapped IPv6
		// prefix would parse, report trusted_proxies, and never match a peer.
		{"IPv4-mapped IPv6 prefix", "::ffff:10.0.0.0/104", "::ffff:10.0.0.0/104"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			trust, err := clientip.ParseTrust(tc.raw)

			if err == nil {
				t.Fatalf("ParseTrust(%q) succeeded with CIDRs %q, want an error", tc.raw, trust.CIDRs())
			}
			if !strings.Contains(err.Error(), clientip.EnvTrustedProxyCIDRs) {
				t.Errorf("error %q does not name %s", err, clientip.EnvTrustedProxyCIDRs)
			}
			if !strings.Contains(err.Error(), tc.badText) {
				t.Errorf("error %q does not name the offending entry %q", err, tc.badText)
			}
			if trust.Mode() != clientip.ModePeerOnly {
				t.Errorf("a failed parse returned mode %q, want peer-only", trust.Mode())
			}
		})
	}
}

// deployedEnvName is the variable name the Helm chart, docker-compose files,
// .env.example and the docs all use. The tests below set it by this literal, not
// through clientip.EnvTrustedProxyCIDRs, so renaming the constant to something
// the deployment does not set fails here instead of silently reverting every
// service to peer-only, which puts all clients behind a proxy in one bucket.
const deployedEnvName = "TRUSTED_PROXY_CIDRS"

func TestTrustFromEnv(t *testing.T) {
	t.Run("unset trusts nothing", func(t *testing.T) {
		t.Setenv(deployedEnvName, "")

		trust, err := clientip.TrustFromEnv()

		if err != nil || trust.Mode() != clientip.ModePeerOnly {
			t.Errorf("TrustFromEnv() = mode %q, err %v; want peer-only and no error", trust.Mode(), err)
		}
	})
	t.Run("reads the variable", func(t *testing.T) {
		t.Setenv(deployedEnvName, " 10.0.0.0/8 ,")

		trust, err := clientip.TrustFromEnv()

		if err != nil {
			t.Fatalf("TrustFromEnv(): %v", err)
		}
		if got := trust.CIDRs(); len(got) != 1 || got[0] != "10.0.0.0/8" {
			t.Errorf("CIDRs() = %q, want [10.0.0.0/8]", got)
		}
	})
	t.Run("an invalid value is an error", func(t *testing.T) {
		t.Setenv(deployedEnvName, "10.0.0.0/99")

		if _, err := clientip.TrustFromEnv(); err == nil {
			t.Error("TrustFromEnv() succeeded, want an error")
		}
	})
}

// TestMiddleware_FeedsTheAccessLog pins the wiring main.go relies on: with the
// middleware installed before chi's Logger, the access log line names the
// derived client, and a forged header from a direct peer cannot change it.
func TestMiddleware_FeedsTheAccessLog(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string][]string
		want       string
		notWant    string
	}{
		{"behind a trusted proxy", trustedPeer, xff("6.6.6.6, 198.51.100.7"), "from 198.51.100.7 - ", "6.6.6.6"},
		{"direct peer forging the header", untrustedPeer, xff("6.6.6.6"), "from 203.0.113.9 - ", "6.6.6.6"},
		// No client is derivable, so the peer stands in. chi's Logger prints
		// RemoteAddr (with its port) unless the middleware recorded an IP.
		{"trusted peer without the header", trustedPeer, nil, "from 10.1.2.3 - ", ":4444"},
		{"trusted peer, every hop trusted", trustedPeer, xff("10.8.8.8, 10.9.9.9"), "from 10.1.2.3 - ", ":4444"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			router := chi.NewRouter()
			router.Use(mustParseTrust(t, trustedProxyCIDRs).Middleware)
			router.Use(chiMiddleware.RequestLogger(&chiMiddleware.DefaultLogFormatter{
				Logger: log.New(&logs, "", 0), NoColor: true,
			}))
			router.Get("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			for name, values := range tc.headers {
				for _, value := range values {
					req.Header.Add(name, value)
				}
			}

			router.ServeHTTP(httptest.NewRecorder(), req)

			if !strings.Contains(logs.String(), tc.want) {
				t.Errorf("access log %q does not contain %q", logs.String(), tc.want)
			}
			if strings.Contains(logs.String(), tc.notWant) {
				t.Errorf("access log %q contains the forged value %q", logs.String(), tc.notWant)
			}
		})
	}
}
