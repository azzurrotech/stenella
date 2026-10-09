# atpclient — in-process client for the embedded atp orchestrator

## Overview

`atpclient` is stenella's typed handle on the embedded **atp** HTTP surface.
Rather than calling `song`, `pod` or `shepherd` directly, stenella sends every
request through atp's own `http.Handler`, so each one passes through atp's auth,
client scoping, usage logging and billing layers exactly like a real external
client would.

The handler is invoked **in-process** with `net/http/httptest.NewRequest` and
`httptest.NewRecorder`. There is no network hop, no socket and no added latency;
the package therefore stays standard-library only.

- Module: `azzurrotech/stenella/atpclient` (`go.mod`, Go 1.22).
- Imports: standard library only.
- Imported by: `stenella/billing`, `stenella/web` (and `stenella/links`).

## Public API

### Construction and transport

```go
type Client struct { /* unexported fields */ }

func New(handler http.Handler, adminUser, adminPassword string) *Client
func (c *Client) Handler() http.Handler
```

- `New` wraps an atp handler (`web.ATPService.Handler()` or an atp middleware
  chain) and stores the admin credentials for later login.
- `Handler` exposes the wrapped handler back to the server wiring.

### Admin session

```go
func (c *Client) Login() error
func (c *Client) Token() (string, error)
func (c *Client) AdminValid(token string, cookies []*http.Cookie) bool
```

- `Login` POSTs the configured credentials to atp's public `/login`, requires a
  non-empty `token` in the reply, and caches it with a 24-hour expiry.
- `Token` returns the cached admin token, re-logging in when empty or expired.
- `AdminValid` reports whether a bearer token **or** cookies authenticate to
  atp's admin API by dispatching `GET /api/summary` to atp itself.

### JSON plumbing

```go
func (c *Client) JSON(method, path string, query url.Values, body, out any) error
func (c *Client) JSONWithToken(method, path string, query url.Values, body, out any, token string) error
func (c *Client) JSONPublic(method, path string, query url.Values, body, out any) error

type HTTPError struct {
	Status int
	Body   string
}
func (e *HTTPError) Error() string
func (e *HTTPError) Is(target error) bool
```

- `JSON` performs an authenticated admin request, obtaining the token via
  `Token`. Query params are appended for `GET`/`DELETE`; `body` is marshalled as
  JSON. `out` is filled when non-nil.
- `JSONWithToken` is the same but uses an explicit token.
- `JSONPublic` sends no credentials (atp public endpoints such as `/login`).
- A response with status `>= 400` becomes an `*HTTPError` whose `Body` is the
  trimmed response body (empty bodies fall back to the status text, and long
  bodies are truncated to 400 bytes).
- `HTTPError.Is` matches another `*HTTPError` with the same `Status`, so
  `errors.Is(err, &atpclient.HTTPError{Status: 404})` works as a sentinel.

### Clients

```go
type ClientRecord struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Created        string  `json:"created"`
	Disabled       bool    `json:"disabled,omitempty"`
	Chargeable     bool    `json:"chargeable"`
	PricePerGBHour float64 `json:"price_per_gb_hour"`
	RetentionHours int     `json:"retention_hours"`
	Notes          string  `json:"notes,omitempty"`
}

func (c *Client) Summary() (map[string]any, error)
func (c *Client) ListClients() ([]ClientRecord, error)
func (c *Client) GetClient(id string) (map[string]any, error)
func (c *Client) CreateClient(id, name, notes string) error
func (c *Client) UpdateClient(id string, patch map[string]any) error
func (c *Client) DeleteClient(id string) error
```

- `Summary` is atp's platform overview; its `clients` key is an `[]any` of
  client objects.
- `CreateClient` also provisions the client's song silo. `DeleteClient` removes
  both the registry entry and the silo. `UpdateClient` is a patch; an empty
  patch is a no-op.

### Vault secrets

```go
type Secret struct {
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
	Set  string `json:"set,omitempty"`
}

func (c *Client) ListSecrets(client string) ([]Secret, error)
func (c *Client) SetSecret(client, name, value, note string) error
func (c *Client) GetSecret(client, name string) (string, error)
func (c *Client) DeleteSecret(client, name string) error
```

- `ListSecrets` never returns values. `GetSecret` returns the decrypted value
  and is admin-only at the atp layer.

### Pod tables and records

```go
type TableQuery struct {
	Limit   int
	Offset  int
	OrderBy string
	Desc    bool
	Q       string
}

func (c *Client) QueryTable(table string, tq TableQuery) ([]map[string]string, int, error)
func (c *Client) GetRecord(table, id string) (map[string]string, error)
func (c *Client) UpsertRecord(table string, values map[string]string) (map[string]string, error)
func (c *Client) DeleteRecord(table, id string) error
func (c *Client) ListTables(client string) ([]map[string]any, error)
func (c *Client) CreateTable(table string, cols []string) error
```

