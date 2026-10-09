# web — Security

## Assets protected

- **Portal sessions** (`stenella_session`) and the capability they represent:
  full access to one client's portal API.
- **The client content passphrase** released by `handleContentKey`
  (`GET /s/api/portal/vault`), the key that opens all of that client's sealed
  feed bodies.
- **Client vault secrets**, including the `portal`/`password` login secret and
  the `payment` record, managed through the portal and admin vault endpoints.
- **Super-admin authority**: the atp admin session/token behind
  `GET/PUT /s/api/admin/*`.
- **Feed and site content**: private/protected item bodies, client-authored pod
  tables, share tokens.
- **Rate-limit budgets** for the unauthenticated login and signup endpoints.

## Threat model and mitigations actually implemented

- **Session tokens are unguessable.** `randomHex(24)` draws 24 bytes from
  `crypto/rand` and hex-encodes them; it panics rather than falling back to a
  weak source. The token is stored server-side in `sessionStore`; the cookie
  only carries the handle.
- **Cookie hardening.** `handleClientLogin` sets `HttpOnly: true`,
  `SameSite: http.SameSiteLaxMode`, `Path: "/"`, `MaxAge` `sessionTTL` (24h),
  and `Secure: requestIsHTTPS(r)` (true on TLS or `X-Forwarded-Proto: https`).
  Logout (`handleClientLogout`) deletes the server-side entry and expires the
  cookie with `MaxAge: -1`.
- **Sessions expire server-side.** `sessionStore.get` deletes an entry once
  `time.Now().After(s.expires)`, so an expired cookie is rejected even if the
  browser keeps sending it.
- **Constant-time secret comparison.** `secretEqual` uses
  `crypto/subtle.ConstantTimeCompare` and still does equal work on length
  mismatch, so a wrong secret does not leak timing.
- **No client enumeration on login.** `handleClientLogin` performs exactly one
  constant-time comparison on every path and returns the same `401
  "invalid client or secret"` for an unknown/disabled client and a wrong
  secret; the specific reason is written only to the server log. Verified by
  `TestClientLoginFailureIsUniform`.
- **Rate limiting on unauthenticated endpoints.**
  `handleClientLogin` uses `login` (5/min, `loginBurst`/`loginWindow`) and
  `handleSignup` uses `signup` (5/min, `signupBurst`/`signupWindow`), both
  `rateLimiter` fixed-window counters. Over budget returns `429` with
  `Retry-After: 60`. The limiter is per-`Server`, not process-global.
- **Rate-limit keying is proxy-aware but off by default.** `remoteIP` uses
  `r.RemoteAddr` unless `Config.TrustProxy` is set, in which case it reads the
  first `X-Forwarded-For` value. `main.go` exposes this as `--trust-proxy` /
  `$STENELLA_TRUST_PROXY=1` and documents it as for a proxy you control only.
- **Body-size caps.** `maxBodyBytes = 4 << 20` is applied by `readBody` via
  `http.MaxBytesReader` to every JSON endpoint; `decodeStream` caps the signup
  urlencoded form at the same bound and its JSON path at 1 MiB. Verified by
  `TestRequestBodySizeCap`.
- **401 vs 403 semantics.** `clientGate` calls `requireClient`: a request with
  no usable session gets `401`; a session belonging to another client gets
  `403` (via `errForbidden`). `adminGate` returns `401 "admin session
  required"` when `isAdmin` is false. `handleWhoami` likewise distinguishes the
  two roles.
- **SSRF guard on server-side fetches.** `httpClient` dials through
  `netguard.NewTransport()`, which blocks loopback/private/link-local targets
  per dial (so every redirect hop is checked). `httpFetch` caps the response
  read at 8 MiB and treats status `>= 400` as an error. The guard is lifted only
  by `--allow-private-fetch` / `$STENELLA_ALLOW_PRIVATE_FETCH=1`; `TestMain`
  enables it solely for the test binary.
- **Encrypt before submit.** Comment bodies arrive as ciphertext (`body_enc`);
  `collabStore.AddComment` refuses an empty body, and `handleCreateComment`
  caps plaintext at `maxCommentBody` (256 KiB) and ciphertext at twice that.
  The server never decrypts a comment it did not have to.
