package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"azzurrotech/atp"
)

// ---------------------------------------------------------------------------
// STX09 — the views and the PWA, as they leave the server
//
// The client logic is covered by node's test runner (web/tests/, run in the
// evidence step). These tests cover what the platform itself is responsible
// for: that the assets are embedded, materialised into the app directory and
// served, that the four views exist in the shipped shell, and that atp's
// generated PWA plumbing (manifest, service worker, OpenSearch) really works
// for this app.
// ---------------------------------------------------------------------------

// webServer builds a platform whose hosted application has its assets in place,
// exactly as main does at startup.
func webServer(t *testing.T) (*atp.Server, string) {
	t.Helper()
	srv, _ := newTestPlatform(t)
	appDir := filepath.Join(srv.Root(), "apps", "stenella")
	if err := materialiseWeb(webFS, appDir); err != nil {
		t.Fatalf("materialiseWeb: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appDir, "index.html")); err != nil {
		t.Fatalf("index.html was not materialised: %v", err)
	}
	return srv, appDir
}

// callHeaders performs one request and returns the response headers, for the
// checks that care about the content type rather than the body.
func callHeaders(t *testing.T, srv *atp.Server, method, path string) (int, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code, rec.Header()
}

func TestWebAssetsAreEmbeddedAndServed(t *testing.T) {
	srv, appDir := webServer(t)

	// every shipped file is on disk and is served over HTTP
	files := []struct {
		path string
		ct   string
		want string
	}{
		{"/stenella/", "text/html", "view-dashboard"},
		{"/stenella/index.html", "text/html", "view-dashboard"},
		{"/stenella/app.css", "text/css", "--accent"},
		{"/stenella/app.js", "javascript", "Stenella"},
		{"/stenella/lib.js", "javascript", "Stenella"},
		{"/stenella/world.js", "javascript", "StenellaWorld"},
		{"/stenella/icon.svg", "image/svg+xml", "<svg"},
	}
	for _, f := range files {
		code, body := call(t, srv, http.MethodGet, f.path, "")
		if code != http.StatusOK {
			t.Errorf("GET %s = %d", f.path, code)
			continue
		}
		if !strings.Contains(body, f.want) {
			t.Errorf("GET %s does not contain %q", f.path, f.want)
		}
		if _, hdr := callHeaders(t, srv, http.MethodGet, f.path); !strings.Contains(strings.ToLower(hdr.Get("Content-Type")), f.ct) {
			t.Errorf("GET %s Content-Type = %q, want %s", f.path, hdr.Get("Content-Type"), f.ct)
		}
	}

	// the browser tests are deliberately not shipped: they must not be
	// precached by the service worker or readable by a visitor
	if _, err := os.Stat(filepath.Join(appDir, "tests")); err == nil {
		t.Error("the client test directory was materialised into the served app")
	}
	if code, _ := call(t, srv, http.MethodGet, "/stenella/tests/lib.test.js", ""); code == http.StatusOK {
		t.Error("the client test file is served to the browser")
	}
}

func TestViewsCoverTheReadme(t *testing.T) {
	srv, _ := webServer(t)
	_, shell := call(t, srv, http.MethodGet, "/stenella/", "")
	_, js := call(t, srv, http.MethodGet, "/stenella/app.js", "")

	// the four coordinated views plus the smaller tools, as regions
	for _, want := range []string{
		`id="view-dashboard"`, `id="view-feed"`, `id="view-map"`, `id="view-tree"`,
		`id="view-inbox"`, `id="view-search"`, `id="view-sources"`, `id="view-console"`,
		`id="searchbar"`, `id="sources-body"`, `id="opml"`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("the shell has no %s region", want)
		}
	}

	// the dashboard's documented regions and the canvas charts
	for _, want := range []string{
		`id="filters"`, `id="kpis"`, `id="charts"`,
		`id="chart-source"`, `id="chart-time"`, `id="chart-category"`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("the dashboard has no %s", want)
		}
	}
	// …drawn with the plain canvas 2D API, not a chart library
	if !strings.Contains(js, "getContext") {
		t.Error("the charts are not drawn with the canvas 2D API")
	}
	low := strings.ToLower(js)
	for _, lib := range []string{"chart.js", "chartjs", "new chart(", "d3.", "d3(", "echarts", "plotly", "google.charts", "highcharts"} {
		if strings.Contains(low, lib) {
			t.Errorf("the front end depends on %q, which the baseline rule forbids", lib)
		}
	}

	// a KPI value element is produced for each card
	if !strings.Contains(js, "kpi-value") {
		t.Error("the dashboard does not render .kpi-value elements")
	}
	// live update: the charts are redrawn in place, never recreated
	if !strings.Contains(js, "redrawCharts") || !strings.Contains(js, "fitCanvas") {
		t.Error("the dashboard does not update its charts in place")
	}

	// the timeline, the map's offline base, the tree and the inbox
	for _, want := range []string{"id=\"timeline\"", "id=\"map\"", "id=\"tree\"", "id=\"inbox\""} {
		if !strings.Contains(shell, want) {
			t.Errorf("the shell has no %s", want)
		}
	}
	if !strings.Contains(js, "StenellaWorld") {
		t.Error("the map does not read the bundled world outline")
	}
}

