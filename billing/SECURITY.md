# billing — Security

## Assets protected

`billing` deals with financial and configuration data rather than credentials:

- The **hosting cost model** (`HostingConfig`): the operator's infrastructure
  rate and fixed monthly overhead. Exposure reveals the platform's margins.
- The **income report** (`Report`, `Client` rows): per-client revenue, host cost
  and net margin, plus a 30-day projection.
- The in-memory `*atpclient.Client`, which carries **atp admin credentials** and
  is used to read summaries and billing data.

## Threat model and mitigations actually implemented

- **No negative costs.** `SetConfig` returns
  `errors.New("hosting costs cannot be negative")` when
  `HostPricePerGBHour < 0` or `FixedCostPerMonth < 0`, so a report cannot be
  skewed by an injected negative rate.
- **Fixed config location.** The config is always read and written at
  `filepath.Join(root, "stenella", "config.json")` (`configPath`). `root` comes
  from the operator (via `web.Config.Root`), not from request data, so there is
  no caller-controlled path component to traverse.
- **Directory created on demand.** `SetConfig` calls `os.MkdirAll(dir, 0o755)`
  before writing, so it cannot fail opaquely on a fresh root.
- **Reads fail soft.** `New` calls `os.ReadFile` and ignores both a missing file
  and a JSON unmarshal error, falling back to `DefaultHostingConfig()`. A
  corrupt or attacker-writable config cannot inject arbitrary field values
  beyond what `HostingConfig` declares, and cannot crash startup.
- **Concurrency-safe config.** `Calculator.mu` (`sync.RWMutex`) guards `cfg`:
  `Config` takes a read lock, `SetConfig` takes a write lock, so a request
  cannot read a half-updated cost model.
- **No computation from untrusted arithmetic beyond declared fields.** `Income`
  only reads atp-provided numbers and multiplies them. It does not execute,
  evaluate or parse expressions from the config or request data.

## Auth model

`billing` has **no authentication or authorization of its own**. It is a
library: whoever holds the `*Calculator` can read the income report and rewrite
the cost model.

In stenella the authorization boundary lives in `web`:

- `GET /s/api/admin/income` and `PUT /s/api/admin/income` are wrapped in
  `Server.adminGate`, which requires a valid atp admin session (bearer token or
  atp admin cookie).
- The `Calculator` is constructed by `web.New` and never exposed to a portal
  (non-admin) caller.

Any other embedder must supply an equivalent admin gate.

## Honest limits / non-claims

- **The config file is not encrypted and is written mode `0o644`.** It contains
  no credentials, but on a multi-user host the cost model is readable by other
  local users. The parent directory is `0o755`.
- **No audit trail.** `SetConfig` overwrites the file; there is no history of
  who changed the cost model or when.
- **`New` swallows config read errors.** A malformed config silently reverts to
  defaults; there is no warning surfaced by the package itself.
- **No input validation on report data.** `Income` trusts atp's summary and
  billing JSON; it clamps nothing and ignores per-client billing failures.
- **Money is `float64`.** Rounding error is possible in large reports; the
  package does not use integer minor units.
- **Privilege is inherited, not held.** The destructive capability is the
  embedded `atpclient.Client`; this package adds no restrictions on it.

## Dependency note

`billing` imports the standard library (`encoding/json`, `errors`, `os`,
`path/filepath`, `sort`, `sync`) plus the in-repo `azzurrotech/stenella/atpclient`.
There are no third-party dependencies.

## Reporting

Report suspected vulnerabilities privately to **security@azzurro.tech**. Do not
open a public issue.
