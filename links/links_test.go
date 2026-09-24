package links

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	atpweb "azzurrotech/atp/web"
	"azzurrotech/stenella/atpclient"
)

const testSecret = "links-test-master-secret-0123456789abcdef-zz"

// newTestStack builds an in-process atp service (with pod) plus the
// atpclient wrapper, exactly like the web layer wires them, over a temp root.
func newTestStack(t *testing.T) (*atpclient.Client, string) {
	t.Helper()
	root := t.TempDir()
	svc, err := atpweb.NewATPService(atpweb.Options{
		Root:          root,
		Secret:        testSecret,
		AdminUser:     "admin",
		AdminPassword: "p",
	})
	if err != nil {
		t.Fatalf("NewATPService: %v", err)
	}
	cli := atpclient.New(svc.Handler(), "admin", "p")
	if err := cli.Login(); err != nil {
		t.Fatalf("atp login: %v", err)
	}
	return cli, root
}

// seedItem registers client (if not yet present) and inserts one item record,
// returning its id. It is safe to call more than once per client.
func seedItem(t *testing.T, cli *atpclient.Client, client string) string {
	t.Helper()
	ensureClient(t, cli, client)
	rec, err := cli.UpsertRecord(ItemsTable(client), map[string]string{
		"title": "A feed story",
		"link":  "https://story.test/1",
		"guid":  "g1",
	})
	if err != nil {
		t.Fatalf("UpsertRecord: %v", err)
	}
	return rec["id"]
}

// ensureClient creates the client, tolerating an existing registration.
func ensureClient(t *testing.T, cli *atpclient.Client, client string) {
	t.Helper()
	if err := cli.CreateClient(client, "Acme Inc", "smoke"); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return
		}
		t.Fatalf("CreateClient: %v", err)
	}
}

// assertJunction verifies the physical symlink junction matches the spec.
func assertJunction(t *testing.T, root, client, id string, links map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "stenella", "links", client, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("junction dir %s: %v", dir, err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["manifest.json"] {
		t.Errorf("junction missing manifest.json")
	}
	for name, expected := range links {
		if !names[name] {
			t.Errorf("junction missing %s", name)
			continue
		}
		linkPath := filepath.Join(dir, name)
		fi, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatalf("lstat %s: %v", linkPath, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink", name)
			continue
		}
		target, err := os.Readlink(linkPath)
		if err != nil {
			t.Fatalf("readlink %s: %v", linkPath, err)
		}
		resolved := filepath.Join(dir, target)
		if expected != "" && resolved != expected {
			t.Errorf("%s target = %q, want %q", name, resolved, expected)
		}
		// The symlink must resolve to something real on disk.
		if _, err := os.Stat(filepath.Join(dir, target)); err != nil {
			t.Errorf("%s target does not resolve: %v", name, err)
		}
	}
	// manifest parses.
	mb, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatalf("manifest json: %v", err)
	}
	if m.ID != id || m.Client != client || m.FromID == "" {
		t.Errorf("manifest = %+v", m)
	}
}

func TestCreateItemLinkAndJunction(t *testing.T) {
	cli, root := newTestStack(t)
	fromID := seedItem(t, cli, "acme")
	toID := seedItem(t, cli, "acme")

	s := New(root, cli)
	ln, err := s.Create("acme", fromID, ToItem, toID, "", "mentions", "see also")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ln.ID == "" || ln.FromID != fromID || ln.ToID != toID || ln.Relation != "mentions" {
		t.Fatalf("link = %+v", ln)
	}

	// The link record is a pod row.
	rec, err := cli.GetRecord(LinksTable("acme"), ln.ID)
	if err != nil {
		t.Fatalf("link record missing: %v", err)
	}
	if rec["relation"] != "mentions" || rec["to_kind"] != "item" {
		t.Errorf("record = %+v", rec)
	}

	wantFrom := filepath.Join(root, "pod", "acme", "items", fromID+".xml")
	wantTo := filepath.Join(root, "pod", "acme", "items", toID+".xml")
	assertJunction(t, root, "acme", ln.ID, map[string]string{
		"from.xml": wantFrom,
		"to.xml":   wantTo,
	})

	// Enriched list resolves both titles.
	links, err := s.List("acme", 0, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("links = %d", len(links))
	}
	if links[0].FromTitle != "A feed story" || links[0].ToTitle != "A feed story" {
		t.Errorf("enrichment = %+v", links[0])
	}
	if links[0].FromLink != "https://story.test/1" {
		t.Errorf("from link = %q", links[0].FromLink)
	}

	// Get round-trip.
	got, err := s.Get("acme", ln.ID)
	if err != nil || got.FromID != fromID {
		t.Fatalf("Get: %v %+v", err, got)
	}

	// Delete cleans the junction + record.
	if err := s.Delete("acme", ln.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "stenella", "links", "acme", ln.ID)); !os.IsNotExist(err) {
		t.Errorf("junction dir still exists after delete")
	}
	if _, err := cli.GetRecord(LinksTable("acme"), ln.ID); err == nil {
		t.Errorf("link record still exists after delete")
	}
}

