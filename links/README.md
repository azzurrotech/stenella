# links — link tracking and token-protected sharing

**Module** `azzurrotech/stenella` · standard library plus the in-repo
`atpclient` package

`links` implements stenella's relationships between feed elements and its public
share records. Each link or share has two halves: a durable row in the client's
pod namespace, and a filesystem *junction* directory of real symbolic links that
physically connect the pod records on disk.

## Overview

The package belongs to the `azzurrotech/stenella` module (Go 1.22). It is called
from the `web` package:

- `web/web.go` constructs both managers (`links.New(cfg.Root, atp)` and
  `links.NewShares(cfg.Root, atp)`), and points the store's `TitleResolver` at
  the feed engine so link lists can show decrypted titles.
- `web/feedapi.go` uses links and shares to render the graph and share pages.
- `web/retention.go` walks link edges (`Store.Edges`) when sweeping.

The pod pass-through (`atpclient.Client`) is the source of truth for the
database half; the junction is a readable on-disk signal that a link or share
points at a specific record and that goes stale when the record moves or dies.
`TitleResolver` exists because item bodies are encrypted at rest: the pod row
carries only a title envelope, so anything that wants to *display* a title must
go through the feed engine's in-memory plaintext copy.

## Public API

### Link constants

- `const ToItem = "item"` — the link's second end is a feed element
  (`<client>/items`).
- `const ToURL = "url"` — the second end is any external URL, stored as a text
  file plus a symlink.

### Link types and Store

- `type Link struct` — `ID`, `FromID`, `ToKind`, `ToID`, `ToURL`, `Relation`,
  `Label`, `Created`, plus enrichment `FromTitle`, `FromLink`, `ToTitle`,
  `ToLink`.
- `type Store struct` — manages link junctions and pod records per client.
- `type TitleResolver interface` — `ItemTitle(client, id string) (title, link
  string, ok bool)`.

Functions and methods:

- `func New(root string, atp *atpclient.Client) *Store` — root must be the same
  data root atp uses.
- `func ItemsTable(client string) string` — `client + "/items"`.
- `func LinksTable(client string) string` — `client + "/links"`.
- `func SharesTable(client string) string` — `client + "/shares"`.
- `func (s *Store) EnsureSchema(client string) error` — create the links and
  shares tables if absent.
- `func (s *Store) Create(client, fromID, toKind, toID, toURL, relation, label
  string) (*Link, error)` — validate both ends through atp, write the pod
  record, materialize the junction.
- `func (s *Store) Edges(client string) ([]Link, error)` — all links without
  title enrichment (single table query, for graph walks).
- `func (s *Store) List(client string, limit, offset int) ([]Link, error)` —
  newest first, enriched with both ends' titles.
- `func (s *Store) Get(client, id string) (*Link, error)`.
- `func (s *Store) Delete(client, id string) error` — removes the record, then
  the junction contents and directory.
- `func (s *Store) SetTitleResolver(r TitleResolver)` — safe before or after
  use.

### Share constants and types

- `const ShareItem = "item"`, `ShareLink = "link"`, `ShareTable = "table"`.
- `type Share struct` — `ID`, `Kind`, `Target`, `Title`, `Token`, `Created`,
  `Expires`, `URL`, `JSONURL`.
- `func (sh *Share) Valid(presented string) bool` — constant-time token check
  plus expiry.
- `type Shares struct` — records + junctions + a public index (share id →
  client).

Functions and methods:

- `func NewShares(root string, atp *atpclient.Client) *Shares` — loads the index.
- `func (s *Shares) Create(client, kind, target, title string, days int)
  (*Share, error)` — `days <= 0` means no expiry.
- `func (s *Shares) ClientFor(id string) (string, bool)` — resolve owner client
  without knowing it up front.
- `func (s *Shares) Get(client, id string) (*Share, error)` — includes the token.
- `func (s *Shares) List(client string, limit, offset int) ([]Share, error)` —
  newest first, token omitted.
- `func (s *Shares) Delete(client, id string) error` — record, junction and
  index entry.

## Usage

```go
cli := atpclient.New(svc.Handler(), "admin", "pw")
if err := cli.Login(); err != nil {
    return err
}
store := links.New(cfg.Root, cli)          // cfg.Root, not cfg.Root/stenella
if err := store.EnsureSchema("acme"); err != nil {
    return err
}
ln, err := store.Create("acme", fromItem, links.ToItem, toItem, "", "mentions", "see also")

shares := links.NewShares(cfg.Root, cli)
sh, err := shares.Create("acme", links.ShareItem, itemID, "a great story", 7)
// present sh.Token on the public share URL; server calls sh.Valid(presented)
```

## Configuration / inputs

- Junction layout: `<root>/stenella/links/<client>/<id>/` with `manifest.json`
  and symlinks `from.xml` / `to.xml`; `<root>/stenella/shares/<client>/<id>/`
  with `manifest.json` and a `target` symlink.
- Symlink targets: `<root>/pod/<client>/items/<id>.xml` for items,
  `<root>/pod/<client>/links/<id>.xml` for links, and the table directory
  `<root>/pod/<client>/<table>/` for a table share. A `url` link writes
  `to.url` and points `to.xml` at it.
- `Store.Edges` queries with `Limit: 100000`; `List` orders by `created`
  descending and applies `limit`/`offset` only when positive.
- Share tokens are 16 random bytes hex-encoded (32 chars); a random-read failure
  falls back to the Unix nanosecond clock.
- Public index: `<root>/stenella/shares/index.json`, mode `0644`.

## Testing

From `/home/matthew/Projects/Platform/stenella`:

```bash
go test ./links/...
```

The suite stands up an in-process atp/pod stack, then covers item and URL link
creation and their junction symlinks (targets resolve, mode is symlink), title
enrichment, `Get`/`List`/`Delete` and record cleanup, URL validation and
identifier validation, `safe` filename sanitization, the item/table share
lifecycle with `ClientFor`, table-directory targets, token validation (real,
wrong, empty, expired, non-expiring) and that `List` omits tokens, path-traversal
rejection for share targets, and index persistence across a fresh manager.

## Design notes

- **Pod is authoritative; junctions are connective tissue.** `Create` writes the
  pod record first. A junction failure is surfaced as an error but the record
  remains; `Delete` removes the record first, then the symlinks (symlinks
  before `os.Remove(dir)`).
- **Symlink-escape guard.** `recordSymlink` cleans the target and requires it to
  sit under `<root>/pod/`, returning "symlink target escapes the pod root"
  otherwise.
- **Identifier validation is separate from sanitization.** `validIdentifier`
  rejects empty, `"."`, `".."`, `/\?#%`, control characters and values over 256
  bytes; `safe` additionally maps unsafe runes to `_` for filenames (dots are
  allowed, so `safe("..")` is `".."` — validation runs first).
- **Two reads for two jobs.** `Edges` is a single table query for traversal;
  `List` pays one pod read per endpoint to enrich titles.
- **Token secrecy in list views.** `List` never sets `Share.Token`; `Get` does,
  for the owner's management view.
- **No update operation.** Links and shares are created and deleted, not
  mutated.

## Dependency note

Imports the Go standard library plus the in-repo `azzurrotech/stenella/atpclient`
package. No third-party modules.