- **Sealed before disk.** `sealItem` encrypts title/summary/content and
  `itemRecord` sends only `*_enc` fields plus metadata to pod, so pod never
  stores a plaintext body.
- **Sealed bodies are opened only for token holders.** The share page
  (`handleSharePage`) validates the share token (`sh.Valid`) before
  `openItemField` decrypts; a body that will not open yields `""`, never the
  ciphertext.
- **Public surfaces are hard-pinned.** Public feed handlers call
  `publicFeedQuery`, which forces `AclClass: feed.ACLPublic` and ignores a
  caller-supplied `?acl=`. `/s/data` refuses the private tables
  `shares`, `links`, `comments`, `pins`, `items` via `privateSiteTable`.
- **Path and host validation.** `siteDataPathGuard`/`invalidSiteDataPath`
  reject unsafe `/s/data` paths before `http.ServeMux` can canonicalize them
  (fail-closed); `validPublicTablePath`, `validPortalTableName`,
  `validPortalColumnName` and `validPortalRecordID` constrain names reaching
  pod. `normalizeSiteHosts`, `hostOnly`, `validHostName` and
  `normalizePlatformPath` reject malformed hosts/paths; `hostDispatch` runs
  outside atp middleware so a mapped host cannot reach another client's
  `/c/` silo.
- **`TrustProxy` is opt-in**, and `hostOnly` refuses malformed `Host` headers
  so a bad authority cannot fall through to a configured mapping.

## Auth model

Two independent gates:

- **Portal (`clientGate`)** — satisfied by a `stenella_session` cookie whose
  session belongs to the `client` query parameter (`sessionClient`), **or** by
  a valid atp admin (`isAdmin`) impersonating that client. The client id is
  validated with `validSiteClientID` before use.
- **Super-admin (`adminGate`)** — satisfied only by `isAdmin`, i.e. a valid
  `X-ATP-Token` bearer or atp admin cookies, each confirmed by dispatching a
  request to atp's own admin guard through `atpclient.AdminValid`.

`isAdmin` is the only place a request's admin claim is trusted, and it always
asks atp. Portal sessions grant full power within their own client namespace
and nothing outside it; the atp admin session grants the whole platform.

## Honest limits / non-claims

- **No CSRF token layer.** State-changing endpoints rely on the session cookie's
  `SameSite=Lax` and on JSON content types, not on an anti-CSRF token. There is
  no origin/`Referer` check.
- **Sessions are in-memory.** A restart invalidates every portal session, and
  sessions are not shared across processes or persisted. There is no
  server-side session listing or bulk revocation beyond deleting the cookie's
  entry.
- **No RBAC.** A portal session is all-or-nothing for its client; an atp admin
  session is all-powerful. There are no per-endpoint roles or scopes.
- **The content passphrase is released to the browser.** `handleContentKey`
  returns the plaintext passphrase to the client's own authenticated session.
  This is a documented boundary: server-side login equals server-side
  plaintext access for that client.
- **Portal-login rate limiting is per-IP and in-memory.** Without
  `--trust-proxy` (the default) an attacker behind shared NATs shares a budget;
  with it enabled, a caller who can spoof `X-Forwarded-For` while bypassing the
  proxy gets a fresh budget. Neither mode is a substitute for a WAF.
- **No admin rate limiting.** The login/signup limiters do not cover admin or
  portal API endpoints.
- **`X-Forwarded-Proto` is trusted unconditionally** by `requestIsHTTPS` for
  choosing the `Secure` cookie flag; a direct attacker who can set that header
  could prevent `Secure` (not remove it once set).
- **The share token travels in the query string** (`?t=`), so it can appear in
  logs and browser history.
- **Comment bodies are not server-searchable.** They are sealed, so only
  metadata search works server-side; full-text search happens in the browser.

## Dependency note

`web` imports the standard library plus only in-repo packages:
`azzurrotech/atp/web`, `stenella/atpclient`, `stenella/billing`,
`stenella/crypt`, `stenella/feed`, `stenella/links` and `stenella/netguard`.
No third-party modules are used. The `standalone_test.go` suite additionally
enforces that the vendored core modules `pod`, `shepherd` and `song` remain
standard-library-only (`TestCoreModulesImportOnlyStdlib`, …).

## Reporting

Report suspected vulnerabilities privately to **security@azzurro.tech**. Do not
open a public issue.
