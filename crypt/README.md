# crypt — at-rest content encryption with vici-compatible envelopes

**Module** `azzurrotech/stenella` · **standard library only**

`crypt` is stenella's content-encryption layer. Every item title, summary and
body, and every comment body, is stored as an AES-256-GCM ciphertext envelope.
Plaintext never touches disk: not in the feed cache, not in a pod record, not in
a log.

## Overview

The package belongs to the `azzurrotech/stenella` module (Go 1.22). It provides
the `KeyStore` that the rest of the platform seals and opens content with.

- The **feed engine** (`feed.Engine`) receives a `*crypt.KeyStore` through
  `feed.Options.Cipher` and seals item bodies on write / opens them on read.
- The **web server** constructs the store (`crypt.New(atp, cfg.AtpSecret)` in
  `web/web.go`) and shares it with the collaboration store (`web/collab.go`) for
  comment bodies.
- `atpclient.Client` satisfies the `Vault` interface: its pod/atp secret store
  holds each client's content key and salt.

The wire format is deliberately identical to `static/vici`'s browser helper:
`vici.encrypt`/`vici.decrypt` produce and consume the same three-field envelope,
so a browser can encrypt its own writes and decrypt server-ingested bodies with
no glue format on either side.

## Public API

### Constants and errors

- `const PBKDF2Iterations = 100000` — iteration count, matching `vici.js`.
- `const SecretKey = "content_key"` — vault entry holding the base64 content key.
- `const SecretSalt = "content_salt"` — vault entry holding the base64 salt.
- `var ErrNoMasterSecret` — no vault key and no master secret to derive one from.
- `var ErrMalformed` — payload is not three base64 fields of the expected lengths.

### Types

- `type Vault interface` — the subset of ATP's secret store the key store needs:
  - `GetSecret(client, name string) (string, error)`
  - `SetSecret(client, name, value, note string) error`
- `type KeyStore struct` — hands out per-client keys and caches derived AEADs.

### Functions and methods

- `func New(vault Vault, masterSecret string) *KeyStore` — build the store;
  `masterSecret` seeds the fallback derivation.
- `func (k *KeyStore) Passphrase(client string) (string, error)` — the content
  key in the form both ends use as the PBKDF2 password. Gate behind an
  authenticated session.
- `func (k *KeyStore) Seal(client, plaintext string) (string, error)` — encrypt
  and return the envelope.
- `func (k *KeyStore) SealOrEmpty(client, plaintext string) (string, bool)` —
  `Seal`, but on error or empty input returns empty and `false`/`true`.
- `func (k *KeyStore) Open(client, payload string) (string, error)` — decrypt;
  an empty payload is `ErrMalformed`.
- `func (k *KeyStore) OpenOK(client, payload string) (string, bool)` — `Open`
  that treats unreadable ciphertext as "no content".
- `func (k *KeyStore) EnsureClient(client string) error` — create the client's
  key and salt if absent; called at provisioning time.
- `func (k *KeyStore) HasKey(client string) bool` — whether the client has a
  vault key rather than relying on the master-secret fallback.

## Usage

```go
vault := atpclient.New(svc.Handler(), "admin", "pw") // implements crypt.Vault
ks := crypt.New(vault, masterSecret)

if err := ks.EnsureClient("acme"); err != nil {
    return err
}
payload, err := ks.Seal("acme", "a private title")
if err != nil {
    return err
}
plain, err := ks.Open("acme", payload) // "a private title"
```

To hand the key to an authenticated browser session:

```go
pass, err := ks.Passphrase("acme")
// vici.decrypt(payload, pass) / vici.encrypt(text, pass)
```

## Configuration / inputs

- `PBKDF2Iterations = 100000`; derived key length 32 bytes, salt 16 bytes,
  GCM nonce 12 bytes (unexported `keyLen`, `saltLen`, `ivLen`).
- Wire format: `base64(salt).base64(iv).base64(ciphertext||tag)`, standard
  base64 (not URL-safe), `||` meaning concatenation with the GCM tag appended.
- Vault names: `content_key`, `content_salt`.
- Fallback derivation uses HMAC-SHA256 over `(masterSecret, label, client)` with
  the domain-separation labels `stenella/content-key\x00` and
  `stenella/content-salt\x00`.
- Derived keys are cached per client for the process lifetime (`KeyStore.cache`),
  so the 100 000-iteration PBKDF2 runs once per client, not once per field.

## Testing

From `/home/matthew/Projects/Platform/stenella`:

```bash
go test ./crypt/...
```

The suite covers PBKDF2 known-answer vectors (RFC 7914 §11), seal/open round
trips including empty and multi-kilobyte plaintext, envelope shape and field
lengths, the master-secret fallback, cross-client isolation, malformed-payload
rejection, re-derivation for a foreign (browser) salt, `OpenOK`/`SealOrEmpty`
degradation, and key caching/stability across two stores.

## Design notes

- **Unconditional encryption.** A client with no vault key falls back to a key
  derived from the master secret; the engine encrypts rather than storing
  plaintext. It returns `ErrNoMasterSecret` only when neither exists.
- **One fixed server salt, many browser salts.** The server seals under the
  client's fixed salt and caches the derived key. A payload carrying a different
  salt (browser-authored) forces `Open` to re-derive with that salt.
- **Constants live in the payload.** The salt is the envelope's first field, so
  the browser reads it rather than being told.
- **No plaintext on failure.** `SealOrEmpty` returns `("", false)`; callers keep
  the field empty. `OpenOK` blanks an unreadable field instead of failing a page.
- **Salt comparison is constant-time** (`bytesEqual`), even though salt is not
  secret, so future readers do not have to re-derive that it is safe.
- `pbkdf2SHA256` is implemented locally because `crypto/pbkdf2` requires Go 1.24
  and this module targets 1.22.
