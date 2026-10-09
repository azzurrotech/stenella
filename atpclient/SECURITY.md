# atpclient — Security

## Assets protected

`atpclient` is a trust-bearing client, so the assets it touches are the
platform's highest-value ones:

- The **atp admin bearer token** cached in `Client.token`.
- The **admin username/password** held in `Client.user`/`Client.pass` and sent
  to atp's `/login`.
- **Vault secret values** returned by `GetSecret` (decrypted, admin-only).
- **Capability tokens and key blocks** issued through the token methods.
- The **atp handler itself**, whose auth/scoping decisions this package relies
  on.

## Threat model and mitigations actually implemented

- **No network exposure.** The handler is invoked in-process via
  `httptest.NewRequest`/`httptest.NewRecorder`. Requests never leave the
  process, so there is no transport for an attacker to tap and no TLS to
  misconfigure here.
- **Token caching with expiry.** `Login` rejects an empty token (`"atp login:
  empty token"`), and `Token` treats a token as valid only while
  `time.Now().Before(expiry)` (24 hours). An expired or empty token triggers a
  fresh login rather than an unauthenticated request.
- **Concurrency-safe token.** `Client.mu` (`sync.RWMutex`) guards `token` and
  `expiry`, so racing callers do not observe a torn state.
- **Explicit credential selection.** The bearer token is only attached as
  `X-ATP-Token` when non-empty (`do`). `JSONPublic` passes no token at all, so
  public calls such as `/login` cannot accidentally carry admin authority.
- **Fail-closed admin check.** `AdminValid` returns `true` only when atp answers
  `GET /api/summary` with `200`; any transport error or non-200 is `false`.
- **Bounded error disclosure.** `HTTPError.Body` is truncated to 400 bytes and
  decode failures mention at most 200 bytes of the body, so a huge or sensitive
  response is not echoed in full into logs or user-facing errors.
- **Bounded response read.** `maxBody = 64 << 20` caps what `do` reads from a
  response, preventing a hostile/broken handler from driving unbounded memory
  growth through this client.
- **Per-segment escaping.** `escapePath` escapes each slash-separated segment,
  so a table name cannot inject extra path structure into atp's pod route.

## Auth model

`atpclient` does not enforce authorization itself; it forwards credentials and
lets atp decide. Two modes exist:

- **Admin** — `Login`/`Token`/`JSON`/`JSONWithToken` send the admin bearer
  token. This is full platform administration.
- **Public** — `JSONPublic` sends nothing.
- **Request-scoped cookies** — only `AdminValid` accepts cookies from a caller;
  it forwards them to atp and reports atp's verdict, which is how the `web`
  package validates a browser's atp admin session.

## Honest limits / non-claims

- **No credentials are encrypted at rest by this package.** `Client.user` and
  `Client.pass` live in process memory for the life of the client.
- **No retry/backoff or circuit breaking.** A flapping atp handler causes one
  failed login attempt per expired token, not a storm-protected retry.
- **No caching of responses.** Every method issues a fresh handler call.
- **Not an authorization boundary.** A caller that constructs a `Client` with
  admin credentials can invoke every admin method; the security property is
  atp's, not this package's.
- **No audit log.** Usage logging is performed by atp's handler, not here.
- **`Handler()` returns the raw wrapped handler**, so a caller can bypass every
  method in this package and drive atp directly.

## Dependency note

`atpclient` imports **standard library only**:

```
bytes, encoding/json, errors, fmt, io, net/http, net/http/httptest,
net/url, strconv, strings, sync, time
```

There are no third-party modules.

## Reporting

Report suspected vulnerabilities privately to **security@azzurro.tech**. Do not
open a public issue.