- `QueryTable` reads through atp's `/api/pod` pass-through with `format=json`.
  It returns the flattened rows (envelope `id`/`created`/`updated` beside the
  table's own columns) and the count atp reported.
- Table names may contain `/` (e.g. `client/table`); each segment is escaped
  individually by the internal `escapePath` helper.
- `CreateTable` creates every column with type `text`.

### Billing, usage and settings

```go
func (c *Client) Billing(client string) (map[string]any, error)
func (c *Client) Hourly(client string) ([]map[string]any, error)
func (c *Client) Usage(client string, limit int) ([]map[string]any, error)
func (c *Client) Settings() (map[string]any, error)
func (c *Client) PutSettings(patch map[string]any) error
```

- `Billing` returns atp's per-client billing summary; `Hourly` returns hourly
  usage rollups; `Usage` returns recent request-log records (limit honored when
  positive). `Settings`/`PutSettings` read and patch platform config.

### Capability tokens (scoped and global)

```go
func (c *Client) IssueKey(client string, args map[string]any) (map[string]any, error)
func (c *Client) IssueMagicLink(client string, args map[string]any) (map[string]any, error)
func (c *Client) IssueBlock(client string, args map[string]any) (map[string]any, error)
func (c *Client) Revoke(client string, args map[string]any) error
func (c *Client) VerifyToken(client, token string) (map[string]any, error)

func (c *Client) IssueKeyGlobal(args map[string]any) (map[string]any, error)
func (c *Client) IssueBlockGlobal(args map[string]any) (map[string]any, error)
func (c *Client) IssueMagicLinkGlobal(args map[string]any) (map[string]any, error)
func (c *Client) RevokeTokenGlobal(token string) error
func (c *Client) RevokeBlockGlobal(block string) error
func (c *Client) JSKeyGlobal(scope, usage string) (map[string]any, error)
```

- The client-scoped calls force the client scope at atp; the `Global` variants
  are unscoped. `JSKeyGlobal` forwards `scope` and `usage` as query parameters
  when non-empty.

### Song silos (hosted sites)

```go
type SongFileOp struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Encrypt   *bool  `json:"encrypt,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

func (c *Client) ListSiloFiles(client, dir string) ([]map[string]any, error)
func (c *Client) CreateSongFile(client string, op SongFileOp) error
func (c *Client) UpdateSongFile(client string, op SongFileOp) error
func (c *Client) DeleteSongFile(client, path string) error
func (c *Client) JSKey(client string) (map[string]any, error)
```

### Shepherd firewall, rate limits and upstreams

```go
func (c *Client) FirewallRules() (map[string]any, error)
func (c *Client) AddFirewallRule(rule any) (map[string]any, error)
func (c *Client) DeleteFirewallRule(id string) error
func (c *Client) RateLimitStatus(key string) (map[string]any, error)
func (c *Client) RateLimitReset() error
func (c *Client) ListUpstreams() (map[string]any, error)
func (c *Client) AddUpstream(upstream any) (map[string]any, error)
func (c *Client) DeleteUpstream(prefix string) error
```

## Usage

```go
handler := atpSvc.Handler()
atp := atpclient.New(handler, "admin", password)
if err := atp.Login(); err != nil {
	return err
}
clients, err := atp.ListClients()
rec, err := atp.QueryTable("acme/products", atpclient.TableQuery{
	Limit: 20, OrderBy: "name",
})
```

## Configuration, credentials and limits

- Credentials: `New` takes `adminUser`/`adminPassword`; they are only used by
  `Login`, which caches the returned bearer token.
- Admin token cache: 24 hours (`time.Now().Add(24 * time.Hour)`).
- Response body cap: `maxBody = 64 << 20` (64 MiB) — the client reads at most
  this much of any atp response body.
- Error body cap: 400 bytes in `HTTPError.Body`; decode errors mention at most
  200 bytes of the offending body.
- No retry or backoff: `Token` re-logs in on expiry, and that is the only
  automatic retry.

## Testing

There are no test files in this package (`go test ./atpclient/...` reports
`[no test files]`). Its behaviour is exercised indirectly by the `web` tests,
which drive `web.ATPService` through this client.

From `/home/matthew/Projects/Platform/stenella`:

```sh
go test ./atpclient/...
```

## Design notes / invariants

- **Nothing is re-implemented.** Every method is a thin wrapper over an atp
  route; auth, scoping, metering and billing stay in atp.
- **One transport path.** All requests go through `do`, which builds the
  `httptest` request, sets `Accept: application/json`, sends `X-ATP-Token` when
  supplied, and copies cookies verbatim.
- **`escapePath` is per segment.** `client/table` is split on `/` and each
  segment escaped, so a real slash survives but embedded specials are encoded.
- **Errors are typed by status.** Use `errors.As`/`errors.Is` on `*HTTPError`.
- **The token is guarded by a mutex.** `Client.mu` protects `token`/`expiry`
  so concurrent callers share one login.
