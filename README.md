# stenella — AzzurroTech Data Platform

**MIT License © Azzurro Technology Inc.** · standard library only · no external
Go modules, no JS frameworks, no build tooling.

stenella is the AzzurroTech data platform. It embeds **atp** (the orchestrator,
which itself embeds **song**, **pod** and **shepherd**) in-process and uses it
as middleware: every request flows through atp's auth, scoping, usage logging
and billing layer exactly like a real client's would. Any atp feature can be
managed from stenella's UI.

On top of that, stenella owns:

- **One feed out of many** — aggregate RSS 2.0, RSS 1.0 (RDF), Atom,
  JSON-Feed and generic web pages into a single newest-first feed, with search,
  category and source filters, de-duplication, retention pruning and OPML bulk
  import.
- **Encrypted at rest** — every item title/summary/body and every comment body
  is stored as an AES-256-GCM ciphertext envelope (`*_enc` columns). Item
  bodies are decrypted in the browser; comments are encrypted *before* they are
  submitted, so the server stores what it was given and cannot read it.
- **Collaboration** — comments, pins and a link graph between items, all held in
  the client's own pod namespace.
- **Retention** — an hourly sweep reclaims aged-out items, honouring pin, comment
  and link exceptions, and writes tombstones so a browser can drop the plaintext
  it cached.
- **Website hosting & management** — every client gets a siloed static site
  (song store) served at `/c/{client}/`, managed from the portal file editor.
- **A search/link homepage** — `/` is a live interface over everything hosted,
  not a static file.
- **Shareable links** — token-protected URLs (`/s/x/{id}?t=…`) that render an
  item, a link, or a whole table (vidi cards), and `combined.xml`/`combined.atom`
  feeds that can be dropped into any reader.
- **Link tracking** — every feed element is a pod database row; relationships
  between rows (and to external URLs) are materialized as real symlink junctions
  on disk, written through atp's pod pass-through.

## Layout

```
stenella/                 # this module (superproject)
├── main.go               # flags + embedded templates/libs; port 8084 default
├── atp/                  # submodule — the embedded orchestrator (song/pod/shepherd)
├── static/               # veni, vidi, vici, vini — the four JS submodules
│   ├── veni/             #   web-component identification & registration
│   ├── vidi/             #   pod output rendering (cards + pagination)
│   ├── vici/             #   cookies + client-side AES-256-GCM encryption
│   └── vini/             #   workflows / data-passing management
├── crypt/                # content-key store + v1 envelope seal/open
├── web/                  # stenella HTTP layer (pages, portal API, admin API)
│   ├── collab.go         #   comments, pins, item links (pod-backed)
│   ├── retention.go      #   hourly sweep + tombstone log
│   ├── collabapi.go      #   collaboration / retention / vault / usage API
│   └── signup.go         #   public self-service client provisioning
├── feed/                 # source management + combined-feed engine
│   └── scraper.go        #   heuristic generic-site extraction
├── links/                # link junctions + token-protected shares
├── billing/              # income aggregation from atp usage logs
├── atpclient/            # in-process client for the embedded atp surface
├── scripts/standalone.sh # proves pod/shepherd/song build with no shared code
```

## Data model

Every record lives inside the owning client's pod namespace. The split that
matters is which fields are **envelope** (clear) and which are **ciphertext**:

| Record | Table | Envelope (clear) fields | Ciphertext fields |
|---|---|---|---|
| item | `<client>/items` | id, source id/name, link, guid, author, categories, published, fetched, `acl_class`, bytes, pin state | `title_enc`, `summary_enc`, `content_enc` |
| comment | `<client>/comments` | id, item ref, author token, created_at, bytes | `body_enc` |
| pin | `<client>/pins` | id, item ref, created_by, created_at | — |
| link | `<client>/links` | id, from_id, to_kind, to_id, to_url, relation, label, created | — |
| client | `<client>/client` | id, name, signup date, content key id | — |

Only envelope fields are indexed, so ACL gating, retention and search never
require reading plaintext.

## Encryption, and where the boundary actually is

- **Encrypted at rest, always.** Bodies are sealed with a per-client 32-byte
  content key using the wire format `v1.<salt>.<iv>.<ct>` — PBKDF2-SHA256
  ×100k → AES-256-GCM. That is exactly what `vici.encrypt`/`vici.decrypt` speak,
  so the browser opens an envelope with the stock library and no glue format.
- **Encrypt-before-submit for authored content.** A comment body is encrypted in
  the browser and POSTed as ciphertext. The server stores the payload verbatim:
  it cannot read, index, log or modify it.
- **No server-side plaintext search.** Free-text queries match envelope metadata
  only (source, categories, author, link host, guid). Full-text search over
  decrypted bodies runs client-side.
- **Documented weakening.** Because ingestion is server-side, a server that
  fetches a feed and renders an RSS export *can* read what it fetched. The claim
  is "encrypted at rest", not "the server never sees plaintext". The content key
  is released only to an authenticated session of that client, so per-device
  envelope re-keying for multi-device access remains open work.

## Access classification