// The baseline rule and the privacy rule meet here: the views must not reach
// for an external resource. A stray CDN, font or tile URL would be both a
// forbidden dependency and an outbound request that leaks the analyst's work.
func TestViewsMakeNoExternalRequest(t *testing.T) {
	srv, appDir := webServer(t)

	external := regexp.MustCompile(`(https?:)?//[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	allowedIn := map[string]bool{
		"icon.svg": true, // the SVG namespace, never fetched
	}
	err := filepath.WalkDir(appDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := filepath.Base(p)
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !external.MatchString(line) {
				continue
			}
			if allowedIn[name] && strings.Contains(line, "www.w3.org") {
				continue
			}
			// a comment that explains the rule is not a request
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "<!--") || strings.HasPrefix(trimmed, "- ") {
				continue
			}
			t.Errorf("%s references an external host: %s", name, trimmed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk app dir: %v", err)
	}

	// the same, through the server, for the shell
	_, shell := call(t, srv, http.MethodGet, "/stenella/", "")
	if external.MatchString(shell) && !strings.Contains(shell, "www.w3.org") {
		t.Errorf("the served shell references an external host:\n%s", shell)
	}
}

// The shell and the script must agree. A mistyped id would not fail a build or a
// Go test; it would fail silently in a browser. There is no browser here, so the
// agreement is checked statically: every id app.js looks up must exist in the
// served shell, and every console panel it renders must be a surface the server
// answers.
func TestViewsAndScriptAgreeOnElementIDs(t *testing.T) {
	srv, _ := webServer(t)
	_, shell := call(t, srv, http.MethodGet, "/stenella/", "")
	_, js := call(t, srv, http.MethodGet, "/stenella/app.js", "")

	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(shell, -1) {
		defined[m[1]] = true
	}
	looked := regexp.MustCompile(`\$\("([^"]+)"\)`).FindAllStringSubmatch(js, -1)
	if len(looked) < 15 {
		t.Fatalf("only %d id lookups found in app.js; the parser is probably wrong", len(looked))
	}
	for _, m := range looked {
		if !defined[m[1]] {
			t.Errorf("app.js looks up #%s, which the shell does not define", m[1])
		}
	}

	// every navigation link has a view section and a router entry
	for _, m := range regexp.MustCompile(`data-view="([^"]+)"`).FindAllStringSubmatch(shell, -1) {
		if !defined["view-"+m[1]] {
			t.Errorf("nav link %q has no #view-%s section", m[1], m[1])
		}
		entry := regexp.MustCompile(`(?m)^\s*"?` + regexp.QuoteMeta(m[1]) + `"?\s*:\s*\{`)
		if !entry.MatchString(js) {
			t.Errorf("nav link %q has no router entry in app.js", m[1])
		}
	}

	// the console panels the script renders are real surfaces
	panelBlock := regexp.MustCompile(`(?s)const PANELS = \[(.*?)\];`).FindStringSubmatch(js)
	if panelBlock == nil {
		t.Fatal("app.js has no PANELS list")
	}
	panels := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(panelBlock[1], -1)
	if len(panels) < 10 {
		t.Fatalf("only %d console panels listed", len(panels))
	}
	for _, m := range panels {
		if code, _ := call(t, srv, http.MethodGet, "/stenella/api/console/"+m[1], ""); code != http.StatusOK {
			t.Errorf("the console panel %q is not a surface the server answers (%d)", m[1], code)
		}
	}
}

