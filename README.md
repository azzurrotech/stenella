# stenella — AzzurroTech open-source intelligence platform

**MIT License © Azzurro Technology Inc.** · module `azzurrotech/stenella`
· **Go standard library only**, plus the embedded **atp** platform (which itself embeds
**song**, **pod** and **shepherd**). Every interface is plain **HTML, CSS and JavaScript** —
no frameworks, no bundlers, no build tooling.

stenella is a single Go server with **two purposes**:

1. **An easy-to-use interface for atp** and all of its associated software, packaged so it can
   be **easily integrated or embedded into other software**.
2. **An open-source intelligence (OSINT) platform** — the **more important** purpose: it
   **combines many sources into one feed**, lets an analyst **link, comment on and annotate**
   what it collects, **group and search** it, and **generate insights from a set of rules** —
   then presents it all through a **dashboard**, a **feed/timeline**, a **map** and an
   **information tree**.

The two are the same server: stenella is an **atp application** — atp is embedded in-process
and provides the fileserver, the serverless-function registry, the secret vault, the storage
(pod), the static hosting and rendering (song) and the capability layer (shepherd). stenella
adds the OSINT model and the views on top; it does **not** re-implement anything atp already
does.

References this document is written against:

- OSINT, the discipline: <https://en.wikipedia.org/wiki/Open-source_intelligence>
- What OSINT is and the intelligence cycle: <https://www.sans.org/blog/what-is-open-source-intelligence>
- The dashboard pattern: <https://www.w3schools.com/projects/project.php?project=DATADASH001>
- The platform it leverages: [`atp/README.md`](atp/README.md)

---

## What stenella is

A running stenella is **one Go binary**. Start it, point a browser at it, and you have an
**installable progressive web application** that:

- **subscribes to sources** — RSS 2.0, RSS 1.0 (RDF), Atom, JSON Feed, and ordinary web pages —
  and **merges them into a single, de-duplicated, newest-first feed**;
- **models every item as a first-class record** in a graph you own, so you can draw **links**
  between items (and to external URLs), attach **comments** and **notes**, and **group** items
  into collections and tags;
- **searches** across that graph — by metadata always, and by content where you have unlocked
  the content key in your browser;
- **runs rule sets** over the graph to produce **insights** — the step that turns raw
  information into intelligence;
- shows all of it through four coordinated views (dashboard, feed/timeline, map, information
  tree) plus the smaller tools an analyst actually uses;
- and, because it embeds **atp**, exposes **atp itself** — its hosted applications, secrets,
  functions, and the song/pod/shepherd surfaces — through the same server and the same
  interface.

It is **identity-free**: it has no account system and does not track readers. A collector owns
their sources and their notes; a shared link is a capability token, not a login.

---

## The two purposes

### Purpose 1 — the atp interface (embeddable)

atp is *"imported, not just run"*. stenella takes that seriously in two directions:

- **stenella uses atp.** It builds one `*atp.Server` (`atp.New`), mounts it as middleware
  (`srv.Middleware(next)`), registers its own serverless functions on it
  (`srv.Handle(method, pattern, fn)`), and reaches the embedded services through the server it
  was handed (`srv.Secrets()`, `srv.Pod()`, `srv.Song()`, `srv.Shepherd()`, `srv.Head()`,
  `srv.Headers()`). Nothing here is a fork of atp.
- **other software uses stenella.** Because a running stenella is itself an `http.Handler`, it
  can be **embedded** the same way atp can: mounted under a prefix of a larger Go server, or
  adopted with `srv.Middleware`-style delegation, and extended by registering additional
  **sources**, **rule sets** and **views** in Go. The OSINT platform is therefore also the
  reference application for building *anything* on atp.

### Purpose 2 — the OSINT platform (the more important purpose)

Open-source intelligence is **not** just collecting public data. SANS puts it plainly:
*"information does not equal intelligence"*. Raw gathered data becomes intelligence only after
it is evaluated, organised, has meaning assigned to it, and is aimed at a **specific
intelligence question**. stenella is built around that distinction:

