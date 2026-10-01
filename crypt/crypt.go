// Package crypt implements stenella's at-rest content encryption.
//
// Every item title, summary and body, and every comment body, is stored as an
// AES-256-GCM ciphertext envelope. Plaintext never touches disk: not in the feed
// cache, not in a pod record, not in a log.
//
// # Wire format
//
// A payload is three dot-separated standard-base64 fields, in the order
//
//	v1 = base64(salt).base64(iv).base64(ciphertext||tag)
//
// with a 16-byte PBKDF2 salt, a 12-byte GCM nonce and a 32-byte derived key.
// That is byte-for-byte the format vici.js produces and consumes
// (Web Crypto AES-256-GCM over PBKDF2-SHA256 at 100000 iterations), so the
// browser decrypts ingested bodies with `vici.decrypt(payload, passphrase)`
// and encrypts its own with `vici.encrypt(text, passphrase)` — no glue format
// on either side of the wire.
//
// The salt travels with the payload, which is what lets a single passphrase
// produce ciphertext under many salts. The server seals under one fixed
// per-client salt so it derives the AES key once per client and caches it
// (100000 PBKDF2 iterations per field would be unusable per item); the browser
// uses a fresh random salt per payload, and the server never needs to open
// those because browser-authored content is never searched server-side.
//
// # Key material
//
// A client has exactly one content key: 32 random bytes generated at client
// creation and held in ATP's AES-256-GCM vault. Its standard-base64 encoding is
// the "passphrase" both ends use. Clients that predate this package — or whose
// vault entry was removed — fall back to a key derived from the platform master
// secret with HMAC-SHA256, so encryption is unconditional rather than opt-in.
//
// See plan §4.4: the boundary enforced here is encryption at rest plus
// encrypt-before-submit for browser-authored content. Releasing a content key
// to an authenticated session is deliberate and documented, not an oversight.
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// PBKDF2Iterations matches the iteration count in vici.js. The two must agree
// or a payload sealed by one end cannot be opened by the other.
const PBKDF2Iterations = 100000

const (
	keyLen  = 32
	saltLen = 16
	ivLen   = 12
)

// Vault names. Both entries live in ATP's encrypted secret store, so the
// content key is itself encrypted at rest under the platform master secret.
const (
	SecretKey  = "content_key"
	SecretSalt = "content_salt"
)

// Errors returned by this package.
var (
	// ErrNoMasterSecret is returned when a client has no vault key and no
	// master secret was configured to derive one from. Without one there is no
	// key to seal under, and silently storing plaintext would be worse.
	ErrNoMasterSecret = errors.New("crypt: no master secret configured for content key derivation")
	// ErrMalformed is returned when a payload is not three base64 fields of the
	// expected lengths.
	ErrMalformed = errors.New("crypt: malformed payload")
)

// Vault is the subset of ATP's secret store the key store needs.
type Vault interface {
	GetSecret(client, name string) (string, error)
	SetSecret(client, name, value, note string) error
}

// KeyStore hands out per-client content keys and seals/opens payloads with
// them. Derived AES keys are cached, so the 100000-iteration PBKDF2 runs once
// per client per process rather than once per field.
type KeyStore struct {
	vault        Vault
	masterSecret []byte

	mu    sync.Mutex
	cache map[string]*clientKey
}

// clientKey is one client's content key material plus the AEAD built from it.
type clientKey struct {
	// passphrase is the base64 encoding of the raw 32-byte key. It is what the
	// browser is handed so its PBKDF2 derivation matches ours.
	passphrase string
	// salt is the fixed PBKDF2 salt this store seals under. It is also the
	// payload's first field, so the browser reads it rather than being told.
	salt []byte
	aead cipher.AEAD
}

// New creates a KeyStore over vault. masterSecret seeds the fallback derivation
// for clients that have no vault key; it should be the same value stenella was
// started with.
func New(vault Vault, masterSecret string) *KeyStore {
	return &KeyStore{
		vault:        vault,
		masterSecret: []byte(masterSecret),
		cache:        map[string]*clientKey{},
	}
}

// ---- public API --------------------------------------------------------------