func TestPWAIsGeneratedForThisApp(t *testing.T) {
	srv, _ := webServer(t)

	// the manifest is atp's, built from the app metadata
	code, body := call(t, srv, http.MethodGet, "/stenella/manifest.webmanifest", "")
	if code != http.StatusOK {
		t.Fatalf("manifest: %d %s", code, body)
	}
	var manifest struct {
		Name        string `json:"name"`
		ShortName   string `json:"short_name"`
		StartURL    string `json:"start_url"`
		Scope       string `json:"scope"`
		Display     string `json:"display"`
		ThemeColor  string `json:"theme_color"`
		Description string `json:"description"`
		Icons       []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
			Type  string `json:"type"`
		} `json:"icons"`
	}
	if err := json.Unmarshal([]byte(body), &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	if manifest.Name != "stenella" || manifest.ShortName != "stenella" {
		t.Errorf("manifest name = %q / %q", manifest.Name, manifest.ShortName)
	}
	if manifest.StartURL != "/stenella/" || manifest.Scope != "/stenella/" {
		t.Errorf("manifest start_url/scope = %q/%q", manifest.StartURL, manifest.Scope)
	}
	if manifest.Display != "standalone" {
		t.Errorf("manifest display = %q, want standalone (installable in its own window)", manifest.Display)
	}
	if manifest.Description == "" {
		t.Error("manifest has no description")
	}
	if len(manifest.Icons) == 0 || manifest.Icons[0].Src != "/stenella/icon.svg" {
		t.Errorf("manifest icons = %+v, want the shipped /stenella/icon.svg", manifest.Icons)
	}
	if manifest.Icons[0].Type != "image/svg+xml" {
		t.Errorf("manifest icon type = %q", manifest.Icons[0].Type)
	}

	// the icon the manifest names must actually exist
	if code, _ := call(t, srv, http.MethodGet, "/stenella/icon.svg", ""); code != http.StatusOK {
		t.Errorf("the manifest's icon is not served: %d", code)
	}

	// the service worker precaches the whole app shell
	code, body = call(t, srv, http.MethodGet, "/stenella/sw.js", "")
	if code != http.StatusOK {
		t.Fatalf("sw.js: %d %s", code, body)
	}
	for _, want := range []string{
		"/stenella/app.js", "/stenella/app.css", "/stenella/lib.js",
		"/stenella/world.js", "/stenella/index.html", "/stenella/icon.svg",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the service worker's SHELL does not precache %s", want)
		}
	}
	if !strings.Contains(body, "install") || !strings.Contains(body, "fetch") {
		t.Error("the service worker has no install/fetch handling")
	}
	// records are network-first, so a stale cache never hides a fresh collection
	if !strings.Contains(body, "/api/") {
		t.Error("the service worker does not treat the JSON endpoints as network-first")
	}

	// the OpenSearch description lets a browser search a collection
	code, body = call(t, srv, http.MethodGet, "/stenella/opensearch.xml", "")
	if code != http.StatusOK {
		t.Fatalf("opensearch.xml: %d %s", code, body)
	}
	if !strings.Contains(body, "{searchTerms}") || !strings.Contains(body, "stenella") {
		t.Errorf("OpenSearch document is not usable:\n%s", body)
	}
}

// The head fragments atp injects are what make the shell installable: without
// the manifest link and the search link the browser never sees them.
func TestShellHeadIsManagedByATP(t *testing.T) {
	srv, _ := webServer(t)
	_, shell := call(t, srv, http.MethodGet, "/stenella/", "")
	for _, want := range []string{
		`rel="manifest"`,
		`manifest.webmanifest`,
		`rel="search"`,
		`opensearch.xml`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("the served shell does not carry %s, so the PWA plumbing is invisible", want)
		}
	}
}
