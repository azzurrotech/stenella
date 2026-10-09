# web — stenella data platform HTTP layer

## Overview

`web` implements the stenella data platform as an `http.Handler` that **embeds
atp and uses it as middleware**. It owns stenella's own namespace — the
search/link homepage at `/`, and the whole `/s/*` workspace (client portal,
super-admin console, public combined feeds, share URLs, static assets, public
site data) — while atp keeps its own namespace (`/api`, `/clients`, `/c`, `/gw`,
`/login`, `/logout`, `/health`).

Every song/pod/shepherd feature a page needs is reached through atp's own HTTP
handler via `stenella/atpclient`; `web` never imports `song`, `pod` or
`shepherd` directly, and never re-implements their behaviour.

- Module: `azzurrotech/stenella/web` (`go.mod`, Go 1.22).
- In-repo imports: `azzurrotech/atp/web` (atp), `stenella/atpclient`,
  `stenella/billing`, `stenella/crypt`, `stenella/feed`, `stenella/links`,
  `stenella/netguard`; all other imports are standard library.
- Imported by: `package main` (`main.go`).

### Source layout

| File | Responsibility |
| --- | --- |
| `web.go` | Package doc, `Config`, `Server`, `New`, `Handler`, host dispatch, router (`routes`), shared body/JSON helpers, feed mirroring, background/workflow lifecycle. |
| `auth.go` | Portal sessions, `clientGate`/`adminGate`, login/logout/whoami, admin check. |
| `api.go` | Portal API: feeds, links, shares, database, sites, secrets/payment, billing/usage, capability tokens. |
| `admin.go` | Super-admin API: summary, income, clients, secrets, feeds, config, usage, shepherd ops. |
| `feedapi.go` | Public combined feeds (HTML, JSON, RSS, Atom) and share page + JSON. |
| `collab.go` | Collaboration store over pod: comments, pins, schema. |
| `collabapi.go` | Collaboration HTTP handlers, retention status/sweep, content key, usage dashboard. |
| `retention.go` | Retention sweeper, survival set, tombstones. |
| `signup.go` | Self-service signup/provisioning. |
| `sitedata.go` | Public read-only pod-table bridge and path guard. |
| `pages.go` | Home/portal/admin pages, templates, static assets. |
| `ratelimit.go` | Shared fixed-window rate limiter and `remoteIP` keying. |
| `hostredirect.go` | Rewrites Song's internal `/{client}/...` redirects on mapped hosts. |

## Public API

### Config / New / Server

```go
type Config struct {
	Root          string
	AtpSecret     string
	AdminUser     string
	AdminPassword string
	PublicBase    string
	Libs          map[string][]byte
	SiteHosts     map[string]string
	PlatformPath  string
	TrustProxy    bool
}

func New(cfg Config) (*Server, error)

type Server struct { /* unexported fields */ }

func (s *Server) Handler() http.Handler
func (s *Server) Background(ctx context.Context)
func (s *Server) Wait()
func (s *Server) Close()
func (s *Server) SweepClient(client string) *SweepReport
```

- `New` normalizes `SiteHosts`/`PlatformPath`, builds the embedded
  `atpweb.ATPService`, logs the `atpclient.Client` in as admin, and wires the
  feed engine, links/shares stores, collaboration store, sweeper, rate limiters,
  session store and income calculator. It returns an error on invalid routing
  config or a failed atp login.
- `Handler` composes `hostDispatch(atpSvc.Middleware(siteDataPathGuard(mux)))`.
  Virtual-host dispatch is outermost so a mapped public host can never reach a
  different client's `/c/` silo.
- `Background` starts two independent long-running loops — the feed refresher
  and the hourly retention sweep. It returns immediately.
- `Wait` blocks until request-spawned fetch/mirror goroutines finish; it does
  **not** wait for the long-running loops.
- `Close` cancels the loops, waits for them, then drains request-spawned work.
  Safe to call more than once.
- `SweepClient` runs the retention sweep for one client, returning `nil` when
  nothing expired or another sweep is in flight.

### Retention types and constants

