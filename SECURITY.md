# stenella Security Overview

stenella embeds **atp** (the orchestrator) and uses it as middleware, so the
security model below is inherited from atp's architecture (which itself embeds
song, pod and shepherd). The claims here are limited to what the code actually
does — no RBAC, no MFA, no SSO, no external identity providers.

## Trust boundaries

1. **Public** — `/` (homepage), combined feeds (`/s/feed/{client}/…`), hosted
   sites (`/c/{client}/`), and token-protected shares (`/s/x/{id}?t=…`,
   `/s/api/x/{id}?t=…`). Anyone may read these. Shares require an exact token;
   the token never appears in index pages or portal listings.
2. **Client portal** (`/s/api/portal/…`) — requires a signed portal session
   cookie (`stenella_session`) whose owner matches the `client` query
   parameter, **or** a valid atp admin session (impersonation). Cross-client
   access is refused even for admins when the path would cross a client
   boundary (enforced by atp's `/c/{client}/…` scoping for song/pod).
3. **Super admin** (`/s/api/admin/…`, `atp /api/*`) — requires the atp admin
   session (stdlib HMAC-signed cookie; no external dependency).

## What the code actually does

- **Sessions.** Admin sessions are atp's stdlib HMAC token cookies with an
  expiry; portal sessions are stenella's own in-memory store with a TTL. No
  third-party session library.
- **Passwords / secrets.** atp keeps a secrets vault encrypted with
  AES-256-GCM under a ≥ 32-byte master secret (`--secret` / `$STENELLA_SECRET`);
  vault files are never written in plaintext. Portal login compares the
  presented secret with a constant-time comparison.
- **Feed fetching.** Sources are fetched over HTTP(S) with bounded time and
  response-size limits. Per-source `auth_secret` values are resolved from the
  vault and sent only as `Authorization: Bearer` on the matching source.
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
  the client's `price_per_gb_hour`; the income console is admin-only.
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