Three classes, mirroring shepherd's directory tree:

| Class | Who reads it | Unauthenticated endpoint |
|---|---|---|
| `public` | anyone | served |
| `private` | the owning client's authenticated session | withheld |
| `protected` | the owning client plus explicitly named clients | withheld |

The class is stamped on the **item** at ingest time, not resolved from the source
at read time, so reclassifying a source never rewrites history and an item cached
as public cannot be served after its source is tightened. The unauthenticated
feed endpoints pin the class to `public` server-side and ignore any
caller-supplied `acl` parameter.

## Retention

The sweep runs hourly as a named workflow, so it inherits logging and usage
accounting. The default window is 24h (per source `retention_days`, clamped to
`[1, 365]`), with three exceptions: an item with a pin, an item referenced by any
comment, and an item reachable from a surviving item through the link graph. A
sweep drops matching rows from the cache **and** from pod, then writes a
tombstone the browser polls so its cached plaintext can be dropped too.

## Building

```bash
go build -o stenella-server ./main.go
```

`go.mod` is `require`-free: only our own modules (`atp`, and hence `pod`,
`shepherd`, `song`) are involved, replaced by their submodule worktrees.
`go.sum` is intentionally empty.

## Running

```bash
export STENELLA_SECRET='a-master-secret-at-least-32-bytes-long!!'
STENELLA_ADMIN_PASSWORD='change-me' ./stenella-server \
  --root=/app/data --port=8084 --libs-dir=static
```

Flags (env fallbacks in parentheses):

| Flag | Default | Purpose |
|---|---|---|
| `--port` | `8084` | listen port |
| `--root` | `./data` | shared data root (atp stores, pod records, feeds, links, shares) |
| `--secret` | *(required)* `$STENELLA_SECRET` | master AES/HMAC secret, ≥ 32 bytes |
| `--admin-user` | `admin` | atp administrator username |
| `--admin-pass` | *(required)* `$STENELLA_ADMIN_PASSWORD` | atp administrator password |
| `--public-base` | `""` | absolute base URL for share links (e.g. `https://azzurro.tech`) |
| `--libs-dir` | `static` | directory with the `veni/vidi/vici/vini` JS worktrees |
| `--host-site` | azzurro.tech and www.azzurro.tech | map a public host to a client site at `/`; repeatable (`host=client`) |
| `--platform-path` | `/platform` | mapped-host path that redirects to that client's portal |

## Web surfaces

| Path | Who | What |
|---|---|---|
| `/` | public | search / link homepage over everything hosted |
| `/s/portal?c={client}` | client | the client portal (feed, database, sites, links, shares, billing) |
| `/s/admin` | admin | the super-admin console (clients, secrets, feeds, income) |
| `/s/feed/{client}` | public | a client's combined feed as HTML |
| `/s/feed/{client}/combined.xml` / `combined.atom` | public | the aggregator output for any RSS/Atom reader |
| `/s/feed/{client}/items` | public | JSON item stream of a combined feed (`q`, `category`, `source`, `since`, `page`, `pageSize`) |
| `/s/x/{id}?t={token}` | public | share page (item / link / vidi-rendered table) |
| `/s/api/x/{id}?t={token}` | public | same share as JSON (vidi `dataSource`) |
| `/c/{client}/` | public | the client's hosted website (song silo) |
| `/s/signup`, `POST /s/api/signup` | public | self-service client provisioning (rate limited, answers once per client id) |
| `/login`, `/logout`, `/s/api/client/login` | — | admin + client sessions |

On a host configured with `--host-site`, the client silo is served at `/`;
`/s/**` remains platform-owned and the normalized `--platform-path` redirects
to `/s/portal?client={client}`. Host matching ignores case, a trailing DNS dot,
and any valid numeric port. Only the platform path and its `/`-delimited descendants
redirect, so a page such as `/platformish` remains a client-site URL. The host
dispatcher runs before ATP: a mapped host cannot address another client's
`/c/{client}/...` silo, and disabled clients are not publicly served.

### Honest integration boundaries

- The `azzurrotech` checkout is a browser-local VINI demonstration. It does not
  capture payment, create a server-side order, send an invoice, or provide a
  consumer/WooCommerce account system. A real purchase requires a separately
  implemented server-side order and payment flow.
- The public site-data endpoint is read-only JSON for the named client's pod
  namespace. Management tables and share tokens are not public; unsafe path
  segments are rejected before the HTTP mux can normalize them.
- RSS/Atom and the JSON item endpoint are the supported public content APIs.
  WordPress `wp-json`, WooCommerce, and oEmbed endpoints are not implemented.
- The four Emperor42 browser libraries are standard-library/static assets. The
  optional Go demos under `static/` are developer services: they default to
  loopback, and a non-loopback bind requires their documented API token.

### Portal API (client session or admin impersonation) — `client` query param

- feeds: `GET/POST /s/api/portal/feeds`, `PUT/DELETE …/feeds/{id}`,
  `POST …/feeds/{id}/fetch`, `POST …/import` (OPML), `POST …/refresh`,
  `GET …/items`