```go
const (
	DefaultRetentionDays = 1
	MinRetentionDays     = 1
	MaxRetentionDays     = 365
	TombstoneTTL         = 48 * time.Hour
)

func ClampRetention(days int) int

type Tombstone struct {
	ItemID   string `json:"item_id"`
	Client   string `json:"client"`
	Removed  string `json:"removed"`
	Reason   string `json:"reason"`
	SourceID string `json:"source_id,omitempty"`
}

type SweepReport struct {
	Client     string      `json:"client"`
	Examined   int         `json:"examined"`
	Removed    int         `json:"removed"`
	KeptPinned int         `json:"kept_pinned"`
	KeptCmt    int         `json:"kept_commented"`
	KeptLink   int         `json:"kept_linked"`
	Errors     int         `json:"errors"`
	Tombstones []Tombstone `json:"tombstones,omitempty"`
	RanAt      string      `json:"ran_at"`
}
```

- `ClampRetention` maps `<= 0` to `DefaultRetentionDays` and clamps into
  `[MinRetentionDays, MaxRetentionDays]`.
- `Tombstone` tells a browser that a cached item is gone; the page polls and
  drops its local copy.

### Collaboration and usage types

```go
type ItemFinder func(client, id string) bool

type Comment struct {
	ID          string `json:"id"`
	ItemRef     string `json:"item_ref"`
	AuthorToken string `json:"author_token,omitempty"`
	BodyEnc     string `json:"body_enc"`
	Bytes       int    `json:"bytes,omitempty"`
	Created     string `json:"created"`
	ACLClass    string `json:"acl_class,omitempty"`
}

type Pin struct {
	ID        string `json:"id"`
	ItemRef   string `json:"item_ref"`
	CreatedBy string `json:"created_by,omitempty"`
	Created   string `json:"created"`
}

type UsageRow struct {
	Client       string         `json:"client"`
	Name         string         `json:"name,omitempty"`
	Sources      int            `json:"sources"`
	Items        int            `json:"items"`
	ItemsByClass map[string]int `json:"items_by_class,omitempty"`
	Pins         int            `json:"pins"`
	Comments     int            `json:"comments"`
	Links        int            `json:"links"`
	SiteFiles    int            `json:"site_files"`
	DiskBytes    int64          `json:"disk_bytes"`
	DiskGB       float64        `json:"disk_gb"`
}
```

## Routes and auth requirements

atp owns `/api`, `/clients`, `/c`, `/gw`, `/login`, `/logout`, `/health` and
serves them through its middleware before/around stenella's mux. The routes
below are the ones registered by `Server.routes`.

| Method | Path | Auth |
| --- | --- | --- |
| GET | `/` | Public (homepage) |
| GET | `/s/portal` | Public page shell; its API is gated |
| GET | `/s/admin` | Public page shell; its API is gated |
| GET | `/s/feed/{client}` | Public |
| GET | `/s/feed/{client}/combined.xml` | Public |
| GET | `/s/feed/{client}/combined.atom` | Public |
| GET | `/s/feed/{client}/items` | Public |
| GET | `/s/x/{id}` | Public **with** share token `?t=` |
| GET | `/s/static/{file...}` | Public |
| GET | `/s/data/{client}/{table...}` | Public; private tables → 403 |
| GET | `/s/signup` | Public |
| POST | `/s/api/signup` | Public, rate-limited |
| POST | `/s/api/client/login` | Public, rate-limited |
| POST | `/s/api/client/logout` | Public (clears cookie) |
| GET | `/s/api/x/{id}` | Public **with** share token `?t=` |

Portal API — `clientGate`: client's own session **or** an authenticated atp
admin impersonating the client.