// Passphrase returns the client content key in the form both ends use as the
// PBKDF2 password. Callers must gate it behind an authenticated session for that
// client; it is the single capability that makes ciphertext readable.
func (k *KeyStore) Passphrase(client string) (string, error) {
	ck, err := k.key(client)
	if err != nil {
		return "", err
	}
	return ck.passphrase, nil
}

// Seal encrypts plaintext under the client's content key and returns the
// vici-compatible payload envelope.
func (k *KeyStore) Seal(client, plaintext string) (string, error) {
	ck, err := k.key(client)
	if err != nil {
		return "", err
	}
	iv := make([]byte, ivLen)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	ct := ck.aead.Seal(nil, iv, []byte(plaintext), nil)
	return strings.Join([]string{
		base64.StdEncoding.EncodeToString(ck.salt),
		base64.StdEncoding.EncodeToString(iv),
		base64.StdEncoding.EncodeToString(ct),
	}, "."), nil
}

// SealOrEmpty is Seal for callers that would rather degrade than fail a fetch:
// on error it returns an empty payload and false, and the caller keeps the
// plaintext field empty rather than writing plaintext to disk.
func (k *KeyStore) SealOrEmpty(client, plaintext string) (string, bool) {
	if plaintext == "" {
		return "", true
	}
	p, err := k.Seal(client, plaintext)
	if err != nil {
		return "", false
	}
	return p, true
}

// Open decrypts a payload produced by Seal or by vici.encrypt.
// An empty payload is an error rather than an empty plaintext: a caller
// reaching Open with "" has lost track of which field it is reading, and
// silently returning "" would turn that into a blank record. Callers that treat
// "no ciphertext" as "no content" use OpenOK.
func (k *KeyStore) Open(client, payload string) (string, error) {
	if payload == "" {
		return "", ErrMalformed
	}
	ck, err := k.key(client)
	if err != nil {
		return "", err
	}
	salt, iv, ct, err := parsePayload(payload)
	if err != nil {
		return "", err
	}
	// A payload sealed by the browser carries its own random salt, so the key
	// must be re-derived for it rather than reusing the cached one.
	aead := ck.aead
	if !bytesEqual(salt, ck.salt) {
		aead, err = aeadFor(ck.passphrase, salt)
		if err != nil {
			return "", err
		}
	}
	pt, err := aead.Open(nil, iv, ct, nil)
	if err != nil {
		return "", fmt.Errorf("crypt: open payload: %w", err)
	}
	return string(pt), nil
}

// OpenOK is Open for callers that treat an unreadable payload as "no content"
// rather than an error — a rotation or a tampered record should blank one field,
// not fail a whole page.
func (k *KeyStore) OpenOK(client, payload string) (string, bool) {
	if payload == "" {
		return "", true
	}
	out, err := k.Open(client, payload)
	if err != nil {
		return "", false
	}
	return out, true
}

// EnsureClient creates the client's content key if it does not have one yet.
// It is called from client provisioning so a brand-new client is encrypted from
// its first record rather than silently falling back.
func (k *KeyStore) EnsureClient(client string) error {
	if k.vault == nil {
		return nil
	}
	if _, err := k.vault.GetSecret(client, SecretKey); err == nil {
		// Already provisioned; make sure a salt exists too so the derived key is
		// not silently the master-secret fallback.
		if _, err := k.vault.GetSecret(client, SecretSalt); err == nil {
			return nil
		}
	}
	raw := make([]byte, keyLen)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	if err := k.vault.SetSecret(client, SecretKey, base64.StdEncoding.EncodeToString(raw), "stenella content key (encrypt-before-decrypt)"); err != nil {
		return fmt.Errorf("crypt: store content key: %w", err)
	}
	if err := k.vault.SetSecret(client, SecretSalt, base64.StdEncoding.EncodeToString(salt), "stenella content key salt"); err != nil {
		return fmt.Errorf("crypt: store content salt: %w", err)
	}
	// Drop any cached fallback so the next call picks up the real key.
	k.mu.Lock()
	delete(k.cache, client)
	k.mu.Unlock()
	return nil
}

