package links

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShareItemLifecycle(t *testing.T) {
	cli, root := newTestStack(t)
	itemID := seedItem(t, cli, "acme")

	s := NewShares(root, cli)
	sh, err := s.Create("acme", ShareItem, itemID, "a great story", 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sh.Token == "" || sh.ID == "" {
		t.Fatalf("share = %+v", sh)
	}

	// Public index resolves the owner client without knowing it up front.
	if got, ok := s.ClientFor(sh.ID); !ok || got != "acme" {
		t.Errorf("ClientFor = %q, %v", got, ok)
	}

	// Junction carries a symlink to the shared pod item record.
	dir := filepath.Join(root, "stenella", "shares", "acme", sh.ID)
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("share manifest: %v", err)
	}
	target, err := os.Readlink(filepath.Join(dir, "target"))
	if err != nil {
		t.Fatalf("readlink target: %v", err)
	}
	if filepath.Join(dir, target) != filepath.Join(root, "pod", "acme", "items", itemID+".xml") {
		t.Errorf("target → %q", target)
	}
	if _, err := os.Stat(filepath.Join(dir, target)); err != nil {
		t.Errorf("target not resolvable: %v", err)
	}

	// Token validation is exact.
	got, err := s.Get("acme", sh.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Valid(sh.Token) {
		t.Errorf("Valid(real token) = false")
	}
	if got.Valid("wrong") {
		t.Errorf("Valid(wrong token) = true")
	}
	if got.Valid("") {
		t.Errorf("Valid(empty token) = true")
	}

	// List omits tokens.
	lst, err := s.List("acme", 0, 0)
	if err != nil || len(lst) != 1 {
		t.Fatalf("List: %v %d", err, len(lst))
	}
	if lst[0].Token != "" {
		t.Errorf("list leaked token %q", lst[0].Token)
	}

	// Delete removes the record, junction and index entry.
	if err := s.Delete("acme", sh.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.ClientFor(sh.ID); ok {
		t.Errorf("index still resolves deleted share")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("junction still exists after delete")
	}
	if _, err := cli.GetRecord(SharesTable("acme"), sh.ID); err == nil {
		t.Errorf("share record still exists after delete")
	}
}

func TestShareTable(t *testing.T) {
	cli, root := newTestStack(t)
	if err := cli.CreateClient("acme", "Acme", ""); err != nil {
		t.Fatalf("CreateClient: %v", err)
	}
	if _, err := cli.UpsertRecord("acme/items", map[string]string{"title": "row", "link": "https://x.test/1"}); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	s := NewShares(root, cli)
	sh, err := s.Create("acme", ShareTable, "items", "all items", 0)
	if err != nil {
		t.Fatalf("Create(table): %v", err)
	}
	dir := filepath.Join(root, "stenella", "shares", "acme", sh.ID)
	target, err := os.Readlink(filepath.Join(dir, "target"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if filepath.Join(dir, target) != filepath.Join(root, "pod", "acme", "items") {
		t.Errorf("table target → %q", target)
	}
	if fi, err := os.Stat(filepath.Join(dir, target)); err != nil || !fi.IsDir() {
		t.Errorf("table target not a dir: %v", err)
	}
}

func TestShareValidationAndExpiry(t *testing.T) {
	cli, root := newTestStack(t)
	itemID := seedItem(t, cli, "acme")
	s := NewShares(root, cli)

	// Unknown share kind.
	if _, err := s.Create("acme", "mystery", itemID, "", 0); err == nil {
		t.Errorf("bad kind should fail")
	}
	// Missing item target.
	if _, err := s.Create("acme", ShareItem, "missing-id", "", 0); err == nil {
		t.Errorf("missing item should fail")
	}
	// No client.
	if _, err := s.Create("", ShareItem, itemID, "", 0); err == nil {
		t.Errorf("empty client should fail")
	}

	// Expiry logic on the value object.
	now := time.Now().UTC()
	lapsed := Share{Token: "tok", Expires: now.Add(-time.Hour).Format(time.RFC3339)}
	if lapsed.Valid("tok") {
		t.Errorf("lapsed share validated")
	}
	fresh := Share{Token: "tok", Expires: now.Add(time.Hour).Format(time.RFC3339)}
	if !fresh.Valid("tok") {
		t.Errorf("fresh share rejected")
	}
	never := Share{Token: "tok"}
	if !never.Valid("tok") {
		t.Errorf("non-expiring share rejected")
	}
}

func TestSharesIndexPersists(t *testing.T) {
	cli, root := newTestStack(t)
	itemID := seedItem(t, cli, "acme")

	first := NewShares(root, cli)
	sh, err := first.Create("acme", ShareItem, itemID, "", 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A fresh manager loads the persisted index from disk.
	second := NewShares(root, cli)
	if got, ok := second.ClientFor(sh.ID); !ok || got != "acme" {
		t.Errorf("reloaded index: %q, %v", got, ok)
	}
}
