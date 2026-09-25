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
	return newTestWebHostedWithRouting(t, map[string]string{
		"azzurro.tech":     "azzurrotech",
		"www.azzurro.tech": "azzurrotech",
	}, "/platform")
}

func newTestWebHostedWithRouting(t *testing.T, siteHosts map[string]string, platformPath string) *Server {
	t.Helper()
	root := t.TempDir()
	s, err := New(Config{
		Root:          root,
		AtpSecret:     webTestSecret,
		AdminUser:     "admin",
		AdminPassword: "s3cret",
		Libs:          map[string][]byte{"veni.js": {}, "vidi.js": {}, "vici.js": {}, "vini.js": {}},
		SiteHosts:     siteHosts,
		PlatformPath:  platformPath,
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
	if err := s.atp.CreateSongFile("azzurrotech", atpclient.SongFileOp{Path: "platformish.html", Content: "<h1>Platformish</h1>", Overwrite: true}); err != nil {
		t.Fatalf("upload platformish.html: %v", err)
	}
	if err := s.atp.CreateSongFile("azzurrotech", atpclient.SongFileOp{Path: "control-panelish.html", Content: "<h1>Control panelish</h1>", Overwrite: true}); err != nil {
		t.Fatalf("upload control-panelish.html: %v", err)
	}
	if err := s.atp.CreateSongFile("azzurrotech", atpclient.SongFileOp{Path: "healthcheck", Content: "<h1>Site healthcheck</h1>", Overwrite: true}); err != nil {
		t.Fatalf("upload healthcheck: %v", err)
	}
	if err := s.atp.CreateSongFile("azzurrotech", atpclient.SongFileOp{Path: "login-page", Content: "<h1>Site login page</h1>", Overwrite: true}); err != nil {
		t.Fatalf("upload login-page: %v", err)
	}
	if err := s.atp.CreateSongFile("azzurrotech", atpclient.SongFileOp{Path: "docs/index.html", Content: "<h1>Docs index</h1>", Overwrite: true}); err != nil {
		t.Fatalf("upload docs/index.html: %v", err)
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

func hostedReqWithHost(t *testing.T, s *Server, method, target, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
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

	// A similarly prefixed name is part of the hosted site, not the platform
	// redirect. Dispatch must match complete path segments.
	rec = hostedReq(t, s, "GET", "http://azzurro.tech/platformish.html")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Platformish") {
		t.Fatalf("GET /platformish.html: got %d, want hosted content (%s)", rec.Code, rec.Body.String())
	}
}

// The platform keeps its own namespace on a mapped host.
func TestSafeMappedSitePath(t *testing.T) {
	for _, path := range []string{"/", "/shop.html", "/docs/", "/docs/index.html"} {
		if !safeMappedSitePath(path) {
			t.Errorf("safeMappedSitePath(%q) = false", path)
		}
	}
	for _, path := range []string{"", "shop.html", "/../other", "/docs/../other", "/docs//index", `\\docs`} {
		if safeMappedSitePath(path) {
			t.Errorf("safeMappedSitePath(%q) = true", path)
		}
	}
}

func TestHostDispatchDoesNotExposeAnotherClientSilo(t *testing.T) {
	s := newTestWebHosted(t)
	if err := s.atp.CreateClient("other", "Other tenant", ""); err != nil {
		t.Fatalf("create other client: %v", err)
	}
	if err := s.atp.CreateSongFile("other", atpclient.SongFileOp{Path: "marker.html", Content: "OTHER TENANT MARKER", Overwrite: true}); err != nil {
		t.Fatalf("upload other marker: %v", err)
	}

	rec := hostedReq(t, s, http.MethodGet, "http://azzurro.tech/c/other/marker.html")
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "OTHER TENANT MARKER") {
		t.Fatalf("cross-tenant mapped path: status=%d body=%q", rec.Code, rec.Body.String())
	}
	rec = hostedReq(t, s, http.MethodGet, "http://azzurro.tech/../other/marker.html")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("mapped traversal path: status=%d, want 404", rec.Code)
	}

	// The mapped client's own explicit /c route remains available.
	rec = hostedReq(t, s, http.MethodGet, "http://azzurro.tech/c/azzurrotech/shop.html")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Shop") {
		t.Fatalf("mapped client explicit path: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHostDispatchPreservesStaticPathsWithATPPrefixes(t *testing.T) {
	s := newTestWebHosted(t)
	for _, tc := range []struct {
		path, marker string
	}{
		{"/healthcheck", "Site healthcheck"},
		{"/login-page", "Site login page"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := hostedReq(t, s, http.MethodGet, "http://azzurro.tech"+tc.path)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.marker) {
				t.Fatalf("static prefix path: status=%d body=%q", rec.Code, rec.Body.String())
			}
		})
	}

	// Exact ATP-owned paths keep their platform meaning rather than becoming
	// files in the mapped silo.
	rec := hostedReq(t, s, http.MethodGet, "http://azzurro.tech/health")
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "Site healthcheck") {
		t.Fatalf("exact /health unexpectedly served the static file: %q", rec.Body.String())
	}
}

