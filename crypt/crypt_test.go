package crypt

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
)

// memVault is an in-memory stand-in for ATP's encrypted secret store.
type memVault struct {
	mu sync.Mutex
	m  map[string]map[string]string
}

func newMemVault() *memVault { return &memVault{m: map[string]map[string]string{}} }

func (v *memVault) GetSecret(client, name string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.m[client]
	if !ok {
		return "", errors.New("no such client")
	}
	val, ok := s[name]
	if !ok {
		return "", errors.New("no such secret")
	}
	return val, nil
}

func (v *memVault) SetSecret(client, name, value, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.m[client] == nil {
		v.m[client] = map[string]string{}
	}
	v.m[client][name] = value
	return nil
}

// RFC 7914 §11 known-answer vectors for PBKDF2-HMAC-SHA256. The browser derives
// with Web Crypto, so the primitive has to match an independent implementation
// rather than only round-tripping against itself.
func TestPBKDF2KnownAnswers(t *testing.T) {
	vectors := []struct {
		password, salt string
		iter, dkLen    int
		want           string
	}{
		{"password", "salt", 1, 32,
			"120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{"password", "salt", 2, 32,
			"ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"},
		{"passwordPASSWORDpassword", "saltSALTsaltSALTsaltSALTsaltSALTsalt", 4096, 40,
			"348c89dbcbd32b2f32d814b8116e84cf2b17347ebc1800181c4e2a1fb8dd53e1c635518c7dac47e9"},
	}
	for _, v := range vectors {
		got := hex.EncodeToString(pbkdf2SHA256([]byte(v.password), []byte(v.salt), v.iter, v.dkLen))
		if got != v.want {
			t.Errorf("pbkdf2SHA256(%q, %q, %d, %d) = %s, want %s",
				v.password, v.salt, v.iter, v.dkLen, got, v.want)
		}
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	ks := New(newMemVault(), strings.Repeat("s", 40))
	const client = "acme"
	for _, plaintext := range []string{"hello", "", "multi\nline\ttitle", "unicode · ✓ ok", strings.Repeat("x", 5000)} {
		payload, err := ks.Seal(client, plaintext)
		if err != nil {
			t.Fatalf("Seal(%q): %v", plaintext, err)
		}
		if plaintext != "" && strings.Contains(payload, plaintext) {
			t.Fatalf("payload leaks plaintext %q: %s", plaintext, payload)
		}
		got, err := ks.Open(client, payload)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got != plaintext {
			t.Fatalf("round trip = %q, want %q", got, plaintext)
		}
	}
}

func TestPayloadShape(t *testing.T) {
	ks := New(newMemVault(), strings.Repeat("s", 40))
	payload, err := ks.Seal("acme", "body")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(payload, ".")
	if len(parts) != 3 {
		t.Fatalf("payload has %d fields, want 3 (vici compatibility): %q", len(parts), payload)
	}
	for i, want := range []int{saltLen, ivLen} {
		raw, err := base64.StdEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatalf("field %d is not standard base64: %v", i, err)
		}
		if len(raw) != want {
			t.Fatalf("field %d is %d bytes, want %d", i, len(raw), want)
		}
	}
	if _, err := base64.StdEncoding.DecodeString(parts[2]); err != nil {
		t.Fatalf("ciphertext is not standard base64: %v", err)
	}
}

// A client without a provisioned vault key must still get ciphertext, keyed
// from the master secret. This is what makes at-rest encryption unconditional.
func TestFallbackKeyStillEncrypts(t *testing.T) {
	v := newMemVault()
	ks := New(v, strings.Repeat("m", 40))
	payload, err := ks.Seal("fallback", "body")
	if err != nil {
		t.Fatal(err)
	}
	if v.m["fallback"] != nil {
		t.Fatal("fallback path should not have written to the vault")
	}
	got, err := ks.Open("fallback", payload)
	if err != nil || got != "body" {
		t.Fatalf("Open = %q, %v; want \"body\", nil", got, err)
	}
	if ks.HasKey("fallback") {
		t.Fatal("HasKey should be false for a client on fallback material")
	}
}

