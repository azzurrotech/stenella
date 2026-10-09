# feed — stenella's single-feed engine

**Module** `azzurrotech/stenella` · standard library plus the in-repo
`netguard` package

`feed` fetches many RSS/Atom/RDF/JSON sources and web pages per client,
normalizes and merges them into one newest-first feed, caches it on disk for
offline use and instant pagination, and serves envelope-only search over
metadata while item bodies stay encrypted at rest.

## Overview

The package belongs to the `azzurrotech/stenella` module (Go 1.22). It is called
from the `web` package:

- `web/web.go` builds the engine (`feed.New(feed.Options{...})`) and supplies a
  `crypt.KeyStore` as the `Cipher`.
- `web/feedapi.go`, `web/api.go`, `web/admin.go`, `web/pages.go` and
  `web/collabapi.go` read the engine (sources, combined feed, items, counts).
- The engine's default HTTP client dials through `netguard.NewTransport()`, so
  user-supplied feed URLs cannot reach loopback/private/link-local targets.

Parsing is done with `encoding/xml` and `encoding/json`; the HTML scraper is a
minimal regexp walk. Bodies sealed through the `Cipher` become vici-compatible
AES-256-GCM envelopes (see the `crypt` package). When `Cipher` is nil the engine
still works but stores plaintext, which is only appropriate for tests —
production always wires `crypt.KeyStore`.

## Public API

### Source kinds and ACL classes

- Kinds: `KindRSS="rss"`, `KindAtom="atom"`, `KindRDF="rdf"`, `KindJSON="json"`,
  `KindOPML="opml"` (import-only), `KindSite="site"` (heuristic scrape).
- Classes: `ACLPublic="public"`, `ACLPrivate="private"`,
  `ACLProtected="protected"`.
- `func ValidACLClass(c string) bool`.
- `func NormalizeACLClass(c string) string` — anything not explicitly private or
  protected becomes `public`.
- `func PageSizeDefault() int` — returns `40`.

### Parsers

- `func ParseRSS(data []byte) ([]Item, error)` — RSS 2.0 and RSS 1.0/RDF.
- `func ParseAtom(data []byte) ([]Item, error)`.
- `func ParseJSONFeed(data []byte) ([]Item, error)` — RFC-8939-style object, or
  a bare array/object of items.
- `func ParseOPML(data []byte) ([]Source, error)` — extracts every `xmlUrl`.
  `OPMLOutline` is the exported outline shape.

### Types

- `Source` — one subscription (id, name, url, kind, enabled, `IntervalMin`,
  `AuthSecret`, `AclClass`, `Retention`, status fields, timestamps).
- `Item` — one normalized element. `Title`/`Summary`/`Content` are plaintext and
  never serialized; `TitleEnc`/`SummaryEnc`/`ContentEnc` are the stored
  envelopes; `ContentBytes` is the plaintext body length; `ACLClass`,
  `CommentCount` and `Source *ItemSource` travel with the item.
- `Envelope` — the wire form for callers holding the content key (ciphertext
  bodies, no plaintext fields).
- `ItemSource` — the reader-safe projection of a `Source`.
- `FetchedFeed` — `Source` plus the `Items` produced by one fetch.
- `Result` — per-source outcome (`Source`, `Status`) from `FetchAll`.
- `Page` — `Items`, `Total`, `Page`, `HasMore`.
- `Query` — `Page`, `PageSize`, `Q`, `Source`, `Category`, `Since`, `AclClass`.
- `Options` — `Root`, `HTTP`, `Cipher`, `Now`.
- `Cipher interface` — `Seal(client, plaintext string) (string, error)` and
  `Open(client, payload string) (string, error)`.
- `SecretResolver func(client, name string) (string, error)`.
- `ScrapeError` with `Error() string` and `Unwrap() error` (returns
  `ErrNoScrapableContent`).
- `ErrNoScrapableContent` sentinel.
- `func (it Item) Envelope() Envelope`, `func Envelopes(items []Item) []Envelope`.

### Engine

- `func New(opts Options) (*Engine, error)`.
- `func (e *Engine) Sources(client string) []*Source` and
  `GetSource(client, id string) *Source` — copies, safe to hold off-lock.
- `AddSource(client, name, rawURL, kind string, interval int, authSecret string,
  retention int) (*Source, error)`.
- `AddSourceClassified(..., aclClass string) (*Source, error)`.
- `AddSources(client string, srcs []Source) []*Source`.
- `UpdateSource(client, id string, patch map[string]any) error`.
- `DeleteSource(client, id string) error`.
- `Fetch(client string, src *Source, resolve SecretResolver) (*FetchedFeed, error)`.
- `FetchAll(client string, resolver SecretResolver, force bool) []Result`.
- `Stale(client string) bool`.
- `Combined(client string, q Query) Page`.
- `Categories(client string) []string`.
- `Expired(client, sourceID string, cutoff time.Time) []string`.
- `Prune(client, sourceID string, ids []string)`.
- `ItemTitle(client, id string) (title, link string, ok bool)`.
- `ItemByID(client, id string) *Item`.
- `BumpCommentCount(client, id string) bool`.
- `Run(ctx context.Context, resolver SecretResolver)`.
- `Clients() []string`.

