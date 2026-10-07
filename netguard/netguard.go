// Package netguard blocks server-side fetches to addresses that are not
// publicly routable (SSRF guard).
//
// stenella fetches operator- and user-supplied URLs on the server side (feed
// sources, OPML import). Without a guard, anyone who can add a source could
// point the platform at loopback services, the RFC1918 internal network or the
// cloud metadata endpoint (169.254.169.254), and read the reply through a
// feed item or an error message.
//
// The guard is the DialContext of the http.Transport used by every server-side
// fetcher. That placement matters: the transport dials once per connection, and
// HTTP redirects open new connections, so each hop of a redirect chain is
// re-checked — an allow-listed URL that 302s to the metadata endpoint is
// refused at the second dial. The address is resolved once, verified, and then
// dialed by its checked IP literal so a DNS answer cannot change between the
// check and the connect (rebinding).
//
// Standard library only.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// AllowPrivate is the package's single escape hatch: when true, DialContext
// performs no address checks at all.
//
// It is safe ONLY in loopback-only development and test setups where the
// operator controls every fetch target — the feed and web test suites set it
// so they can fetch their own httptest fixtures on 127.0.0.1. It must never be
// set in a production or internet-facing process: doing so re-enables SSRF
// against loopback, the internal network and the cloud metadata endpoint.
//
// The shipped binary leaves it false; the --allow-private-fetch flag (env
// STENELLA_ALLOW_PRIVATE_FETCH=1) in main.go is the only way to turn it on.
var AllowPrivate bool

// remediation is appended to every refusal so the operator is told the one
// supported way to lift the guard without reading this source.
const remediation = " (set --allow-private-fetch only for loopback dev/test setups)"

// blockReason returns why ip must not be dialed, or "" when it may be.
// Loopback, unspecified, link-local (which includes the cloud metadata address
// 169.254.169.254), RFC1918 private, IPv6 unique-local and multicast
// destinations are all refused.
func blockReason(ip net.IP) string {
	if AllowPrivate {
		return ""
	}
	// Normalize IPv4-mapped IPv6 (::ffff:10.0.0.1) so it cannot slip past the
	// IPv4 checks by wearing an IPv6 costume.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsUnspecified():
		// 0.0.0.0/:: connect to loopback on Linux; treat as the same refusal.
		return "unspecified"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "link-local (includes cloud metadata 169.254.169.254)"
	case ip.IsPrivate():
		return "private (RFC1918 or IPv6 unique-local)"
	case ip.IsMulticast():
		return "multicast"
	}
	return ""
}

// checkIP reports whether the dialer may connect to ip, with a refusal that
// names the reason and the one supported way to lift the guard.
func checkIP(ip net.IP) error {
	reason := blockReason(ip)
	if reason == "" {
		return nil
	}
	return fmt.Errorf("netguard: refusing to dial %s: %s address%s", ip, reason, remediation)
}

// DialContext is an http.Transport dialer that refuses non-public targets.
// Wire it as Transport.DialContext so the check covers every connection the
// client opens, including redirect hops.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if AllowPrivate {
		// Escape hatch open: dial exactly as net.Dialer would, no checks.
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("netguard: dial %q: %w", addr, err)
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolved, rerr := net.DefaultResolver.LookupIPAddr(ctx, host)
		if rerr != nil {
			return nil, fmt.Errorf("netguard: resolve %q: %w", host, rerr)
		}
		for _, ra := range resolved {
			ips = append(ips, ra.IP)
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("netguard: host resolved to no addresses")
	}
	dialer := &net.Dialer{}
	var firstErr error
	for _, ip := range ips {
		// Dial the verified IP literal — not the hostname — so the checked
		// answer is the one actually connected to (anti-rebinding).
		if cerr := checkIP(ip); cerr != nil {
			if firstErr == nil {
				firstErr = cerr
			}
			continue
		}
		conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if derr == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = derr
		}
	}
	return nil, firstErr
}

// NewTransport returns a copy of http.DefaultTransport whose dialer enforces
// the guard. Callers that perform server-side fetches use this instead of the
// default transport so no request can bypass the check.
func NewTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = DialContext
	return tr
}