| Method | Path |
| --- | --- |
| GET | `/s/api/portal/whoami` |
| GET, POST | `/s/api/portal/feeds` |
| PUT, DELETE | `/s/api/portal/feeds/{id}` |
| POST | `/s/api/portal/feeds/{id}/fetch` |
| POST | `/s/api/portal/import` |
| GET | `/s/api/portal/items` |
| POST | `/s/api/portal/refresh` |
| GET, POST | `/s/api/portal/links` |
| DELETE | `/s/api/portal/links/{id}` |
| GET, POST | `/s/api/portal/items/comments` |
| DELETE | `/s/api/portal/items/comments/{id}` |
| POST, DELETE | `/s/api/portal/items/pin` |
| GET | `/s/api/portal/items/pins` |
| GET | `/s/api/portal/retention` |
| POST | `/s/api/portal/retention/sweep` |
| GET | `/s/api/portal/vault` |
| GET, POST | `/s/api/portal/shares` |
| DELETE | `/s/api/portal/shares/{id}` |
| GET | `/s/api/portal/db/tables` |
| GET, POST | `/s/api/portal/db/table` |
| DELETE | `/s/api/portal/db/record` |
| POST | `/s/api/portal/db/table/create` |
| POST | `/s/api/portal/db/table/bulk` |
| GET | `/s/api/portal/sites/meta` |
| GET | `/s/api/portal/sites/files` |
| POST, PUT, DELETE | `/s/api/portal/sites/file` |
| GET, POST, DELETE | `/s/api/portal/secrets` |
| GET, PUT | `/s/api/portal/payment` |
| GET | `/s/api/portal/billing` |
| GET | `/s/api/portal/usage` |
| POST | `/s/api/portal/keys` |
| POST | `/s/api/portal/keys/magic` |
| GET | `/s/api/portal/keys/verify` |
| POST | `/s/api/portal/revoke` |

Super-admin API — `adminGate`: valid atp admin session required.

| Method | Path |
| --- | --- |
| GET | `/s/api/admin/summary` |
| GET, PUT | `/s/api/admin/income` |
| GET | `/s/api/admin/clients` |
| POST | `/s/api/admin/client` |
| PUT, DELETE | `/s/api/admin/client/{id}` |
| GET, POST, DELETE | `/s/api/admin/secrets` |
| GET | `/s/api/admin/feeds` |
| GET, PUT | `/s/api/admin/config` |
| GET | `/s/api/admin/usage` |
| GET | `/s/api/admin/retention` |
| POST | `/s/api/admin/retention/sweep` |
| GET, POST | `/s/api/admin/shepherd/firewall/rules` |
| DELETE | `/s/api/admin/shepherd/firewall/rules/{id}` |
| GET | `/s/api/admin/shepherd/ratelimit/status` |
| POST | `/s/api/admin/shepherd/ratelimit/reset` |
| GET, POST | `/s/api/admin/shepherd/upstreams` |
| DELETE | `/s/api/admin/shepherd/upstreams/{prefix}` |
| POST | `/s/api/admin/shepherd/keys` |
| POST | `/s/api/admin/shepherd/keys/block` |
| POST | `/s/api/admin/shepherd/keys/magic` |
| POST | `/s/api/admin/shepherd/revoke` |
| POST | `/s/api/admin/shepherd/revoke/block` |
| GET | `/s/api/admin/shepherd/jskey` |

## Usage

```go
svc, err := web.New(web.Config{
	Root:          "./data",
	AtpSecret:     secret, // >= 32 bytes
	AdminUser:     "admin",
	AdminPassword: adminPass,
	PublicBase:    "https://azzurro.tech",
	Libs:          libs, // veni.js, vidi.js, vici.js, vini.js
	SiteHosts:     map[string]string{"azzurro.tech": "azzurrotech"},
	PlatformPath:  "/platform",
})
if err != nil {
	log.Fatal(err)
}
go svc.Background(context.Background())
defer svc.Close()
http.ListenAndServe(":8084", svc.Handler())
```

## Configuration

`web.Config` fields and defaults (set by `New`):

| Field | Meaning | Default / limit |
| --- | --- | --- |
| `Root` | Shared data root (clients, secrets, song+pod stores, feeds/links/shares/config) | `"./data"` |
| `AtpSecret` | atp master secret | Required by atp; expected `>= 32` bytes |
| `AdminUser` | atp administrator username | `"admin"` |
| `AdminPassword` | atp administrator password | `$STENELLA_ADMIN_PASSWORD`, else `"admin"` |
| `PublicBase` | Prefix for share URLs | `""` (relative URLs) |
| `Libs` | JS libraries keyed by filename, served at `/s/static/lib/<name>` | `nil` |
| `SiteHosts` | Public host → client id for hosted sites at `/` | validated; invalid entries rejected |
| `PlatformPath` | Path on mapped hosts that redirects to the portal | `"/platform"` |
| `TrustProxy` | Read `X-Forwarded-For` for rate-limit keys | `false` |

Limits and constants enforced by the package:

