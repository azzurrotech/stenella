# links — Security

## Assets

- **The link graph** — which items point at which items or external URLs, and
  the relation/label metadata.
- **Share records and tokens** — a share token is the bearer capability that
  unlocks a public view onto one item, link or table.
- **The public share index** — the `share id → client` mapping that lets a
  token URL resolve without knowing the tenant.
- **Pod records and the filesystem junction** — the on-disk XML the junction
  symlinks point at.

## Threat model and mitigations

- **Path traversal via identifiers.** Every public entry point validates
  `client`, `id`, `fromID` and share `target` with `validIdentifier`: it rejects
  empty values, `"."` and `".."`, the separators and URL metacharacters
  `/\?#%`, any control character (`< 0x20`, `0x7f`) and values over 256 bytes.
  Filenames are additionally passed through `safe`, which maps every rune outside
  `[A-Za-z0-9._-]` to `_`.
- **Symlink escape.** `recordSymlink` cleans the resolved target and requires it
  to begin with `<root>/pod/`; otherwise it refuses with "symlink target escapes
  the pod root". Junctions therefore cannot link outside the pod tree.
- **Broken-record prevention.** `Store.Create` validates both endpoints through
  atp (`GetRecord`) before writing; a `url` end is parsed and accepted only when
  it is `http`/`https`, has a host, has no userinfo, and contains no
  `\r`, `\n` or `\t`. `Shares.Create` validates the target through atp (item,
  link, or table) before writing.
- **Token comparison.** `Share.Valid` requires a non-empty stored token, checks
  lengths, then uses `crypto/subtle.ConstantTimeCompare`. A mismatched length or
  value fails; a non-expiring share has `Expires == ""`.
- **Expiry.** When `Expires` is set, a share past its RFC-3339 timestamp is
  invalid, so a leaked token stops working after `days`.
- **Token exposure.** Random tokens are generated with `crypto/rand`
  (16 bytes → 32 hex chars); `List` deliberately omits tokens so list views do
  not leak them. `Get` returns the token because it is the owner's own record.

## Honest limits / non-claims

- **This package does no authentication or authorization.** It has no session or
  reader concept. Callers must gate `Create`, `Delete`, `Get`, `List`,
  `Edges` and `ClientFor`; the package will happily act for any client id passed
  in. Public share-serving must enforce `Valid` and expiry itself.
- **Tokens are stored in plaintext.** The pod share record holds the raw token
  (not a hash), so anyone who can read or query `<client>/shares` — or call
  `Get` — learns the capability. There is no rotation.
- **The index is clear and shared.** `index.json` maps share ids to client ids
  at mode `0644`; an id leaks its owning tenant. It is written with
  `os.WriteFile`, i.e. non-atomically, and its write errors are ignored.
- **The junction is not access control.** It is a set of real symlinks under the
  data root; any process with filesystem access can follow them to the pod XML.
  `recordSymlink` prevents linking *outside* the pod tree, not reading it.
- **No rate limiting or brute-force lockout.** Token guessing is bounded only by
  the token's entropy; the package does not throttle attempts.
- **No revocation by token.** A share is removed only by `Delete` (or expiry);
  there is no "revoke this presented token" path.
- **Title enrichment is best-effort.** Without a wired `TitleResolver`, `List`
  returns a blank title (the pod row has only a ciphertext title) and falls back
  to the clear `link` column. It never decrypts.
- **Deletes are tolerant, not transactional.** `Delete` errors from removing
  symlinks/manifest/dir are ignored after the pod record is deleted, so a
  filesystem failure can leave an orphan junction.
- **No update operation.** Records are immutable; changing a link or share means
  delete + create, and there is a window where the old one is gone.

## Dependency note

Imports the Go standard library (`crypto/rand`, `crypto/subtle`,
`encoding/hex`, `encoding/json`, `errors`, `fmt`, `net/url`, `os`,
`path/filepath`, `strings`, `sync`, `time`) plus the in-repo
`azzurrotech/stenella/atpclient` package. No third-party modules.

## Reporting

Report suspected vulnerabilities privately to **security@azzurro.tech**. Do not
open a public issue, and do not include a live share token, a private record's
contents or a working traversal payload in a report.