- database (pod): `GET …/db/tables`, `GET/POST …/db/table`, `DELETE …/db/record`
- links: `GET/POST …/links`, `DELETE …/links/{id}`
- shares: `GET/POST …/shares`, `DELETE …/shares/{id}`
- sites (song): `GET …/sites/meta`, `GET …/sites/files`, `POST/PUT …/sites/file`,
  `DELETE …/sites/file`
- vault: `GET/POST/DELETE …/secrets`, `GET/PUT …/payment`,
  `GET …/vault` (the content key, for that client only)
- billing: `GET …/billing`, `GET …/usage`
- keys (shepherd): `POST …/keys`, `GET …/keys/verify`, `POST …/revoke`
- collaboration: `GET/POST …/items/comments`,
  `DELETE …/items/comments/{id}`, `POST/DELETE …/items/pin`,
  `GET …/items/pins`
- retention: `GET …/retention`, `POST …/retention/sweep`
- identity: `GET …/whoami`, `POST /s/api/client/login|logout`

A comment body is posted already encrypted (`body_enc`), so the portal API stores
and returns ciphertext. Comment reads return the envelope; decrypting it is the
browser's job.

### Admin API (atp session)

`GET /s/api/admin/summary|income|clients|feeds|config|usage|retention`,
`POST /s/api/admin/client`, `PUT/DELETE /s/api/admin/client/{id}`,
`GET/POST/DELETE /s/api/admin/secrets`, `PUT /s/api/admin/income`,
`PUT /s/api/admin/config`, `POST /s/api/admin/retention/sweep`.

## Storage layout under `--root`

```
data/
├── clients.json            # atp client registry (atp-managed)
├── secrets/                # AES-256-GCM vault (atp-managed)
├── logs/                   # per-client usage logs (atp-managed)
├── pod/{client}/{table}/   # pod filesystem database — records are <id>.xml
├── song/{client}/          # siloed static sites
├── stenella/
│   ├── feeds/{client}.json # feed source configs
│   ├── cache/{source}.json # fetched item caches (retention-pruned)
│   ├── links/{client}/{id}/# link junctions — symlinks to the pod records
│   └── shares/
│       ├── index.json      # public share id → client index
│       └── {client}/{id}/  # share junctions — symlink to the shared record/table
```

Links and shares are **physical**: a link junction holds `from.xml` → and
`to.xml` → symlinks into `data/pod/{client}/items/`; a share junction's `target`
is a symlink to the shared record or table directory. A stale symlink is the
on-disk signal that the pointed-at row moved or died — that's the "tracking".

## Front end

Portal, admin, feed, share and signup pages are plain HTML rendered from
`go:embed`-ed templates with vanilla `app.js`. The four Emperor42 libraries
(`static/{veni,vidi,vici,vini}`) are loaded into memory at startup
(`--libs-dir`) and served at `/s/static/…`:

- **veni** discovers and registers custom elements;
- **vidi** renders pod tables supplied by `/s/api/x/{id}?t=…` shares;
- **vici** owns all cookies and client-side encryption;
- **vini** drives multi-step flows (portal login, wizards) with persisted
  `vini_workflows` progress.

`web/static/collaboration.js` is a second bundle, loaded after the libraries and
only where the browser holds a content key (the portal items pane). It fetches
the passphrase once, opens each `*_enc` envelope with vici, encrypts comment
bodies before POST, filters decrypted text client-side, and polls for tombstones
so a swept item is deleted rather than merely hidden. Without vici it still
renders metadata — it just cannot show bodies, post comments or search text.

## Tests

```bash
go build ./... && go vet ./... && staticcheck ./...
go test ./...        # feed parsing/combine, scraping, link+share junctions, web walkthrough
go test -race ./...  # same, under the race detector
./scripts/standalone.sh   # pod, shepherd and song each build/vet/test alone
```

The web test drives a full admin → client → portal → feed → link → share →
billing → income cycle against an in-process atp service and verifies the
symlink junctions on disk. Separate tests cover the collaboration surface
(comment ciphertext round-trip, pin idempotence, link survival set, retention
sweep + tombstones, content-key client scoping, signup, usage dashboard), ACL
classification and the public feed's refusal to serve private or protected items,
and the static asset embed.

`scripts/standalone.sh` is a shell script rather than a Go test on purpose: a
test inside one module cannot see what the *other* modules require, so it could
pass while the module boundary rotted. `web/standalone_test.go` asserts the same
properties from inside the toolchain, so a plain `go test ./...` still catches a
regression.

## Container

`Dockerfile` builds the same binary (`EXPOSE 8084`). Run with:

```bash
docker build -t stenella .
docker run --rm -p 8084:8084 \
  -e STENELLA_SECRET='a-master-secret-at-least-32-bytes-long!!' \
  -e STENELLA_ADMIN_PASSWORD='change-me' \
  stenella
```

TLS termination is expected at a Caddy reverse proxy in front (see
`super/stenella_atp/Caddyfile` for the historical routing table; the canonical
service→port map is in the workspace AGENTS.md, §8).