### Free functions

- `func GuessKind(p string) string` — kind from a path/URL heuristic.
- `func ItemMatches(it Item, q string) bool` — case-insensitive substring over
  clear metadata only.
- `func MetadataSearchable(q string) bool` — whether a query can be answered
  from the envelope (false for multi-word prose).
- `func ItemTime(it Item) time.Time` — `Published`, else `Updated`, else
  `Fetched`.
- `func AllowedACL(it Item, allowed ...string) bool` — no arguments means
  public only.

## Usage

```go
e, err := feed.New(feed.Options{Root: "./data", Cipher: keys})
if err != nil {
    return err
}
src, err := e.AddSourceClassified("acme", "Example",
    "https://example.test/feed.atom", "", 15, "", 30, feed.ACLPublic)
if err != nil {
    return err
}
if _, err := e.Fetch("acme", src, nil); err != nil {
    return err
}
page := e.Combined("acme", feed.Query{Page: 1, PageSize: 40, AclClass: feed.ACLPublic})
```

## Configuration / inputs

- Defaults: `defaultIntervalMin = 15`, `defaultPageSize = 40`,
  `defaultRetentionDays = 30`. Default root is `"./data"`.
- Default HTTP client: 20 s timeout; a fetcher reads at most `16<<20` (16 MiB)
  per response. The scraper truncates HTML at `scraperMaxBytes = 4<<20` (4 MiB)
  and summaries at `scraperSummaryLimit = 4000`; Atom summary fallback is cut at
  800 bytes. Background `Run` ticks once a minute.
- Data layout: sources and cache under `<root>/feeds/` and `<root>/cache/`,
  written atomically via temp file + rename.
- `Source.AuthSecret` is a *name*; the value is resolved by `SecretResolver` and
  sent as `Authorization: Bearer <value>`.
- Fetch request headers: `User-Agent: stenella/1.0 (+https://azzurro.tech)` and
  an Accept list preferring RSS/Atom/JSON feed types.
- Retention: items are kept at ingest if `Published.After(cutoff)` **or**
  `Updated.After(cutoff)`, where `cutoff = now - retention*24h`. `Prune` removes
  exactly the ids it is given.

## Testing

From `/home/matthew/Projects/Platform/stenella`:

```bash
go test ./feed/...
```

The suite covers each parser (RSS, RDF, Atom, JSON feed and array, OPML), kind
guessing, stable 16-hex item ids, tolerant date parsing, `MetadataSearchable`,
encryption-at-rest of the cache (asserting body words are absent and envelope
metadata present), `ContentBytes`, retention `Expired`/`Prune`, combined-feed
ordering/pagination/search/category/source filters and dedup, ACL
normalization/classification/filtering, scraper extraction (OpenGraph
precedence, script/style stripping, no-headline error, undated freshness,
oversized-page truncation), body-based routing, and the injectable clock.
`TestMain` sets `netguard.AllowPrivate = true` so fixtures can be served on
`127.0.0.1`; the shipped binary does not.

## Design notes

- **Two-file model per source.** Source config lives in
  `<root>/feeds/<client>.json`; each source's items live in
  `<root>/cache/<sourceID>.json`. Both are written with `atomicWrite`
  (temp file + `os.Rename`).
- **Metadata clear, bodies opaque.** Envelope metadata (ids, guids, links,
  categories, source name/URL, timestamps) is plaintext so ACL gating, ordering,
  retention and server-side search work without opening bodies; body text is
  sealed. `ItemMatches` deliberately never reads `Title`/`Summary`/`Content`.
- **Seal-then-copy.** `saveCache` seals a *copy* of the items and copies the
  envelopes back onto the live items, so an in-memory item keeps its plaintext
  while its stored form matches `Item.Envelope`.
- **Fail-closed on seal error.** Items whose bodies failed to seal are dropped
  from the file rather than written in the clear.
- **ACL is stamped, not looked up.** An item carries its effective class from
  ingest; reclassifying a source never rewrites cached history, and a public
  reader is gated on the item's own class.
- **Idempotent ids and dedup.** `itemID` is the first 16 hex chars of
  SHA-256 over `client|source|guid|link`; `Combined` dedups by that id.
- **Ordering.** Newest-first by `ItemTime`, ties broken by ascending id;
  `Combined` treats `PageSize <= 0` as the default and clamps `Page < 1`.
- **Concurrency.** The engine is safe for concurrent use; `Run` refreshes stale
  sources in the background, and `FetchAll` marks an in-flight
  `client/source` key so two refreshers do not race the same source.
- `KindRDF` is declared but `GuessKind` never returns it; RDF documents are
  handled by `ParseRSS`.
