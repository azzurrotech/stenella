# Browser Libraries (veni · vidi · vici · vini)

The four Emperor42 browser libraries that stenella loads at startup
(`--libs-dir`, default `static`) and serves at `/s/static/lib/*`. They are
plain, dependency-free JavaScript assets — no bundler, no npm install step, no
third-party runtime. Each is a git submodule with its own module (`go.mod`),
its own test suite and, in some cases, an **optional** Go demo server.

| Library | Asset | Role | Docs | Security |
|---|---|---|---|---|
| **veni** | `veni.js` | Web-component identification & registration (`Custom Elements`, `Shadow DOM`); exposes `window.Veni` and a singleton `window.veni`. A Go crawl demo ships alongside but shares no API. | [`veni/README.md`](veni/README.md) | [`veni/SECURITY.md`](veni/SECURITY.md) |
| **vidi** | `vidi.js` | Renders records from a JSON or XML endpoint as escaped cards with pagination and CSV export (`new Vidi({...})`). A Go template-editor demo ships alongside. | [`vidi/README.md`](vidi/README.md) | [`vidi/SECURITY.md`](vidi/SECURITY.md) |
| **vici** | `vici.js` | Non-`HttpOnly` cookie helpers and client-side Web Crypto AES-GCM encrypt/decrypt (`vici.encrypt`/`vici.decrypt`). The format is byte-compatible with stenella’s `crypt` package. A Go card/timeline editor demo ships alongside. | [`vici/README.md`](vici/README.md) | [`vici/SECURITY.md`](vici/SECURITY.md) |
| **vini** | `vini.js` | Defines and advances multi-step browser workflows: state, step payloads, progress and a bounded local execution log (`window.vini`, `window.Vini`). Renders no UI and provides no server/auth/transaction layer. | [`vini/README.md`](vini/README.md) | [`vini/SECURITY.md`](vini/SECURITY.md) |

## How stenella uses them

- `main.go` reads `veni.js`, `vidi.js`, `vici.js` and `vini.js` from
  `--libs-dir` at startup and serves them from memory at `/s/static/lib/*`.
- **vici** is the encryption boundary in the browser: the portal releases a
  client content key and vici opens `*_enc` envelopes and encrypts comment
  bodies before submission. vici does **not** set `HttpOnly` (JavaScript
  cannot); it manages only non-`HttpOnly` cookies and must never be treated as
  a security boundary by itself.
- **vidi** reads the public share endpoint `/s/api/x/{id}?t=…` (and
  `/s/data/{client}/{table}`) and escapes rendered values.
- **veni** registers custom elements already present in a page.
- **vini** drives multi-step flows such as the portal login and wizards,
  persisting progress in browser storage.

## Optional Go demos

`veni/`, `vidi/`, `vici/` and `vini/` each contain an optional Go demo server.
These are developer services, not part of the platform: they default to
**loopback** binds, and a non-loopback bind requires the demo’s documented API
token. Do not expose them.

## Testing

Each library module builds, vets and tests on its own:

```bash
cd static/veni && go build ./... && go vet ./... && go test ./...
# repeat for vidi, vici, vini
```

## Security

Each library has a `SECURITY.md` describing its threat model and honest limits.
The platform-wide policy is in
[`../../docs/SECURITY-POLICY.md`](../../docs/SECURITY-POLICY.md). Report
vulnerabilities privately to `security@azzurro.tech`.

*Last updated 2026-10-09.*
