# netguard — Security

`netguard` is the Server-Side Request Forgery (SSRF) guard for stenella's
server-side fetches. This document states what the code actually does. It does
not claim to be a firewall, and it is only effective for HTTP clients that use
it.

## Assets

- **The server's network position** — the ability to open connections to
  loopback, the internal network, the container/host network and cloud metadata.
- **Cloud instance credentials** — reachable through the metadata endpoint
  (`169.254.169.254`) on many cloud providers.
- **Internal services** — anything listening on a private or loopback address
  that trusts requests originating from the host.
- **The fetch reply itself** — a feed item or an error message can carry the
  response body back to whoever added the source.

## Threat model and mitigations

The attacker controls a URL that the platform will fetch (a feed source or an
OPML `xmlUrl`). The goal is to make the server connect somewhere it should not.

- **Address classification at dial time.** `blockReason` refuses, in order:
  loopback (`IsLoopback`), unspecified (`IsUnspecified`), link-local unicast or
  multicast (`IsLinkLocalUnicast` / `IsLinkLocalMulticast`, which includes
  `169.254.169.254`), private RFC1918 and IPv6 unique-local (`IsPrivate`), and
  multicast (`IsMulticast`). Anything else is permitted.
- **Dial-time placement covers redirects.** The guard is the transport's
  `DialContext`, so it runs for every connection, and an allow-listed URL that
  redirects to a blocked address is refused at the next dial.
- **Resolve-then-dial-by-IP.** The host is resolved once
  (`net.DefaultResolver.LookupIPAddr`), each answer is checked, and the
  connection targets the checked IP literal, defeating a DNS answer that changes
  between the check and the connect (rebinding).
- **IPv4-mapped IPv6 normalisation.** `ip.To4()` is applied first so
  `::ffff:10.0.0.1` is checked as the IPv4 it is, not as an unrelated IPv6.
- **Fail-closed resolution.** A hostname that resolves to no addresses is
  refused.
- **Actionable refusals.** Errors name the blocked IP and reason and append the
  remediation string, so an operator does not have to read the source to find the
  one supported escape hatch.

## Honest limits / non-claims

- **It only guards clients that use it.** Any code that fetches with a plain
  `http.DefaultClient` / `http.Get`, or with a transport built without
  `netguard`, bypasses the guard entirely. `NewTransport()` must be wired in.
- **`AllowPrivate = true` disables every check.** With it set, `DialContext`
  dials exactly as `net.Dialer` would. It is intended only for loopback-only
  development and test setups and must never be set in an internet-facing
  process. The shipped binary defaults it to `false`.
- **Classification is only as broad as Go's `net.IP` predicates.** Address
  ranges those predicates do not classify as private are not blocked, including
  carrier-grade NAT `100.64.0.0/10`, `192.0.0.0/24`, and NAT64/6to4/Teredo
  addresses that embed a private IPv4. It is not an exhaustive RFC 6890
  special-use registry.
- **No hostname or port policy.** There is no allow-list, deny-list, scheme
  check or port restriction. Any public address and any port is dialable.
- **No egress firewall.** This is an in-process check, not a network policy; a
  separate process, an SSRF in another component, or a direct socket bypasses
  it.
- **DNS is still trusted to answer.** Rebinding between check and connect is
  mitigated by dialing the checked IP, but a resolver that returns a public
  address for a host that later routes internally is a network-level concern
  outside this package.
- **No logging, metrics or alerting.** Refusals are returned to the caller as
  errors; the package records nothing itself.
- **No protection against allowed public URLs.** A public endpoint that proxies
  or reflects internal data is reachable and is not the guard's concern.

## Dependency note

Standard library only: `context`, `errors`, `fmt`, `net`, `net/http`.

## Reporting

Report suspected vulnerabilities privately to **security@azzurro.tech**. Do not
open a public issue, and do not include live cloud credentials, internal host
names or a working SSRF payload in a report.