func TestCreateURLLinkAndJunction(t *testing.T) {
	cli, root := newTestStack(t)
	fromID := seedItem(t, cli, "acme")

	s := New(root, cli)
	ln, err := s.Create("acme", fromID, ToURL, "", "https://outside.test/article", "related", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	dir := filepath.Join(root, "stenella", "links", "acme", ln.ID)
	toURLBytes, err := os.ReadFile(filepath.Join(dir, "to.url"))
	if err != nil {
		t.Fatalf("to.url probe: %v", err)
	}
	if strings.TrimSpace(string(toURLBytes)) != "https://outside.test/article" {
		t.Errorf("to.url = %q", string(toURLBytes))
	}

	// to.xml is a symlink back to the probe file inside the junction.
	linkPath := filepath.Join(dir, "to.xml")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink to.xml: %v", err)
	}
	if filepath.Base(target) != "to.url" {
		t.Errorf("to.xml → %q, want .../to.url", target)
	}
	if _, err := os.Stat(linkPath); err != nil {
		t.Errorf("to.xml not resolvable: %v", err)
	}

	// Enrichment treats the URL as both title and link.
	lst, err := s.List("acme", 0, 0)
	if err != nil || len(lst) != 1 {
		t.Fatalf("List: %v %d", err, len(lst))
	}
	if lst[0].ToTitle != "https://outside.test/article" || lst[0].ToLink != "https://outside.test/article" {
		t.Errorf("url enrichment = %+v", lst[0])
	}
}

func TestCreateValidation(t *testing.T) {
	cli, root := newTestStack(t)
	fromID := seedItem(t, cli, "acme")
	otherID := seedItem(t, cli, "acme")
	s := New(root, cli)

	// Missing from item.
	if _, err := s.Create("acme", "nope", ToItem, otherID, "", "", ""); err == nil {
		t.Errorf("missing from item should fail")
	}
	// Missing to item.
	if _, err := s.Create("acme", fromID, ToItem, "nope", "", "", ""); err == nil {
		t.Errorf("missing to item should fail")
	}
	// Invalid to_kind.
	if _, err := s.Create("acme", fromID, "carrot", "", "", "", ""); err == nil {
		t.Errorf("invalid to_kind should fail")
	}
	// URL link requires http(s).
	if _, err := s.Create("acme", fromID, ToURL, "", "ftp://nope", "", ""); err == nil {
		t.Errorf("non-http url should fail")
	}
	// No client.
	if _, err := s.Create("", fromID, ToItem, otherID, "", "", ""); err == nil {
		t.Errorf("empty client should fail")
	}
	// Nothing was persisted by the failures.
	lst, err := s.List("acme", 0, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(lst) != 0 {
		t.Errorf("failed creates leaked records: %+v", lst)
	}
}

func TestSafe(t *testing.T) {
	// Spaces and path separators are sanitized; dots are allowed.
	if got := safe("ac me/../x"); got != "ac_me_.._x" {
		t.Errorf("safe = %q", got)
	}
	if got := safe(""); got != "x" {
		t.Errorf("safe('') = %q", got)
	}
	if got := safe("plain-name_1.test"); got != "plain-name_1.test" {
		t.Errorf("safe = %q", got)
	}
}