func TestDifferentClientsCannotDecryptEachOther(t *testing.T) {
	v := newMemVault()
	ks := New(v, strings.Repeat("m", 40))
	if err := ks.EnsureClient("alice"); err != nil {
		t.Fatal(err)
	}
	if err := ks.EnsureClient("bob"); err != nil {
		t.Fatal(err)
	}
	payload, err := ks.Seal("alice", "alice secret")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ks.Open("bob", payload); err == nil || got != "" {
		t.Fatalf("bob opened alice's payload: %q, %v", got, err)
	}
	if !ks.HasKey("alice") || !ks.HasKey("bob") {
		t.Fatal("EnsureClient did not provision vault keys")
	}
}

func TestOpenRejectsMalformed(t *testing.T) {
	ks := New(newMemVault(), strings.Repeat("m", 40))
	for _, bad := range []string{
		"", "onefield", "a.b", "a.b.c.d",
		"!!!.!!!.!!!",
		base64.StdEncoding.EncodeToString(make([]byte, 4)) + "." +
			base64.StdEncoding.EncodeToString(make([]byte, 12)) + "." +
			base64.StdEncoding.EncodeToString(make([]byte, 32)),
	} {
		if _, err := ks.Open("acme", bad); err == nil {
			t.Fatalf("Open(%q) succeeded, want error", bad)
		}
	}
}

// A payload sealed by the browser uses a random salt per payload. The server
// must re-derive rather than reusing its cached key, or every browser-authored
// comment would be undecryptable server-side.
func TestOpenForeignSaltRederives(t *testing.T) {
	ks := New(newMemVault(), strings.Repeat("m", 40))
	if _, err := ks.Seal("acme", "warm the cache"); err != nil {
		t.Fatal(err)
	}
	foreign, err := sealWithRandomSalt(ks, "acme", "browser wrote this")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ks.Open("acme", foreign)
	if err != nil {
		t.Fatalf("Open of a foreign-salt payload: %v", err)
	}
	if got != "browser wrote this" {
		t.Fatalf("Open = %q", got)
	}
}

// sealWithRandomSalt mimics vici.encrypt: same passphrase, fresh salt.
func sealWithRandomSalt(ks *KeyStore, client, plaintext string) (string, error) {
	pass, err := ks.Passphrase(client)
	if err != nil {
		return "", err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	aead, err := aeadFor(pass, salt)
	if err != nil {
		return "", err
	}
	iv := make([]byte, ivLen)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	return strings.Join([]string{
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(iv),
		base64.StdEncoding.EncodeToString(aead.Seal(nil, iv, []byte(plaintext), nil)),
	}, "."), nil
}

func TestOpenOKBlanksUnreadable(t *testing.T) {
	ks := New(newMemVault(), strings.Repeat("m", 40))
	payload, _ := ks.Seal("acme", "body")
	// Truncating the ciphertext must not panic or leak; the field just blanks.
	if got, ok := ks.OpenOK("acme", payload[:len(payload)-8]); ok {
		t.Fatalf("truncated payload opened as %q", got)
	}
	if got, ok := ks.OpenOK("acme", ""); !ok || got != "" {
		t.Fatalf("empty payload = %q, %v; want \"\", true", got, ok)
	}
}

func TestSealOrEmpty(t *testing.T) {
	ks := New(newMemVault(), "")
	if _, ok := ks.SealOrEmpty("acme", ""); !ok {
		t.Fatal("empty plaintext should succeed without a key")
	}
	// No master secret and no vault key: sealing must fail rather than fall back
	// to writing plaintext.
	if _, ok := ks.SealOrEmpty("acme", "body"); ok {
		t.Fatal("sealed without any key material")
	}
}

func TestKeyDerivationIsCachedAndStable(t *testing.T) {
	ks := New(newMemVault(), strings.Repeat("m", 40))
	p1, err := ks.Seal("acme", "one")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := ks.Seal("acme", "two")
	if err != nil {
		t.Fatal(err)
	}
	// Same client, same fixed salt: field 0 must match across payloads.
	if strings.Split(p1, ".")[0] != strings.Split(p2, ".")[0] {
		t.Fatal("server payloads used different salts; the derived key would be re-derived per item")
	}
	// And a separate store over the same master secret derives the same key, so
	// a restart can still read the cache.
	ks2 := New(newMemVault(), strings.Repeat("m", 40))
	if got, err := ks2.Open("acme", p1); err != nil || got != "one" {
		t.Fatalf("second store Open = %q, %v", got, err)
	}
}