func TestHostDispatchRewritesSongRedirects(t *testing.T) {
	s := newTestWebHosted(t)
	rec := hostedReq(t, s, http.MethodGet, "http://azzurro.tech/docs?from=site")
	if rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusFound {
		t.Fatalf("directory redirect status = %d, want redirect", rec.Code)
	}
	location := rec.Header().Get("Location")
	if strings.Contains(location, "azzurrotech/") || strings.Contains(location, "/c/azzurrotech/") {
		t.Fatalf("Song internal prefix leaked in redirect: %q", location)
	}
	if !strings.Contains(location, "docs") || !strings.Contains(location, "from=site") {
		t.Fatalf("redirect did not preserve public path/query: %q", location)
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
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("site-data response has no ETag")
	}
	req := httptest.NewRequest(http.MethodGet, "/s/data/azzurrotech/products", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional site-data request: got %d, want 304", rec.Code)
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

func TestHostOnlyCanonicalizationAndValidation(t *testing.T) {
	valid := []struct {
		host string
		want string
	}{
		{"azzurro.tech", "azzurro.tech"},
		{"AZZURRO.TECH.", "azzurro.tech"},
		{"AzZuRrO.TeCh.:443", "azzurro.tech"},
		{"127.0.0.1:8084", "127.0.0.1"},
		{"[2001:DB8::A]:443", "2001:db8::a"},
		{"[::1]", "::1"},
	}
	for _, tt := range valid {
		if got := hostOnly(tt.host); got != tt.want {
			t.Errorf("hostOnly(%q) = %q, want %q", tt.host, got, tt.want)
		}
	}

	invalid := []string{
		"",
		" azzurro.tech",
		"azzurro tech",
		"azzurro.tech/path",
		"https://azzurro.tech",
		"user@azzurro.tech",
		"azzurro.tech:",
		"azzurro.tech:https",
		"azzurro.tech:65536",
		"azzurro.tech:80:90",
		"foo..example",
		"-foo.example",
		"foo-.example",
		"::1",
	}
	for _, host := range invalid {
		if got := hostOnly(host); got != "" {
			t.Errorf("hostOnly(%q) = %q, want invalid", host, got)
		}
	}
}

func TestNormalizeSiteHostsValidatesMappings(t *testing.T) {
	got, err := normalizeSiteHosts(map[string]string{
		" Example.COM.:8443 ": " azzurrotech ",
		"WWW.AZZURRO.TECH":    "azzurrotech",
	})
	if err != nil {
		t.Fatalf("normalizeSiteHosts: %v", err)
	}
	if len(got) != 2 || got["example.com"] != "azzurrotech" || got["www.azzurro.tech"] != "azzurrotech" {
		t.Fatalf("normalized mappings = %#v", got)
	}

	for name, mappings := range map[string]map[string]string{
		"bad host":        {"https://azzurro.tech": "azzurrotech"},
		"empty client":    {"azzurro.tech": ""},
		"unsafe client":   {"azzurro.tech": "../escape"},
		"canonical clash": {"azzurro.tech": "one", "AZZURRO.TECH.": "two"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeSiteHosts(mappings); err == nil {
				t.Fatal("normalizeSiteHosts should reject this mapping")
			}
		})
	}
}

func TestNormalizePlatformPath(t *testing.T) {
	valid := []struct {
		configured string
		want       string
	}{
		{"", "/platform"},
		{"control-panel/", "/control-panel"},
		{" /control//panel/// ", "/control/panel"},
		{"/control/../admin", "/admin"},
		{"/login-page", "/login-page"},
		{"/healthcheck", "/healthcheck"},
	}
	for _, tt := range valid {
		got, err := normalizePlatformPath(tt.configured)
		if err != nil {
			t.Errorf("normalizePlatformPath(%q): %v", tt.configured, err)
		} else if got != tt.want {
			t.Errorf("normalizePlatformPath(%q) = %q, want %q", tt.configured, got, tt.want)
		}
	}

	invalid := []string{"/", "//control", "/s", "/s/portal", "/api", "/c/client", "/login/panel", "/control?tab=1", "/control#fragment"}
	for _, configured := range invalid {
		if got, err := normalizePlatformPath(configured); err == nil {
			t.Errorf("normalizePlatformPath(%q) = %q, want an error", configured, got)
		}
	}
}

func TestNewRejectsInvalidRoutingConfig(t *testing.T) {
	for name, cfg := range map[string]Config{
		"malformed host": {
			Root: t.TempDir(), AtpSecret: webTestSecret,
			SiteHosts: map[string]string{"https://azzurro.tech": "azzurrotech"},
		},
		"empty client": {
			Root: t.TempDir(), AtpSecret: webTestSecret,
			SiteHosts: map[string]string{"azzurro.tech": ""},
		},
		"invalid platform path": {
			Root: t.TempDir(), AtpSecret: webTestSecret,
			PlatformPath: "/s",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("New should reject invalid routing configuration")
			}
		})
	}
}

