# feed — Security

## Assets

- **Fetched item bodies** (titles, summaries, content) — sealed at rest through
  the caller-supplied `Cipher` (in production, `crypt.KeyStore`).
- **Source AuthSecret values** — bearer tokens used to fetch a protected source.
  The engine stores only the secret *name*; values are resolved by the caller.
- **Server-side fetch capability** — user/operator-supplied URLs fetched from
  inside the deployment.
- **ACL classification** — which items are public, private or protected.

## Threat model and mitigations

- **SSRF from feed URLs.** The default HTTP client uses
  `netguard.NewTransport()`, whose `DialContext` refuses loopback, unspecified,
  link-local (including `169.254.169.254`), RFC1918/IPv6-ULA private and
  multicast addresses, and re-checks each redirect hop. See the `netguard`
  package.
- **Plaintext at rest.** With a `Cipher` wired, `saveCache` seals every body
  before writing; the on-disk cache holds ciphertext plus clear metadata. On a
  seal error the affected items are dropped rather than persisted in the clear.
- **No server-side body search.** `ItemMatches` matches only id, source name/
  URL, author, link, guid and categories, so encrypted bodies are never scanned
  or reconstructed server-side.
- **ACL gating.** `Combined` checks an item's own stamped class against
  `Query.AclClass`; `AllowedACL` treats an empty allowed set as public-only, and
  `NormalizeACLClass` maps anything unrecognised to public (the only
  unauthenticated-served class).
- **Bounded input.** Responses are read through `io.LimitReader(..., 16<<20)`;
  HTML scraping is capped at 4 MiB and summaries at 4000 characters, limiting
  memory use from hostile or malformed sources.
- **XML parsing.** Parsing uses `encoding/xml`, which does not expand external
  entities or fetch external DTDs.
- **HTML extraction.** `parsePage` removes `script`, `style`, `noscript`, `svg`,
  `template` and `iframe` bodies before extracting text, and `cleanText`
  strips tags, so script bodies are not carried into item content. (The value is
  then sealed and, in the web layer, rendered as text.)
- **Secret handling.** `SecretResolver` is supplied by the caller; the engine
  never persists secret values, only the `AuthSecret` name.

## Honest limits / non-claims

- **No cipher means no encryption.** If `Options.Cipher` is nil the engine
  stores plaintext. This exists for tests; production must wire
  `crypt.KeyStore`.
- **Metadata is not encrypted.** Links, guids, categories, source names/URLs,
  author and timestamps are stored in the clear so that indexing, retention and
  ACL gating work. `ItemMatches` can search them.
- **ACL is only as good as the caller.** The package compares classes; it does
  not authenticate readers. A handler that forgets to set `Query.AclClass` on an
  unauthenticated path gets the unrestricted view. Use `AllowedACL` /
  `AclClass: ACLPublic` on public endpoints.
- **No feed authenticity.** The package does not verify signatures, DKIM, or the
  identity of a feed; a source serves whatever it serves.
- **The scraper is heuristic, not a parser.** It is a regexp tag walk with no
  DOM, no sanitizer guarantees beyond script/style removal, and it can miss
  content or mis-extract. Treat scraped content as untrusted text.
- **URL scheme is not restricted here.** `AddSource` requires a parseable URL
  with a scheme and host, but does not require `https`; the web layer's policy is
  authoritative. Link/URL *content* inside a feed is stored, not fetched.
- **Retention is time-based, not a data-minimisation guarantee.** Items with
  neither `Published` nor `Updated` are dropped by the ingest window because
  both are before the cutoff; items inside the window remain until pruned.
- **Per-source cadence, not global rate limiting.** `FetchAll` skips sources
  fetched within `IntervalMin` unless `force` is set; there is no cross-client
  fetch budget in this package.
- **Background loop depends on the caller.** `Run` only refreshes what `Stale`
  reports and stops when its context is cancelled.

## Dependency note

The non-standard-library import is the in-repo `azzurrotech/stenella/netguard`
package, itself standard-library only. The `crypt` package is referenced only by
tests and by callers that supply a `Cipher`; it is not imported by `feed` itself.

## Reporting

Report suspected vulnerabilities privately to **security@azzurro.tech**. Do not
open a public issue, and do not include live bearer tokens, private source URLs
or decrypted user content in a report.
