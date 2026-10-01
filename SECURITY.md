# stenella Security Overview

stenella embeds **atp** (the orchestrator) and uses it as middleware, so the
security model below is inherited from atp's architecture (which itself embeds
song, pod and shepherd). The claims here are limited to what the code actually
does — no RBAC, no MFA, no SSO, no external identity providers.

## Trust boundaries

1. **Public** — `/` (homepage), combined feeds (`/s/feed/{client}/…`), hosted
   sites (`/c/{client}/`), and token-protected shares (`/s/x/{id}?t=…`,
   `/s/api/x/{id}?t=…`). Anyone may read these. Shares require an exact token;
   the token never appears in index pages or portal listings. The public feed
   endpoints are hard-pinned to `acl_class = public` server-side and ignore any
   caller-supplied `?acl=`; the frontend filter is presentational only.
2. **Client portal** (`/s/api/portal/…`) — requires a portal session cookie
   (`stenella_session`) whose owner matches the `client` query parameter, **or** a
   valid atp admin session (impersonation). The two failures are distinct
   answers: no usable session is 401, a valid session belonging to a *different*
   client is 403, so a signed-in user is never told to re-authenticate when
   re-authenticating cannot help.
3. **Super admin** (`/s/api/admin/…`, `atp /api/*`) — requires the atp admin
   session (stdlib HMAC-signed cookie; no external dependency).

## The encryption boundary, stated plainly

- **Encrypted at rest, always.** Every item title/summary/body and every comment
  body is an AES-256-GCM ciphertext envelope in `*_enc` columns. Plaintext never
  touches disk — not in the feed cache, not in pod XML, not in logs. A client
  with no passphrase of its own still gets ciphertext, keyed from the platform
  master secret.
- **Wire format.** `v1.<salt>.<iv>.<ct>`, PBKDF2-SHA256 ×100k → AES-256-GCM —
  byte-for-byte what `vici.encrypt`/`vici.decrypt` produce and consume.
- **Encrypt-before-submit for authored content.** A comment body is encrypted in
  the browser and POSTed as ciphertext. The server stores the payload verbatim
  and cannot read or modify it.
- **No server-side plaintext search, by construction.** Free text is matched
  against envelope metadata only — source, categories, author, link host, guid.
  Full-text search over decrypted bodies executes in the browser.
- **Known weakening, deliberate.** Ingestion is server-side, so a server that
  fetches a feed and renders an RSS export *can* read what it fetched. The claim
  is "encrypted at rest", not "plaintext never exists outside the browser". The
  content key is released only to an authenticated session of that client, over
  the same channel as the rest of the portal; per-device envelope re-keying for
  multi-device access is **not** implemented (plan risk R2).
- **Access classification.** Each item is stamped `public`/`private`/`protected`
  at ingest. Pinned to the item, not resolved from the source at read time, so
  reclassifying a source cannot retroactively expose or hide already-cached
  items.

## Retention, and what deletion means

The hourly sweep removes aged-out items from the cache **and** from pod, with
exceptions for items that are pinned, commented, or reachable from a surviving
item through the link graph. Because bodies are ciphertext the server holds, a
sweep alone would leave plaintext that a browser had already decrypted, so each
removal also writes a tombstone; the page polls and drops its own copy. A sweep
is skipped rather than re-entered if one is already in flight, since two
concurrent sweeps would prune ids the other had just re-evaluated.

## What the code actually does

- **Sessions.** Admin sessions are atp's stdlib HMAC token cookies with an
  expiry; portal sessions are stenella's own in-memory store with a TTL. No
  third-party session library.
- **Passwords / secrets.** atp keeps a secrets vault encrypted with
  AES-256-GCM under a ≥ 32-byte master secret (`--secret` / `$STENELLA_SECRET`);
  vault files are never written in plaintext. Portal login compares the
  presented secret with a constant-time comparison.
- **Feed fetching.** Sources are fetched over HTTP(S) with bounded time and
  response-size limits (16 MiB per fetch; a scraped page is additionally cut to
  4 MiB before parsing, so inline trackers and base64 images are dropped rather
  than stored). Per-source `auth_secret` values are resolved from the vault and
  sent only as `Authorization: Bearer` on the matching source.
  HTML in `<description>`/`<content>` is retained on the server but the web UI
  renders summaries as text (templates use `html/template` escaping — stored
  values are never injected raw).
- **Share tokens.** Token-protected shares use the same master-secret-derived
  signing; a wrong or expired token yields 403, an unknown share id yields
  404 (no oracle). Share indexes and portal listings never leak tokens.
- **Path hygiene.** Client ids and junction names are sanitized before use in
  file paths (`links.safe`, atp's segment sanitization), blocking `..` /
  separator traversal. Symlink junctions only ever point *inside* the pod data
  root.
- **Usage + billing.** Every portal/admin request that flows through atp is
  logged per client (request/response bytes, silo size samples) and billed at
  the client's `price_per_gb_hour`; the income console is admin-only. Metering
  reads envelope metadata only (record counts, byte sizes), never plaintext.
- **Content key release.** `GET /s/api/portal/vault` returns the content key
  only for the client named in the same request, behind the portal gate. It is
  not reachable from a public route.
- **Signup.** `POST /s/api/signup` is the only unauthenticated mutating endpoint
  on the platform. It is rate limited and answers only once per client id; the
  portal secret it returns is shown exactly once and not recoverable afterwards.
- **Shared HTML.** Untrusted share content is escaped before it reaches a page
  (`TestSanitizeHTMLEscapesUntrustedShareContent` guards this), and stored values
  are never injected raw — templates use `html/template`.
- **Static JS.** The four Emperor42 libraries are ordinary browser JavaScript.
  vici uses the WebCrypto API (PBKDF2-SHA256, 256-bit AES-GCM) when an
  application explicitly supplies a passphrase. The azzurro checkout draft is
  intentionally browser-local and is not a place for secrets or payment data.
- **Public site data.** `/s/data/{client}/{table}` is read-only and limited to
  one enabled client namespace. Traversal, management tables, and share tokens
  fail closed; host-mapped sites cannot address another client's silo.

## Honest non-claims

What this project deliberately keeps simple:

- **No TLS internally.** stenella speaks plain HTTP; TLS termination is the
  reverse proxy's job (Caddy in front, per the port table in AGENTS.md §8).
- **No rate limiting UI.** atp logs usage but per-client rate-limit *tuning*
  screens are not built.
- **No RBAC / MFA / SSO / GDPR-anonymization.** These are not implemented.
- **No server-side plaintext search.** By design rather than by omission: free
  text is matched against envelope metadata only, and body search happens in the
  browser. A client searching a large archive gets the current page filtered, not
  a server-side full-text index (plan risk R1).
- **No per-device key separation.** One content key per client, released to that
  client's authenticated sessions. Anyone holding the key can decrypt that
  client's envelopes; the transport is the only thing protecting it in use.
- **No payment/order capture.** The azzurro checkout is a local VINI workflow;
  it does not charge a card, create an order, issue an invoice, or implement
  WooCommerce/WordPress accounts or APIs. Those require a separate server-side
  integration.
- **Portal sessions are in-memory** (single process) — restart drops portal
  sessions; they are re-established by login.
- **`httpOnly` cookies are set by the server; vici documents that JavaScript
  cannot set `httpOnly` itself** — treat any client-side cookie flag as a UI
  preference, not a security boundary.

## Vulnerability reporting

Report suspected vulnerabilities privately to `info@azzurro.tech` — do not
open a public issue. Include the affected version, a minimal repro, and the
impact. We'll acknowledge within 5 business days.