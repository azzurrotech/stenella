# netguard — SSRF guard for server-side fetches

**Module** `azzurrotech/stenella` · **standard library only**

`netguard` blocks server-side HTTP fetches to addresses that are not publicly
routable. It is the transport-level guard against Server-Side Request Forgery
when stenella fetches operator- and user-supplied URLs.

## Overview

The package belongs to the `azzurrotech/stenella` module (Go 1.22). Without a
guard, anyone who can add a feed source or import an OPML file could point the
platform at loopback services, the RFC1918 internal network or the cloud
metadata endpoint (`169.254.169.254`) and read the reply through a feed item or
an error message.

The guard is installed as the `DialContext` of an `http.Transport`, which is
what makes it cover every connection the client opens — including each hop of a
redirect chain, because redirects dial anew. It is used by:

- `feed`'s default client (`netguard.NewTransport()` in `feed.New`), and
- `main.go`, which exposes the single escape hatch as `--allow-private-fetch`
  / `STENELLA_ALLOW_PRIVATE_FETCH=1` and assigns `netguard.AllowPrivate`.
- The `feed` and `web` test suites set `netguard.AllowPrivate = true` so their
  `httptest` fixtures on `127.0.0.1` are reachable.

## Public API

- `var AllowPrivate bool` — the package's single escape hatch. When `true`,
  `DialContext` performs no address checks at all. It defaults to `false`; the
  shipped binary leaves it false.
- `func DialContext(ctx context.Context, network, addr string) (net.Conn, error)`
  — an `http.Transport` dialer that refuses non-public targets. Wire it as
  `Transport.DialContext` so the check covers every connection, including
  redirect hops.
- `func NewTransport() *http.Transport` — returns a clone of
  `http.DefaultTransport` whose `DialContext` is `DialContext`. Use this instead
  of the default transport for any server-side fetch.

The package exports nothing else; `blockReason`, `checkIP` and `remediation`
are unexported.

## Usage

```go
// Simplest: a guarded client for server-side fetches.
client := &http.Client{
    Timeout:   20 * time.Second,
    Transport: netguard.NewTransport(),
}

// Or wire the guard onto an existing transport.
tr := http.DefaultTransport.(*http.Transport).Clone()
tr.DialContext = netguard.DialContext
```

## How a guarded fetch flows

1. The caller builds an `http.Client` (or transport) with
   `netguard.NewTransport()`.
2. `http.Client.Do` connects, and the transport calls
   `DialContext(ctx, "tcp", "host:port")`.
3. `DialContext` splits host and port, parses the host as an IP literal when
   possible, and otherwise resolves it with `net.DefaultResolver.LookupIPAddr`.
4. Every answer is checked with `checkIP`; if none is permitted, the first
   refusal is returned and the request fails.
5. The first permitted answer that connects is dialed as
   `net.JoinHostPort(ip.String(), port)` — the literal, not the hostname.

## Configuration / inputs

- `AllowPrivate` is process-global and is checked at the top of both
  `blockReason` and `DialContext`.
- In the shipped server the only supported way to set it is the
  `--allow-private-fetch` flag (environment `STENELLA_ALLOW_PRIVATE_FETCH=1`),
  which `main.go` assigns to `netguard.AllowPrivate`.
- Refused addresses are named in the error, and the message always appends the
  remediation string `" (set --allow-private-fetch only for loopback dev/test
  setups)"`.

## Testing

From `/home/matthew/Projects/Platform/stenella`:

```bash
go test ./netguard/...
```

Tests cover the `blockReason` table (public IPv4/IPv6 allowed; loopback,
RFC1918, IPv6 unique-local, cloud metadata, IPv4-mapped loopback, unspecified
v4/v6 refused), that `AllowPrivate` starts false and opens every refusal, and
that `DialContext` actually refuses a live `127.0.0.1` listener — by IP literal
and by hostname — with an error naming both the reason and the flag, then
succeeds once `AllowPrivate` is set.

## Design notes

- **Check at dial time, not URL-parse time.** The transport dials per
  connection; redirects open new connections, so a URL that passes the first
  check and 302s to an internal address is refused at the second dial.
- **Resolve once, dial the IP literal.** A hostname is resolved with
  `net.DefaultResolver.LookupIPAddr`, each answer is verified, and the
  connection is made to the checked `ip.String()` — not the hostname — so a DNS
  answer cannot change between the check and the connect (DNS rebinding).
- **IPv4-mapped IPv6 is normalised.** `ip.To4()` runs first so `::ffff:10.0.0.1`
  cannot slip past the IPv4 checks in an IPv6 costume.
- **Multiple answers.** If a host resolves to several addresses, each is checked;
  the first permitted one that dials wins, and the first error is returned if
  none do.
- **Refusal is descriptive and actionable.** Errors say what was blocked (the
  IP and the reason) and how to lift the guard for loopback development.

## Refused address classes

`blockReason` refuses, in order:

1. loopback (`127.0.0.0/8`, `::1`)
2. unspecified (`0.0.0.0`, `::`)
3. link-local unicast or multicast, which includes the cloud metadata address
   `169.254.169.254`
4. private — RFC1918 (`10/8`, `172.16/12`, `192.168/16`) or IPv6 unique-local
   (`fc00::/7`)
5. multicast

Any other address is permitted.
