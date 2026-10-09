# crypt — Security

## Assets

- **Content plaintext:** item titles, summaries and bodies, and comment bodies.
  These are the values the package keeps off disk.
- **Per-client content keys:** the base64 passphrase and salt held in—and handed
  out by—the vault.
- **The platform master secret:** the fallback input from which a content key is
  derived when a client has no vault entry.
- **The derived AEAD key cache:** the in-process copy of each client's key.

## Threat model and mitigations

The boundary enforced here is **encryption at rest plus encrypt-before-submit**
for browser-authored content. An attacker who reads the filesystem (feed cache,
pod XML, logs) without a content key should get ciphertext, not prose.

- **Confidentiality + integrity:** AES-256-GCM (`cipher.NewGCM`). The tag is
  appended to the ciphertext and verified on `Open`; a tampered or truncated
  payload fails instead of returning altered plaintext.
- **Key derivation:** PBKDF2-HMAC-SHA256 at `PBKDF2Iterations = 100000`, 32-byte
  key, 16-byte salt, 12-byte random GCM nonce per seal (`crypto/rand`). This
  matches `vici.js` so each end can open the other's payload.
- **Per-client isolation:** distinct clients derive distinct keys (vault key or
  HMAC-SHA256 expansion keyed by client id). A payload sealed for one client
  does not open under another; the test suite asserts this.
- **Deterministic fallback:** with no vault key, the key is
  `HMAC-SHA256(masterSecret, label, client)`, and the salt is a separate
  label-derivation. Distinct `stenella/content-key\x00` /
  `stenella/content-salt\x00` labels keep the two from leaking structure about
  each other. Encryption is therefore unconditional rather than opt-in.
- **Fail-closed:** `ErrNoMasterSecret` is returned rather than storing plaintext;
  `SealOrEmpty` returns false and callers keep the field empty; `OpenOK` blanks
  an unreadable field rather than aborting a page. `Open` rejects an empty
  payload as `ErrMalformed` so a misread field cannot become a blank record.
- **Vault-backed keys:** key material lives in ATP's encrypted secret store
  (`SecretKey`, `SecretSalt`) and is only read through the `Vault` interface.
  `HasKey` lets the portal warn that a client is on fallback material.

## Honest limits / non-claims

- **Encryption at rest only.** `crypt` does not encrypt data in transit and does
  not manage TLS.
- **Key release is deliberate.** `Passphrase` hands the content key to whoever
  holds the client's authenticated session. Possession of the passphrase is
  sufficient to decrypt; the package does not itself authorize the caller. The
  plan explicitly documents this.
- **No key rotation.** There is no re-key or re-encrypt path here. A vault key
  removed after records were sealed makes those records unreadable (they blank
  via `OpenOK`) rather than recoverable.
- **No additional authenticated data.** `Seal`/`Open` pass `nil` AAD, so no
  metadata (client id, record id, field name) is cryptographically bound to a
  payload. A ciphertext could be moved between fields without detection.
- **Fallback is derivable, not random.** Anyone with the master secret and a
  client id can derive that client's fallback key; the master secret must be
  treated as the crown jewel.
- **Salt fallback is passphrase-derived.** When a vault key exists without a
  usable salt, the salt is `SHA-256(passphrase || "\x00salt")` truncated to 16
  bytes. This is a compatibility shim, not a design recommendation.
- **In-memory exposure.** Decrypted plaintext and derived keys live in process
  memory; a user with access to the process, its memory or its core dumps can
  read them.
- **No server-side body search.** Because bodies are ciphertext, server queries
  cannot read them; that is intentional and is handled by other packages.
- **Not post-quantum.** AES-256-GCM and PBKDF2-SHA256 are classical primitives.

## Dependency note

Standard library only: `crypto/aes`, `crypto/cipher`, `crypto/hmac`,
`crypto/rand`, `crypto/sha256`, `encoding/base64`, `encoding/binary`, `errors`,
`fmt`, `strings`, `sync`.

## Reporting

Report suspected vulnerabilities privately to **security@azzurro.tech**. Do not
open a public issue, and do not include real content keys, master secrets or
decrypted user content in a report.
