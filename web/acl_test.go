package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// aclRSS serves the same fixture body under a per-request source so the same
// body can back several sources with different classifications.
func aclRSS(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Fixture %s</title>
<item><title>%s headline</title><link>https://fx.test/%s</link><guid>%s-1</guid>
<pubDate>Wed, 23 Sep 2026 12:30:00 GMT</pubDate><description>body</description></item>
</channel></rss>`, r.URL.Path, r.URL.Path, r.URL.Path, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// publicItems reads the unauthenticated combined-items endpoint.
func publicItems(t *testing.T, h http.Handler, client string) []map[string]any {
	t.Helper()
	rec := webReq(t, h, "GET", "/s/feed/"+client+"/items", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("public items: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	mustDecode(t, rec, &out)
	return out.Items
}

// setupACLPortal creates a client, signs in as admin, and returns the admin
// cookie for driving the portal API.
func setupACLPortal(t *testing.T, h http.Handler, client string) *http.Cookie {
	t.Helper()
	admin := adminLogin(t, h)
	rec := webReq(t, h, "POST", "/s/api/admin/client",
		fmt.Sprintf(`{"id":%q,"name":"Acme","notes":""}`, client), []*http.Cookie{admin})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}
	return admin
}

// addACLSource registers a source with the given class and fetches it eagerly.
func addACLSource(t *testing.T, h http.Handler, admin *http.Cookie, client, url, class string) string {
	t.Helper()
	rec := webReq(t, h, "POST", "/s/api/portal/feeds?client="+client,
		fmt.Sprintf(`{"url":%q,"name":%q,"kind":"rss","acl_class":%q}`, url, class, class),
		[]*http.Cookie{admin})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add feed %s: %d %s", class, rec.Code, rec.Body.String())
	}
	var out struct {
		Source struct {
			ID       string `json:"id"`
			AclClass string `json:"acl_class"`
		} `json:"source"`
	}
	mustDecode(t, rec, &out)
	if out.Source.AclClass != class {
		t.Fatalf("source created with class %q, want %q", out.Source.AclClass, class)
	}
	// Fetch synchronously so the item is in cache before assertions.
	rec = webReq(t, h, "POST", "/s/api/portal/feeds/"+out.Source.ID+"/fetch?client="+client, "",
		[]*http.Cookie{admin})
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch %s: %d %s", class, rec.Code, rec.Body.String())
	}
	return out.Source.ID
}

// A source added without an explicit class must be stored as public, and its
// items must carry the classification so the frontend can group them.
func TestSourceDefaultsToPublicACL(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	fx := aclRSS(t)
	admin := setupACLPortal(t, h, "acme")

	rec := webReq(t, h, "POST", "/s/api/portal/feeds?client=acme",
		fmt.Sprintf(`{"url":%q,"name":"Plain","kind":"rss"}`, fx.URL+"/plain"), []*http.Cookie{admin})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add feed: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Source struct {
			AclClass string `json:"acl_class"`
		} `json:"source"`
	}
	mustDecode(t, rec, &out)
	if out.Source.AclClass != "public" {
		t.Fatalf("default class = %q, want public", out.Source.AclClass)
	}
}

// Private and protected items must never appear on the unauthenticated
// endpoint, and must still be visible to the signed-in client.
func TestPublicFeedExcludesPrivateAndProtected(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	fx := aclRSS(t)
	admin := setupACLPortal(t, h, "acme")

	webReq(t, h, "POST", "/s/api/admin/secrets?client=acme",
		`{"name":"portal","value":"sesame","note":"login"}`, []*http.Cookie{admin})
	portal := portalLogin(t, h, "acme", "sesame")

	addACLSource(t, h, admin, "acme", fx.URL+"/public1", "public")
	addACLSource(t, h, admin, "acme", fx.URL+"/private1", "private")
	addACLSource(t, h, admin, "acme", fx.URL+"/protected1", "protected")

	// Public reader sees exactly one item, and it is stamped public.
	pub := publicItems(t, h, "acme")
	if len(pub) != 1 {
		t.Fatalf("public endpoint returned %d items, want 1: %+v", len(pub), pub)
	}
	src, ok := pub[0]["source"].(map[string]any)
	if !ok {
		t.Fatalf("public item carries no source object: %+v", pub[0])
	}
	if src["acl_class"] != "public" {
		t.Fatalf("public item class = %v, want public", src["acl_class"])
	}
	body, _ := json.Marshal(pub)
	if want := "private1 headline"; contains(string(body), want) {
		t.Fatalf("private item leaked into public endpoint: %s", body)
	}

	// Authenticated client sees all three.
	rec := webReq(t, h, "GET", "/s/api/portal/items?client=acme&page=1&pageSize=50", "", []*http.Cookie{portal})
	if rec.Code != http.StatusOK {
		t.Fatalf("portal items: %d %s", rec.Code, rec.Body.String())
	}
	var all struct {
		Total int `json:"total"`
	}
	mustDecode(t, rec, &all)
	if all.Total != 3 {
		t.Fatalf("portal total = %d, want 3", all.Total)
	}

	// ...and can filter down to one class at a time.
	rec = webReq(t, h, "GET", "/s/api/portal/items?client=acme&acl=private&page=1&pageSize=50", "", []*http.Cookie{portal})
	var priv struct {
		Total int `json:"total"`
	}
	mustDecode(t, rec, &priv)
	if priv.Total != 1 {
		t.Fatalf("portal private total = %d, want 1", priv.Total)
	}
}

// An unknown class on add or update is rejected rather than silently coerced,
// so a client cannot believe it set a restriction that was never applied.
func TestInvalidACLClassRejected(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	fx := aclRSS(t)
	admin := setupACLPortal(t, h, "acme")

	rec := webReq(t, h, "POST", "/s/api/portal/feeds?client=acme",
		fmt.Sprintf(`{"url":%q,"name":"Bogus","kind":"rss","acl_class":"secret"}`, fx.URL+"/bogus"),
		[]*http.Cookie{admin})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid class accepted: %d %s", rec.Code, rec.Body.String())
	}

	// Add a valid one, then try to move it to an invalid class.
	id := addACLSource(t, h, admin, "acme", fx.URL+"/ok", "public")
	rec = webReq(t, h, "PUT", "/s/api/portal/feeds/"+id+"?client=acme",
		`{"acl_class":"nonsense"}`, []*http.Cookie{admin})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid update accepted: %d %s", rec.Code, rec.Body.String())
	}
}

// An update that omits acl_class must leave the existing classification alone.
func TestUpdateWithoutACLClassKeepsExisting(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	fx := aclRSS(t)
	admin := setupACLPortal(t, h, "acme")

	id := addACLSource(t, h, admin, "acme", fx.URL+"/keep", "private")

	// A name-only update (the shape the portal's Pause/Enable button sends).
	rec := webReq(t, h, "PUT", "/s/api/portal/feeds/"+id+"?client=acme",
		`{"name":"Renamed"}`, []*http.Cookie{admin})
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}

	rec = webReq(t, h, "GET", "/s/api/portal/feeds?client=acme", "", []*http.Cookie{admin})
	var out struct {
		Sources []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			AclClass string `json:"acl_class"`
		} `json:"sources"`
	}
	mustDecode(t, rec, &out)
	for _, src := range out.Sources {
		if src.ID != id {
			continue
		}
		if src.Name != "Renamed" {
			t.Fatalf("name not applied: %q", src.Name)
		}
		if src.AclClass != "private" {
			t.Fatalf("class reset to %q by a name-only update", src.AclClass)
		}
		return
	}
	t.Fatalf("source %s missing after update", id)
}

// The public endpoint must not honour an ?acl=private escalation attempt.
func TestPublicEndpointIgnoresACLParam(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	fx := aclRSS(t)
	admin := setupACLPortal(t, h, "acme")

	addACLSource(t, h, admin, "acme", fx.URL+"/esc1", "private")

	for _, q := range []string{"?acl=private", "?acl=protected", "?acl=private&pageSize=200"} {
		rec := webReq(t, h, "GET", "/s/feed/acme/items"+q, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("public items%s: %d", q, rec.Code)
		}
		var out struct {
			Total int `json:"total"`
		}
		mustDecode(t, rec, &out)
		if out.Total != 0 {
			t.Fatalf("public endpoint%s returned %d items, want 0", q, out.Total)
		}
	}
}

// Site posts carry their own class, and the public feed respects it.
func TestPostsACLClassIsPublicGated(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	setupACLPortal(t, h, "acme")

	if err := s.atp.CreateTable("acme/posts", []string{"title", "slug", "date", "acl_class"}); err != nil {
		t.Fatalf("create posts table: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rows := []map[string]string{
		{"id": "open-1", "title": "Open post", "slug": "open-1", "date": now, "acl_class": "public"},
		{"id": "shut-1", "title": "Members post", "slug": "shut-1", "date": now, "acl_class": "protected"},
		{"id": "bare-1", "title": "Unclassified post", "slug": "bare-1", "date": now},
	}
	for _, row := range rows {
		if _, err := s.atp.UpsertRecord("acme/posts", row); err != nil {
			t.Fatalf("upsert %s: %v", row["id"], err)
		}
	}

	titles := map[string]bool{}
	for _, it := range publicItems(t, h, "acme") {
		title, _ := it["title"].(string)
		titles[title] = true
	}
	if !titles["Open post"] {
		t.Errorf("public post missing from public feed")
	}
	if !titles["Unclassified post"] {
		t.Errorf("post with no class should default to public")
	}
	if titles["Members post"] {
		t.Errorf("protected post leaked into public feed")
	}
}

func contains(hay, needle string) bool {
	return len(needle) > 0 && len(hay) >= len(needle) && indexOf(hay, needle) >= 0
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
