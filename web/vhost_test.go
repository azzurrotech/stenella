package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"azzurrotech/stenella/atpclient"
)

// newTestWebHosted builds a stenella server with azzurro.tech mapped to the
// azzurrotech client site, the target deployment shape.
func newTestWebHosted(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	s, err := New(Config{
		Root:          root,
		AtpSecret:     webTestSecret,
		AdminUser:     "admin",
		AdminPassword: "s3cret",
		Libs:          map[string][]byte{"veni.js": {}, "vidi.js": {}, "vici.js": {}, "vini.js": {}},
		SiteHosts: map[string]string{
			"azzurro.tech":   "azzurrotech",
			"www.azzurro.tech": "azzurrotech",
		},
		PlatformPath: "/platform",
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}

	// Seed the azzurrotech client with a song silo index file and a pod table.
	if err := s.atp.CreateClient("azzurrotech", "Azzurro Technology", "azzurro.tech hosted site"); err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := s.atp.CreateSongFile("azzurrotech", atpclient.SongFileOp{Path: "index.html", Content: "<h1>Software Problem Solved</h1>", Overwrite: true}); err != nil {
		t.Fatalf("upload index.html: %v", err)
	}
	if err := s.atp.CreateSongFile("azzurrotech", atpclient.SongFileOp{Path: "shop.html", Content: "<h1>Shop</h1>", Overwrite: true}); err != nil {
		t.Fatalf("upload shop.html: %v", err)
	}
	if err := s.atp.CreateTable("azzurrotech/products", []string{"name", "price"}); err != nil {
		t.Fatalf("create products table: %v", err)
	}
	if _, err := s.atp.UpsertRecord("azzurrotech/products", map[string]string{
		"id": "azzurro-150", "name": "Azzurro 150", "price": "100",
	}); err != nil {
		t.Fatalf("upsert product: %v", err)
	}
	return s
}

func hostedReq(t *testing.T, s *Server, method, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// The mapped domain serves the client's hosted site at "/".
func TestHostDispatchServesClientSite(t *testing.T) {
	s := newTestWebHosted(t)

	rec := hostedReq(t, s, "GET", "http://azzurro.tech/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / on azzurro.tech: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Software Problem Solved") {
		t.Fatalf("expected silo index content, got %s", rec.Body.String())
	}

	rec = hostedReq(t, s, "GET", "http://www.azzurro.tech/shop.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /shop.html on www.azzurro.tech: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Shop") {
		t.Fatalf("expected silo shop content, got %s", rec.Body.String())
	}
}

// azzurro.tech/platform redirects to the stenella platform automatically.
func TestHostDispatchPlatformRedirect(t *testing.T) {
	s := newTestWebHosted(t)

	rec := hostedReq(t, s, "GET", "http://azzurro.tech/platform")
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("GET /platform: got %d, want 308", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/s/portal?client=azzurrotech" {
		t.Fatalf("redirect target: got %q, want /s/portal?client=azzurrotech", loc)
	}

	// A sub-path behaves the same.
	rec = hostedReq(t, s, "GET", "http://azzurro.tech/platform/admin")
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("GET /platform/admin: got %d, want 308", rec.Code)
	}
}

// The platform keeps its own namespace on a mapped host.
func TestHostDispatchKeepsPlatformNamespace(t *testing.T) {
	s := newTestWebHosted(t)

	rec := hostedReq(t, s, "GET", "http://azzurro.tech/s/portal")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /s/portal on azzurro.tech: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Client Portal") {
		t.Fatalf("expected portal page, got %s", rec.Body.String())
	}

	// The public site-data endpoint is platform-namespace too.
	rec = hostedReq(t, s, "GET", "http://azzurro.tech/s/data/azzurrotech/products")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /s/data/... on azzurro.tech: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// An unmapped host sees the normal platform homepage.
func TestHostDispatchUnmappedHost(t *testing.T) {
	s := newTestWebHosted(t)

	rec := hostedReq(t, s, "GET", "http://platform.internal/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / on unmapped host: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Everything hosted") {
		t.Fatalf("expected the platform homepage, got %s", rec.Body.String())
	}

	// A client still works under its /c/ prefix on the platform host.
	rec = hostedReq(t, s, "GET", "http://platform.internal/c/azzurrotech/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /c/azzurrotech/: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// Public site-data serves a client's own pod tables as JSON.
func TestSiteDataPublic(t *testing.T) {
	s := newTestWebHosted(t)

	rec := hostedReq(t, s, "GET", "http://localhost/s/data/azzurrotech/products")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /s/data/azzurrotech/products: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "azzurro-150") || !strings.Contains(body, "Azzurro 150") {
		t.Fatalf("expected the product record in the payload, got %s", body)
	}

	// Unknown client → 404.
	rec = hostedReq(t, s, "GET", "http://localhost/s/data/ghost/products")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown client: got %d, want 404", rec.Code)
	}

	// A table that does not exist yet reads as empty — a fresh client site
	// must not break before its first publish.
	rec = hostedReq(t, s, "GET", "http://localhost/s/data/azzurrotech/nope")
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown table: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"records":[]`) {
		t.Fatalf("unknown table should be empty, got %s", rec.Body.String())
	}
}

// The management tables (shares/links) never leak through the public endpoint.
func TestSiteDataPrivateTables(t *testing.T) {
	s := newTestWebHosted(t)

	for _, table := range []string{"shares", "links"} {
		rec := hostedReq(t, s, "GET", "http://localhost/s/data/azzurrotech/"+table)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("GET /s/data/../%s: got %d, want 403", table, rec.Code)
		}
	}
}