| Stage | What most feed readers do | What stenella does |
|---|---|---|
| Collection | subscribe to feeds | many feeds → **one** combined feed, plus pages and records |
| Processing | show newest first | normalise, **de-duplicate**, classify, **extract entities & places** |
| Analysis | — | **links, comments, notes**; **groups**; **search** |
| Production | — | **rule-driven insights** ("what does this data matter for?") |
| Dissemination | a list | dashboard, timeline, map, tree, exports, shared reads |

Every stage is a file- or function-backed behaviour in the same Go server, which is what makes
the platform honest about where a claim comes from: an item, a link, a note and an insight are
all records you can inspect, export and cite.

---

## Open-source intelligence, the discipline we build to

### Definition

OSINT is the **collection and analysis of data gathered from open sources** — overt, publicly
available material — **to produce intelligence**. It is used in national security, law
enforcement and business intelligence to answer intelligence requirements. The definitions
stenella is designed around:

- **DHS** — intelligence derived from publicly available information, collected and
  disseminated *promptly* to address *specific* needs.
- **NATO** — intelligence from publicly available information and other unclassified data with
  limited public distribution or access.
- **EU** — collecting and analysing open-source information to generate **actionable**
  intelligence.
- **SANS** — intelligence produced by collecting, evaluating and analysing publicly available
  information **with the purpose of answering a specific intelligence question**.

Two ideas follow directly and shape the whole design:

1. **Information ≠ intelligence.** stenella never presents raw items as conclusions. Items are
   *information*; **insights** are the analysed output, and they always say which items,
   links and rules produced them.
2. **A question comes first.** A **collection** (a group plus its sources and rule sets) is
   tasking: it encodes the intelligence question the analyst is answering.

### The intelligence cycle

stenella's pipeline *is* the intelligence cycle, named the same way:

| Cycle stage | stenella stage | Where it lives |
|---|---|---|
| Preparation / tasking | a **collection** ("the question"): sources, groups, rules | `stenella/collections` records |
| Collection | **aggregate** RSS/Atom/JSON/pages into one feed | `feed` package + fetch functions |
| Processing | **normalise, de-duplicate, classify, extract** entities/places | `process` package |
| Analysis & Production | **links, comments, notes, groups, search**, then **rules → insights** | `graph`, `search`, `rules` packages |
| Dissemination | dashboard, timeline, map, tree, exports, shares | `views` + atp hosting/shares |

The cycle is **iterative**: an insight can spawn a new collection; a new source re-runs the
rules; every stage is observable and re-runnable.

### Source categories

The six OSINT categories of information flow all map to a stenella source kind:

| Category | Examples | stenella source kind |
|---|---|---|
| Media | newspapers, magazines, radio, TV | `feed` (site feeds, transcripts) |
| Internet | online publications, blogs, social media, video | `feed`, `page` |
| Public government data | reports, budgets, hearings, speeches | `feed`, `page` |
| Professional & academic | journals, papers, theses | `feed` |
| Commercial data | assessments, databases, imagery | `feed`, `dataset` (imported) |
| Grey literature | reports, preprints, patents, working papers | `feed`, `page` |

### Passive by default