func TestNewNormalizesRoutingConfig(t *testing.T) {
	s := newTestWebHostedWithRouting(t, map[string]string{
		" Example.COM.:8443 ": " azzurrotech ",
	}, "control-panel//")

	if s.cfg.PlatformPath != "/control-panel" {
		t.Fatalf("PlatformPath = %q, want /control-panel", s.cfg.PlatformPath)
	}
	if s.cfg.SiteHosts["example.com"] != "azzurrotech" || len(s.cfg.SiteHosts) != 1 {
		t.Fatalf("SiteHosts = %#v", s.cfg.SiteHosts)
	}

	// The request authority is case-insensitive and may include a trailing
	// dot and a different edge/listener port than the configured authority.
	rec := hostedReqWithHost(t, s, "GET", "http://canonical.invalid/", "ExAmPlE.CoM.:9443")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Software Problem Solved") {
		t.Fatalf("canonical mapped host: got %d, want hosted site (%s)", rec.Code, rec.Body.String())
	}

	rec = hostedReqWithHost(t, s, "GET", "http://canonical.invalid/control-panel?view=admin", "example.com")
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("custom platform root: got %d, want 308", rec.Code)
	}
	rec = hostedReqWithHost(t, s, "GET", "http://canonical.invalid/control-panel/admin", "example.com")
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("custom platform sub-path: got %d, want 308", rec.Code)
	}

	rec = hostedReqWithHost(t, s, "GET", "http://canonical.invalid/control-panelish.html", "example.com")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Control panelish") {
		t.Fatalf("custom platform boundary: got %d, want hosted content (%s)", rec.Code, rec.Body.String())
	}
}

func TestHostDispatchKeepsSNamespaceBoundary(t *testing.T) {
	s := newTestWebHosted(t)
	for _, target := range []string{"/s", "/s/", "/s/portal?view=full"} {
		t.Run(target, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "http://azzurro.test"+target, nil)
			req.Host = "azzurro.tech"
			rec := httptest.NewRecorder()
			s.hostDispatch(next).ServeHTTP(rec, req)

			if !called || rec.Code != http.StatusNoContent {
				t.Fatalf("mapped %q: called=%v status=%d, want platform handler/204", target, called, rec.Code)
			}
		})
	}
}

func TestHostDispatchMalformedHostUsesPlatform(t *testing.T) {
	s := newTestWebHosted(t)
	rec := hostedReqWithHost(t, s, "GET", "http://platform.invalid/", "azzurro.tech:not-a-port")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Everything hosted") {
		t.Fatalf("malformed Host: got %d, want platform homepage (%s)", rec.Code, rec.Body.String())
	}
}

func TestCloneReqPreservesQueryString(t *testing.T) {
	for _, target := range []string{"/shop.html?tag=go%2Btest&empty=", "/shop.html?"} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			cloned := cloneReq(req, "/c/azzurrotech/shop.html")
			if cloned.URL.RawQuery != req.URL.RawQuery || cloned.URL.ForceQuery != req.URL.ForceQuery {
				t.Fatalf("query = (%q, force=%v), want (%q, force=%v)", cloned.URL.RawQuery, cloned.URL.ForceQuery, req.URL.RawQuery, req.URL.ForceQuery)
			}
			if req.RequestURI == "" {
				t.Fatal("test request has no RequestURI")
			}
			wantRequestURI := "/c/azzurrotech" + req.RequestURI
			if cloned.RequestURI != wantRequestURI {
				t.Fatalf("RequestURI = %q, want %q", cloned.RequestURI, wantRequestURI)
			}
			if req.URL.Path != "/shop.html" || req.RequestURI != target {
				t.Fatal("cloneReq mutated the original request")
			}
		})
	}
}
