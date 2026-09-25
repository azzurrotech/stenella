# stenella — AzzurroTech Data Platform

**MIT License © Azzurro Technology Inc.** · standard library only · no external
Go modules, no JS frameworks, no build tooling.

stenella is the AzzurroTech data platform. It embeds **atp** (the orchestrator,
which itself embeds **song**, **pod** and **shepherd**) in-process and uses it
as middleware: every request flows through atp's auth, scoping, usage logging
and billing layer exactly like a real client's would. Any atp feature can be
managed from stenella's UI.

On top of that, stenella owns:

- **One feed out of many** — aggregate RSS 2.0, RSS 1.0 (RDF), Atom and
  JSON-Feed sources into a single newest-first feed, with search, category and
  source filters, de-duplication, retention pruning and OPML bulk import.
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
├── web/                  # stenella HTTP layer (pages, portal API, admin API)
├── feed/                 # source management + combined-feed engine
├── links/                # link junctions + token-protected shares
├── billing/              # income aggregation from atp usage logs
├── atpclient/            # in-process client for the embedded atp surface
```

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
- vault: `GET/POST/DELETE …/secrets`, `GET/PUT …/payment`
- billing: `GET …/billing`, `GET …/usage`
- keys (shepherd): `POST …/keys`, `GET …/keys/verify`, `POST …/revoke`
- identity: `GET …/whoami`, `POST /s/api/client/login|logout`

### Admin API (atp session)

`GET /s/api/admin/summary|income|clients|feeds|config`, `POST /s/api/admin/client`,
`PUT/DELETE /s/api/admin/client/{id}`, `GET/POST/DELETE /s/api/admin/secrets`,
`PUT /s/api/admin/income`, `PUT /s/api/admin/config`.

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

Portal, admin, feed and share pages are plain HTML rendered from
`go:embed`-ed templates with vanilla `app.js`. The four Emperor42 libraries
(`static/{veni,vidi,vici,vini}`) are loaded into memory at startup
(`--libs-dir`) and served at `/s/static/…`:

- **veni** discovers and registers custom elements;
- **vidi** renders pod tables supplied by `/s/api/x/{id}?t=…` shares;
- **vici** owns all cookies and client-side encryption;
- **vini** drives multi-step flows (portal login, wizards) with persisted
  `vini_workflows` progress.

## Tests

```bash
go test ./...        # feed parsing/combine, link+share junctions, web walkthrough
go test -race ./...  # same, under the race detector
```

The web test drives a full admin → client → portal → feed → link → share →
billing → income cycle against an in-process atp service and verifies the
symlink junctions on disk.

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