stenella is **passive**: it reads published material and never engages a subject. It does not
post, comment, follow, like or message on any external platform, and it makes **no outbound
request the operator did not configure**. Active collection is deliberately out of scope: it is
a human decision with legal weight, not a feature the server performs for you (see
[Ethics](#ethics-legality-and-privacy)).

---

## How stenella leverages atp

atp is embedded, not re-implemented. Each atp surface does one job for stenella:

| atp surface | atp API | what stenella uses it for |
|---|---|---|
| server | `atp.New(atp.Options{…})`, `srv.Middleware(next)` | the whole HTTP surface; mounted in-process and embeddable by a host |
| function registry | `srv.Handle(method, pattern, fn)` | fetchers, processors, rule evaluation, geocoding, exports — all as serverless functions bound to verbs + endpoints |
| secrets vault | `srv.Secrets()` | feed credentials, API tokens and share keys — server-side only, never rendered into the page |
| pod | `srv.Pod()` | the record store: sources, items, links, comments, notes, groups, rules, insights |
| song | `srv.Song()` | static hosting, server-side templates and the head/Web-Component injection for the views |
| shepherd | `srv.Shepherd()` | identity-free capability tokens for share links and operator sessions |
| head manager | `srv.Head()` | one place that injects the PWA manifest, OpenSearch document, preload hints and per-view fragments |
| header manager | `srv.Headers()` | the security/privacy/performance header policy for every response |

Because atp already **generates the client-app plumbing** (web app manifest, service worker,
OpenSearch description, cache and security headers, media and WebAssembly delivery), stenella's
front end is a PWA *by construction* and the OSINT code only has to describe its data.

---

## The OSINT pipeline

### 1. Collection — one feed out of many

A **source** is a URL plus a kind and a schedule:

```text
kind: feed | page | dataset      # feed → RSS2/RSS1(RDF)/Atom/JSON Feed; page → generic extraction
url:  https://example.org/feed.xml
collection: elections-2026        # the group / tasking this source serves
every: 15m                        # fetch cadence (fetcher is an atp function)
acl:  public | private | protected
```

The four collection methodologies the discipline names are all covered by these kinds:
**social-media intelligence** and **search-engine data mining/scraping** arrive as feeds or as
extracted pages, **public-records checking** is a feed or page source, and **data-broker
matching** is deliberately out of scope — matching and verification run against records you
import yourself, never against a third-party data broker.

Fetching and parsing are **atp serverless functions**, so they are registered, observable and
replaceable:

```go
srv.Handle("POST", "/api/sources/{id}/fetch", stenella.FetchSource)
srv.Handle("POST", "/api/collections/{id}/refresh", stenella.RefreshCollection)
```

Parsing is **baseline Go**: `encoding/xml` for RSS/RDF/Atom and `encoding/json` for JSON Feed —
no third-party parser. Every fetched item is normalised to one shape (title, summary, body,
author, link, guid, categories, published, fetched, source, collection) and **merged into a
single combined feed** that is both a page and a machine-readable document:

```text
/feeds/{collection}.xml     # the aggregated RSS/Atom any reader can subscribe to
/feeds/{collection}.json    # the same items as JSON for the app and for exports
```

**De-duplication** is by stable identity (guid, then canonical link, then a content hash), so
the same story from five wires collapses to one item with **five source attributions** — the
first step from information toward intelligence.

#### The combined feed that can be shared — implemented

Collection is live today as **one URL you can hand to anybody**:

```text
https://stenella.example.org/stenella/feeds/all.xml
```

`GET /stenella/feeds/all.xml` fetches every configured source, merges them into a single
RSS 2.0 document and serves it with no account, no token and no cookie — atp's access model
adds no security to hosted resources, so the URL is the whole interface. `all` is the
collection name for the configured source set, which makes the endpoint the
`/feeds/{collection}.xml` shape described above.

The sources behind that example URL are:

| Source | Origin feed |
|---|---|
| **RadioItalia (RAI)** | `https://www.radioitalia.it/rss.xml` |
| **ANSA** | `https://www.ansa.it/sito/ansait_rss.xml` |
| **CBC** | `https://www.cbc.ca/webfeed/rss/rss-topstories` |
| **Communications Security Establishment** | the Government of Canada news API (`dept=communicationssecurity`) |
| **Government of Canada** | the same API, national news feed |

**It contains no links and no private elements.** That is the purpose of the endpoint: it is
safe to give to a partner, a bot or a stranger. Each item is reduced to a title, a plain-text
summary, a publication date and the source **name**; everything else is removed before the
document is rendered:

| Removed — links | Removed — private elements |
|---|---|
| `<link>` and `<guid>` URLs, `<enclosure url>`, `<comments>`, `<docs>`, `<cloud>` | `<author>` / `<dc:creator>` names, e-mail addresses, `@handles` |
| Atom `<id>` (a URL) and `<atom:link rel="self">` | `<managingEditor>` / `<webMaster>`, `<copyright>` |
| any `http(s)`, `ftp` or `mailto` URL, any `www.` host or bare domain inside a title or summary | the source URLs themselves — provenance survives as the source name |
| HTML anchors, scripts and all other source markup | tracking identifiers and any source-supplied metadata |

The channel is equally bare: no `<link>`, no `<generator>` and no `<docs>`, so the document
that leaves the building fingerprints nothing about the host.

`GET /stenella/api/aggregate` serves the same sanitised item set as JSON, for stenella's own
views and exports.

Operationally the fetcher is deliberately boring and resilient:

- sources are fetched **concurrently**, each with a timeout, one retry and a 4 MiB body cap;
- the merge is de-duplicated by headline, sorted **newest first**, capped per source and
  **cached for five minutes**, so a shared URL is not a way to hammer the wires;
- a source that fails is skipped and reported on the JSON twin (with its URL redacted), and
  if **every** source fails the previous result is still served — a shared feed should not go
  empty because one wire is down.

The source set is configuration, not code. `STELLENA_RSS_SOURCES` is a comma-separated list of
`Name=URL` pairs; when it is unset the five sources above are used:

```sh
STELLENA_RSS_SOURCES='CBC World=https://www.cbc.ca/webfeed/rss/rss-world' ./stenella-server
```

> Operator note: CBC's WAF drops requests whose `User-Agent` carries a contact URL or a
> multi-word comment, so the fetcher sends the bare token `stenella/1.0`. Leave it bare or CBC
> silently stops contributing.

### 2. Processing — normalise, de-duplicate, classify, enrich

After collection, each item is processed (again as an atp function, so it can be re-run):

- **Normalise** — one item shape regardless of source format.
- **De-duplicate** — collapse cross-source duplicates, keeping every attribution.
- **Classify** — assign the `acl` class, categories and any configured labels.
- **Extract** — pull **entities** (people, organisations, places, handles, keywords) and
  **coordinates** where the item carries them, into the graph.
- **Link** — create candidate links (shared entities, near-duplicate content, explicit
  references) for the analyst to accept, reject or extend.

The distinguishing claim of the source categories above holds here too: this is processing, not
conclusions. Nothing becomes an *insight* until a rule says so.

### 3. Analysis — links, comments and notes

An analyst's work is captured as records, not as browser state:

- **Links** — typed, directed edges between two items, or between an item and an external URL:
  `relation` (e.g. `supports`, `contradicts`, `duplicates`, `references`, `follows`),
  a `label`, and who drew it. Links are how "associations between entities" become explicit.
- **Comments** — threaded, attributable annotations on an item, a link, a group or an insight.
- **Notes** — free-form analyst notes, attachable to anything recorded, kept with the
  collection they belong to so a later reader knows *why* something mattered.

Automatic candidate links (from processing) and human links live in the same table with an
`origin` field (`auto` | `human`), so an analyst can always see which is which.

### 4. Grouping and search

- **Groups** (collections, tags, saved queries) organise items without moving them. An item may
  belong to many groups; a group may be a fixed list, a rule ("everything matching X"), or a
  manual selection.
- **Search** is two-layered:
  - **Server-side**, over **envelope metadata** — source, collection, category, author, link
    host, guid, date range, and any extracted entity. This is always available and needs no key.
  - **Client-side**, over **content** — because bodies are stored **encrypted at rest** (pod's
    optional encryption, unwrapped by the browser with the collection's content key), full-text
    search over decrypted text runs in the browser and never sends plaintext to the server.

### 5. Insight rules

An **insight** is a conclusion produced by a **rule set** evaluated over the graph — the
"meaning-making" step the sources insist on. Rules are declarative data (stored as pod records,
editable in the UI), evaluated by an atp function:

```text
rule "cross-source convergence":
  when  3 items from >= 2 distinct sources
        mention entity "Acme Corp"
        within 48h
  then  insight severity=watch  "Acme Corp activity cluster"
        cite  the matched items and links
rule "contradiction":
  when  two items are linked relation=contradicts
  then  insight severity=review  "Conflicting reports"  cite both items
rule "new place":
  when  an item's place is not in any prior item of the collection
  then  insight severity=note  "First mention of <place>"
```

Each insight **cites** the items, links and rule that produced it and carries a **severity** and
a **timestamp**, so an analyst can triage, dismiss with a reason, or promote it to a note. Rules
are the part most worth extending, so the rule schema is deliberately small (match conditions +
aggregate + thresholds) and the engine is a normal Go package:

```go
srv.Handle("POST", "/api/rules", stenella.CreateRule)
srv.Handle("POST", "/api/rules/{id}/run", stenella.RunRule)
srv.Handle("GET", "/api/insights", stenella.ListInsights)
```

### 6. Dissemination

Output leaves stenella as:

- a **combined feed** (RSS/Atom/JSON) per collection;
- **exports** — JSON and CSV of items, links, insights and citations;
- a **timeline** and an **information tree** an analyst can read and screenshot;
- a **share** — a shepherd capability token that grants read-only access to one item, one
  group or one insight, revocable and rate-limited.

---

## The information model

Everything is a pod record in the collection's namespace; every record is a plain XML file you
can read. The model is intentionally small and graph-shaped:

| Record | Is | Key fields (envelope) | Optional ciphertext |
|---|---|---|---|
| `source` | a subscribed origin | kind, url, collection, schedule, acl | credentials |
| `item` | one gathered piece of information | id, source, link, guid, author, categories, published, fetched, acl, place, bytes | `title_enc`, `summary_enc`, `content_enc` |
| `entity` | a person/org/place/handle/keyword | id, kind, name, resolved coordinates (optional) | — |
| `link` | a typed edge | from, to_kind, to_id/to_url, relation, label, origin, created_by | — |
| `comment` | an annotation | on (record ref), author token, created_at | `body_enc` |
| `note` | analyst free text | on, collection, created_at | `body_enc` |
| `group` | a collection / tag / saved query | id, name, kind, predicate | — |
| `rule` | a rule set | id, name, conditions, window, severity | — |
| `insight` | a rule-produced conclusion | id, rule, severity, created_at, citations | — |

The split that matters is **envelope vs ciphertext**: only envelope fields are indexed, so
grouping, filtering, metadata search and rule matching never require reading plaintext. Where a
collection's content key is unlocked in the browser, the encrypted title/summary/body/comment/note
fields are decrypted **client-side**. This follows atp's model: encrypted at rest, and the server
stores what it is given.

---

## Views

All four views are server-side-rendered from `go:embed`-ed templates and enhanced with baseline
JavaScript. They share one head fragment (managed by `srv.Head()`) and one data source (the
collection's records via the JSON endpoints), and each is deep-linkable and offline-capable as
part of the PWA.

### The dashboard view

The dashboard mirrors the **W3Schools "Analytics Dashboard" (`DATADASH001`)** pattern — filters,
KPI cards, and charts that update live as you filter — reimplemented with **baseline** HTML,
CSS and JavaScript:

- **Regions**: a heading, a `#filters` bar, a `#kpis` row and a `#charts` area.
- **Filters**: category, source, collection and date range (plus free-text).
- **KPI cards**: item count, distinct sources, open insights, and time since the newest item
  (each value in a `.kpi-value` element).
- **Charts**: bar (items per source), line (items over time), pie (items per category), drawn
  with the standard **`<canvas>` 2D API** — **no chart library**, which is the one deliberate
  difference from the reference project (whose Chart.js dependency would violate the
  baseline-only rule).
- **Live update**: changing a filter recomputes KPIs and **updates** the existing charts in
  place rather than recreating them.

### The feed / timeline view

The heart of the product: the **combined feed** as a chronological **timeline**, newest first,
with per-item source attribution, category chips, the collection's acl class, and inline
indicators for existing links, comments and notes. From here an analyst can open an item, draw
a link, add a comment or note, add the item to a group, or jump to the same item on the map or
in the tree. It renders the same data as `/feeds/{collection}.xml`, so what you see is what any
RSS reader would get.

### The map view

Items and entities that carry coordinates are placed on a **map**:

- the base map is rendered from a bundled, offline **world outline** drawn with inline SVG or
  `<canvas>` (an equirectangular projection), so the default view makes **no external request**
  and leaks nothing;
- markers are items/entities; clustering groups nearby points; clicking a marker opens the item
  and its links;
- an **optional** richer base (tiles from a geocoder/tile server the operator configures) can be
  enabled explicitly; it is off by default because it is an outbound call and a privacy decision
  (see [Ethics](#ethics-legality-and-privacy));
- coordinates come from the item itself when present, or from a **geocoding function** the
  operator may enable.

### The information tree view

The tree makes the graph legible: **sources → groups → items → entities → links/insights**,
collapsible at every level, again in baseline HTML (nested lists) or inline SVG for large
graphs. It is the view for "how does this item relate to that one", for spotting clusters, and
for exporting a branch as a citation-ready outline.

### Other elements

The smaller tools an analyst expects, all baseline:

- a **search bar** (metadata server-side; content client-side when unlocked) with saved queries;
- **tags** and **watchlists** (a watchlist is a rule set plus an alert);
- an **insights inbox** with severity triage, dismiss-with-reason and promote-to-note;
- **OPML import/export** for bulk source management;
- **exports** — JSON/CSV, the combined feed, and a printable briefing of a collection;
- **accessibility and comfort** — keyboard navigation, dark mode, responsive layout, offline.

---

## Progressive web application

stenella is a PWA, and mostly for free: because it runs on atp, atp **generates** the web app
manifest, the service worker and the head links (`<link rel="manifest">`, the OpenSearch
`<link rel="search">`, resource hints) and sets the cache/compression policy. stenella supplies
the app metadata (name, icons, theme) and the views; the plumbing is atp's job. The effect:

- the platform is **installable** and runs in its own window (`display: standalone`);
- the **app shell is precached**, so the last-read items and the UI work **offline**;
- the combined feed is also an **OpenSearch** provider, so a browser can search a collection.

---

## Source reliability and disinformation

The sources are explicit that the open nature of OSINT makes it a target: information
explosion makes source evaluation hard, and state actors plant false information (even
manipulated geospatial data). stenella treats this as a first-class concern rather than hiding
it:

- **Provenance is always attached.** Every item carries its source(s); every insight cites the
  items, links and rule that produced it.
- **Source reliability is recorded, not assumed.** A source can be graded; grading is visible
  wherever its items appear, and rules can weight by it.
- **Cross-source corroboration is measurable.** Because duplicates collapse with all their
  attributions, and entities are shared, "one source vs many" is queryable.
- **Human judgement is kept.** Auto-links and auto-insights are labelled `auto` and can be
  disputed with a comment; nothing silently overwrites an analyst's assessment.

---

## Ethics, legality and privacy

OSINT is a lawful, public-source discipline, and stenella is built to keep it that way. The
ethical principles the sources name are design constraints here:

| Principle | How stenella respects it |
|---|---|
| **Legal boundaries** | passive collection only; no authentication bypass, no engagement, no dark-web clients |
| **Avoid harm** | no doxxing features; no aggregation of personal data beyond what a source published; no automated action against a subject |
| **Privacy** | identity-free; no reader tracking; content encrypted at rest; the base map and all analytics are offline by default |
| **Integrity & accuracy** | provenance and citations on every insight; auto vs human marked; dismissal requires a reason |
| **Purpose limitation** | work is scoped to a **collection** ("the question"); exports and shares are the analyst's explicit act |
| **Transparency & accountability** | every record is a readable file; rules are visible data; access is via revocable capability tokens, not silent accounts |

Because atp's access model adds **no security requirement** to reach hosted resources, the
platform is open by default and the operator chooses what to privilege; stenella's own
operator actions and shares are the capability layer on top.

---

## Extending stenella

The system is meant to grow without a rewrite. Every extension point is a Go function
registered on the embedded atp server, or a record type in pod:

| Want to… | Do this |
|---|---|
| add a source kind | register a fetch/parse function with `srv.Handle`; it returns items in the normal shape |
| change processing | add or replace processing functions (normalise, dedupe, classify, extract) |
| add an entity extractor | register an extractor function; it emits `entity` records |
| add a rule vocabulary | add condition/comparison kinds to the rule engine package |
| add an insight kind | emit an `insight` record with its citations from a rule |
| add a view | add a template + a JSON endpoint; it is a PWA route like the others |
| change storage | swap the pod table set; the model is records, not a fixed schema |
| embed in other software | mount stenella as an `http.Handler`, or use it as the reference for mounting atp |

The design rule is the same as atp's: **the platform is imported and expanded, not forked.**

---

## Layout

```text
stenella/                     # this module (superproject)
├── main.go                   # flags; builds the atp server; mounts stenella's functions
├── rss.go                    # collection (implemented): fetch/parse/sanitise + the shareable combined feed
├── rss_test.go               # its tests
├── atp/                      # submodule — the embedded platform (song/pod/shepherd)
├── collections/              # tasking: sources, groups, rules per collection
├── feed/                     # the staged home for the feed package (rss.go is the implemented collector)
├── process/                  # normalise, de-duplicate, classify, entity/place extraction
├── graph/                    # items, entities, links, comments, notes
├── search/                   # metadata search (server) + content search (browser)
├── rules/                    # rule engine → insights
├── views/                    # dashboard, feed/timeline, map, tree (+ their templates)
├── web/                      # HTTP layer: page routes, JSON APIs, shares
└── static/                   # baseline CSS + JS (charts, map, tree, app shell helpers)
```

`go.mod` is `require`-free apart from the embedded platform modules (`azzurrotech/atp`, and
transitively `pod`, `shepherd`, `song`), replaced by their submodule worktrees.

---

## Running locally

```sh
# build one binary
go build -o stenella-server .

# run it (the master secret must be at least 32 bytes)
./stenella-server \
  --addr :8084 \
  --root ./data \
  --secret 'a-master-secret-at-least-32-bytes-long!!'

# then read the shareable combined feed
curl -s http://localhost:8084/stenella/feeds/all.xml
```

Flags (env fallbacks in parentheses):

| Flag | Env | Default | Purpose |
|---|---|---|---|
| `--addr` | `$ATP_ADDR` | `:8080` | listen address (`host:port`) |
| `--root` | `$ATP_ROOT` | `./data` | shared data root (atp stores, pod records, hosted app, vault) |
| `--secret` | `$ATP_SECRET` | *(required)* | master secret, **at least 32 bytes**: vault key, pod encryption key and shepherd's signing key |

The combined feed's source set is environment configuration, not a flag:
`$STELLENA_RSS_SOURCES` (see [the feed section](#the-combined-feed-that-can-be-shared--implemented)),
and `$STELLENA_<NAME>_KEY` (`newsapi`, `twitter`, `shodan`) supplies optional source credentials.

---

## Deploying

One static binary plus a data directory. Cross-compiles to any Go target:

```sh
GOOS=linux GOARCH=amd64 go build -o stenella-server .
```

Terminate TLS at a front proxy (Caddy, nginx) and forward to `--addr`. Media and WebAssembly
are served by atp with correct types and range support; the security headers (CSP, nosniff,
HSTS, permissions, COOP/COEP) come from atp's header manager, so there is nothing to configure
per deployment. A container image is a two-line `Dockerfile` around the binary.

---

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

The behaviour this document specifies is delivered in stages. Delivered already: the embedded
**atp** submodule, the repository scaffolding, and **collection** — `rss.go` fetches, parses,
sanitises and merges the configured sources and serves the shareable combined feed described
in [Collection](#the-combined-feed-that-can-be-shared--implemented), with tests in
`rss_test.go`. The remaining Go packages (`collections`, `feed`, `process`, `graph`, `search`,
`rules`, `views`, `web`) and their atp-registered functions are the contract they implement.
Where this document names atp, song, pod or shepherd behaviour, those details are already
implemented and tested inside the submodule — see [`atp/README.md`](atp/README.md).

---

## References

- **Open-source intelligence (Wikipedia)** — <https://en.wikipedia.org/wiki/Open-source_intelligence>
- **What is Open-Source Intelligence? (SANS)** — <https://www.sans.org/blog/what-is-open-source-intelligence>
- **Analytics Dashboard project (W3Schools, `DATADASH001`)** — <https://www.w3schools.com/projects/project.php?project=DATADASH001>
- **atp — AzzurroTech Platform** — [`atp/README.md`](atp/README.md)
- **Go for Web Development** — <https://go.dev/solutions/webdev>