// HasKey reports whether the client has a vault-held content key, as opposed to
// relying on the master-secret fallback. The portal uses it to warn an operator
// that a client is on fallback material.
func (k *KeyStore) HasKey(client string) bool {
	if k.vault == nil {
		return false
	}
	v, err := k.vault.GetSecret(client, SecretKey)
	return err == nil && v != ""
}

// ---- internals ---------------------------------------------------------------

// key returns the cached client key, deriving and caching it on first use.
func (k *KeyStore) key(client string) (*clientKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if ck, ok := k.cache[client]; ok {
		return ck, nil
	}
	ck, err := k.derive(client)
	if err != nil {
		return nil, err
	}
	k.cache[client] = ck
	return ck, nil
}

// derive reads the vault-held key material, or falls back to the master secret.
func (k *KeyStore) derive(client string) (*clientKey, error) {
	var passphrase string
	var salt []byte
	if k.vault != nil {
		if v, err := k.vault.GetSecret(client, SecretKey); err == nil && v != "" {
			passphrase = v
			if s, err := k.vault.GetSecret(client, SecretSalt); err == nil && s != "" {
				if raw, derr := base64.StdEncoding.DecodeString(s); derr == nil && len(raw) == saltLen {
					salt = raw
				}
			}
		}
	}
	if passphrase == "" {
		if len(k.masterSecret) == 0 {
			return nil, ErrNoMasterSecret
		}
		passphrase = base64.StdEncoding.EncodeToString(expand(k.masterSecret, contentKeyLabel, client))
		salt = expand(k.masterSecret, contentSaltLabel, client)[:saltLen]
	}
	if len(salt) != saltLen {
		// A vault entry with no usable salt still needs one; deriving it from the
		// passphrase keeps the browser's derivation identical.
		sum := sha256.Sum256([]byte(passphrase + "\x00salt"))
		salt = sum[:saltLen]
	}
	aead, err := aeadFor(passphrase, salt)
	if err != nil {
		return nil, err
	}
	return &clientKey{passphrase: passphrase, salt: salt, aead: aead}, nil
}

// Domain separation labels for the master-secret fallback. Using distinct
// labels for the key and the salt means one never leaks structure about the
// other, and neither can be confused with a vault-held value.
const (
	contentKeyLabel  = "stenella/content-key\x00"
	contentSaltLabel = "stenella/content-salt\x00"
)

// expand derives 32 bytes from the master secret, a fixed label and the client
// id. HMAC-SHA256 is already a PRF, so this is a sufficient one-step KDF and
// avoids inventing a home-grown HKDF.
func expand(secret []byte, label, client string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label))
	mac.Write([]byte(client))
	return mac.Sum(nil)
}

func aeadFor(passphrase string, salt []byte) (cipher.AEAD, error) {
	key := pbkdf2SHA256([]byte(passphrase), salt, PBKDF2Iterations, keyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func parsePayload(payload string) (salt, iv, ct []byte, err error) {
	parts := strings.Split(payload, ".")
	if len(parts) != 3 {
		return nil, nil, nil, ErrMalformed
	}
	salt, err = base64.StdEncoding.DecodeString(parts[0])
	if err != nil || len(salt) != saltLen {
		return nil, nil, nil, ErrMalformed
	}
	iv, err = base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(iv) != ivLen {
		return nil, nil, nil, ErrMalformed
	}
	ct, err = base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(ct) < 16 {
		return nil, nil, nil, ErrMalformed
	}
	return salt, iv, ct, nil
}

// bytesEqual is a constant-time slice comparison. The values compared here are
// salts, so timing does not really matter — but the comparison is free to make
// constant-time and a future reader should not have to re-derive that.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// pbkdf2SHA256 is RFC 8018 PBKDF2 with HMAC-SHA256 as the PRF. It is written
// out here because crypto/pbkdf2 only exists from Go 1.24 and this module
// targets 1.22 — twenty lines beats a version bump.
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := prf.Size()
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	var counter [4]byte
	u := make([]byte, hLen)
	t := make([]byte, hLen)
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(counter[:], uint32(block))
		prf.Write(counter[:])
		u = prf.Sum(u[:0])
		copy(t, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for i := range t {
				t[i] ^= u[i]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
