// Package clientip decides which IP address a request is attributed to.
//
// A client controls every header it sends, so X-Forwarded-For, X-Real-IP and
// True-Client-IP say nothing about who connected unless a proxy we trust wrote
// them. This package believes X-Forwarded-For only when the TCP peer is inside
// a configured set of proxy networks (TRUSTED_PROXY_CIDRS, default: none), and
// then only the entries those proxies appended. X-Real-IP and True-Client-IP
// are never read.
//
// This package is the only place in the service that may read a forwarded
// header. Everything else (the rate limiter, the audit log, access logs) calls
// ClientIP, so the trust decision is made once and can be reviewed in one file.
//
// The same file is kept, with its own tests, in flag-api, marketplace and
// evaluator: each service builds from its own Docker context with GOWORK=off,
// so there is no shared module to import it from. Change all three together;
// TestCopiesStayIdentical fails when they drift.
package clientip

import (
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"strings"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
)

const (
	// EnvTrustedProxyCIDRs names the variable holding the comma-separated CIDRs
	// of the reverse proxies whose X-Forwarded-For is believed.
	EnvTrustedProxyCIDRs = "TRUSTED_PROXY_CIDRS"

	// ModePeerOnly means no proxy is trusted: the client is the TCP peer.
	ModePeerOnly = "peer_only"
	// ModeTrustedProxies means X-Forwarded-For is believed when the TCP peer is
	// inside one of the configured CIDRs.
	ModeTrustedProxies = "trusted_proxies"
)

// Trust is the set of proxy networks whose X-Forwarded-For is believed. The
// zero value trusts nothing. It is immutable once built.
type Trust struct {
	prefixes []netip.Prefix
}

// ParseTrust parses a comma-separated CIDR list. Whitespace and empty entries
// (a trailing comma) are tolerated; an empty list trusts nothing. An entry that
// is not a CIDR, or that cannot do what an operator listing it means, is an
// error, so a typo stops startup instead of silently trusting less, or more,
// than the operator intended.
func ParseTrust(raw string) (Trust, error) {
	var prefixes []netip.Prefix
	for _, field := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(field)
		if entry == "" {
			continue
		}
		prefix, err := parseProxyPrefix(entry)
		if err != nil {
			return Trust{}, err
		}
		prefixes = append(prefixes, prefix)
	}
	return Trust{prefixes: prefixes}, nil
}

// parseProxyPrefix parses one trusted-proxy CIDR and rejects the two forms that
// parse but defeat their purpose: a /0, which matches every peer and so makes
// the peer check a no-op, and an IPv4-mapped IPv6 prefix, which never matches
// because peers are unmapped to IPv4 before the check.
func parseProxyPrefix(entry string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(entry)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%s: invalid CIDR %q (want notation such as 10.0.0.0/8): %w",
			EnvTrustedProxyCIDRs, entry, err)
	}
	if prefix.Bits() == 0 {
		return netip.Prefix{}, fmt.Errorf("%s: %q matches every address, which would make every caller a trusted proxy; list only the proxy networks",
			EnvTrustedProxyCIDRs, entry)
	}
	if prefix.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("%s: %q is an IPv4-mapped IPv6 prefix, which never matches a peer; write it as an IPv4 CIDR",
			EnvTrustedProxyCIDRs, entry)
	}
	return prefix.Masked(), nil
}

// TrustFromEnv reads and parses TRUSTED_PROXY_CIDRS.
func TrustFromEnv() (Trust, error) {
	return ParseTrust(os.Getenv(EnvTrustedProxyCIDRs))
}

// Mode names how forwarded headers are treated, for the startup log.
func (t Trust) Mode() string {
	if len(t.prefixes) == 0 {
		return ModePeerOnly
	}
	return ModeTrustedProxies
}

// CIDRs returns the trusted networks in canonical form, for the startup log.
func (t Trust) CIDRs() []string {
	cidrs := make([]string, len(t.prefixes))
	for i, prefix := range t.prefixes {
		cidrs[i] = prefix.String()
	}
	return cidrs
}

// Middleware records the client IP of each request for ClientIP and for chi's
// Logger. Install it before the Logger and before anything that keys on the
// client. It never alters RemoteAddr or any header.
//
// When the TCP peer is inside a trusted network, X-Forwarded-For is walked
// right to left, skipping trusted hops; the first other entry is the client.
// An entry that does not parse ends the walk with no client, and every other
// case (an untrusted peer, no header, only trusted hops) uses the peer itself.
func (t Trust) Middleware(next http.Handler) http.Handler {
	fromPeer := chiMiddleware.ClientIPFromRemoteAddr(next)
	if len(t.prefixes) == 0 {
		return fromPeer
	}
	// The peer stands in only when the forwarded walk found nothing. Applying
	// it unconditionally would overwrite the client the walk just derived.
	peerIfNoneFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if chiMiddleware.GetClientIPAddr(r.Context()).IsValid() {
			next.ServeHTTP(w, r)
			return
		}
		fromPeer.ServeHTTP(w, r)
	})
	fromForwarded := chiMiddleware.ClientIPFromXFF(t.CIDRs()...)(peerIfNoneFound)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if peer, ok := peerAddr(r.RemoteAddr); ok && t.contains(peer) {
			fromForwarded.ServeHTTP(w, r)
			return
		}
		fromPeer.ServeHTTP(w, r)
	})
}

// ClientIP returns the client of r as a bare IP: the address Middleware
// derived, or, where the middleware did not run, the TCP peer. It never
// consults a header, and returns "" only when the peer address itself is not
// an IP.
func ClientIP(r *http.Request) string {
	if ip := chiMiddleware.GetClientIPAddr(r.Context()); ip.IsValid() {
		return ip.WithZone("").String()
	}
	if peer, ok := peerAddr(r.RemoteAddr); ok {
		return peer.String()
	}
	return ""
}

func (t Trust) contains(ip netip.Addr) bool {
	for _, prefix := range t.prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// peerAddr parses the host of a RemoteAddr: "ip:port", "[ip]:port", a bare IP,
// or a bracketed IP without a port. IPv4-mapped IPv6 is folded to IPv4 and a
// zone is dropped, so one host has one spelling and a trusted range matches it.
func peerAddr(remoteAddr string) (netip.Addr, bool) {
	if withPort, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return withPort.Addr().Unmap().WithZone(""), true
	}
	host := strings.TrimSuffix(strings.TrimPrefix(remoteAddr, "["), "]")
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap().WithZone(""), true
}