- Request body cap: `maxBodyBytes = 4 << 20` (4 MiB) for every JSON endpoint
  (`readBody`, `decodeBody`); `decodeStream` caps the urlencoded form path too
  and the JSON signup path at 1 MiB.
- Portal session TTL: `sessionTTL = 24h`; cookie `stenella_session`.
- Login rate limit: 5 attempts per minute per key (`loginWindow`/`loginBurst`).
- Signup rate limit: 5 attempts per minute per key (`signupWindow`/`signupBurst`).
- Portal secret length: `signupSecretBytes = 24` bytes (48 hex chars).
- Comment ciphertext caps: plaintext `maxCommentBody = 256 KiB`; `body_enc`
  `≤ 2×` that.
- Retention: sweep interval 1h; tombstones kept `48h`; window clamped to
  `[1, 365]` days (default 1).
- Public combined feed cap: `maxCombinedItems = 10000`.
- Pagination caps: feed/DB/portal page size 200; public site-data `limit`
  default 200, max 1000; share table dump 500.
- atp admin token cache inside `atpclient`: 24h.

## Testing

From `/home/matthew/Projects/Platform/stenella`:

```sh
go test ./web
go test ./...
```

`TestMain` sets `netguard.AllowPrivate = true` **only for the test binary**, so
the suite can fetch its own loopback fixtures; the shipped binary leaves the
SSRF guard on.

The suite covers, among other things:

- Auth gates: `TestAdminGateRejectsAnonymous`, `TestPortalGateRejectsAnonymous`,
  `TestPortalGateDistinguishesWrongClientFromAnonymous`,
  `TestCreateTableRequiresAuth`.
- Login abuse defenses: `TestClientLoginRateLimited`,
  `TestClientLoginFailureIsUniform`, `TestRequestBodySizeCap`.
- ACL enforcement on public feeds: `TestPublicFeedExcludesPrivateAndProtected`,
  `TestPublicEndpointIgnoresACLParam`, `TestPostsACLClassIsPublicGated`.
- Virtual hosting and path safety: `TestHostDispatchServesClientSite`,
  `TestHostDispatchDoesNotExposeAnotherClientSilo`, `TestHostOnly…`,
  `TestNormalizePlatformPath`, `TestSafeMappedSitePath`.
- Public site data: `TestSiteDataPrivateTables`, `TestSiteDataPublic`,
  `TestSiteDataGuardRejectsEncodedTraversalBeforeMux`.
- Collaboration/retention: `TestCommentRoundTripIsCiphertext`,
  `TestPinIsIdempotent`, `TestItemLinkSurvivalSet`,
  `TestRetentionSweepAndTombstones`, `TestTombstoneWindowExpires`,
  `TestContentKeyIsClientScoped`.
- Signup: `TestSignupProvisionsClient`, `TestSignupRejections`.
- Static assets and the standalone dependency gate
  (`TestCoreModulesImportOnlyStdlib`, …).

## Design notes / invariants

- **atp is the only path to song/pod/shepherd.** Every feature is a call on
  `atpclient`, so usage metering and scoping always apply.
- **Two session kinds.** A stenella portal session (`stenella_session`) is a
  random in-memory capability handle; atp admin auth is the atp bearer token or
  atp's own `atp_session` cookie, checked by `isAdmin` via
  `atpclient.AdminValid`.
- **401 vs 403 means different things.** `requireClient` returns
  `errUnauthorized` (401) when there is no usable session and `errForbidden`
  (403) when a valid session belongs to a different client.
- **Ciphertext before submit, sealed before disk.** Comment bodies arrive
  already encrypted; feed item bodies are sealed by `sealItem` and only
  `*_enc` columns plus metadata reach pod (`itemRecord`).
- **One item graph.** Item→item edges live in the links store; the retention
  survival set walks those edges so pinned/commented items pull reachable items
  out of the sweep.
- **Public site data is hard-pinned.** The public feed endpoints force
  `acl_class=public` and ignore a caller-supplied `?acl=`; `/s/data` refuses
  `shares`, `links`, `comments`, `pins` and `items`.
- **Virtual hosts are isolated.** `hostDispatch` runs outside atp middleware;
  on a mapped host only the mapped client's own `/c/` silo is reachable and
  everything else routes to `/c/{mappedClient}/...`.
