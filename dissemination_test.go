package main

import (
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"azzurrotech/atp"
)

// ---------------------------------------------------------------------------
// STX07 — the shareable feed and its JSON twin
// ---------------------------------------------------------------------------

func TestSplitFeedName(t *testing.T) {
	cases := map[string][2]string{
		"news.xml":      {"news", "xml"},
		"news.json":     {"news", "json"},
		"all.xml":       {"all", "xml"},
		"supply.json":   {"supply", "json"},
		"news":          {"", ""},
		"news.atom":     {"", ""},
		"":              {"", ""},
		".xml":          {"", ""},
		"news.xml.back": {"", ""},
	}
	for in, want := range cases {
		c, f := splitFeedName(in)
		if c != want[0] || f != want[1] {
			t.Errorf("splitFeedName(%q) = (%q,%q), want (%q,%q)", in, c, f, want[0], want[1])
		}
	}
}

// resetSources replaces the seeded configuration with the test's own sources.
func resetSources(t *testing.T, a *App, srcs ...Source) {
	t.Helper()
	existing, err := a.store.Sources()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range existing {
		if err := a.store.DeleteSource(s.ID); err != nil {
			t.Fatal(err)
		}
	}
	for i := range srcs {
		s := srcs[i]
		if err := a.store.PutSource(&s); err != nil {
			t.Fatal(err)
		}
	}
}

// feedInvariants asserts the whole served document — not just its parsed items
// — carries no link and no private element. This is the promise the README makes
// about the shareable feed, so it is checked on the bytes that leave the server.
func feedInvariants(t *testing.T, label, body string) {
	t.Helper()
	if containsLink(body) {
		t.Errorf("%s: document contains a link", label)
	}
	for _, bad := range []string{
		"<link", "</link", "<guid", "<atom:link", "<enclosure", "<comments",
		"<cloud", "<docs", "<generator", "<managingEditor", "<webMaster",
		"<author", "<dc:creator", "href=", "http://", "https://", "www.",
	} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(bad)) {
			t.Errorf("%s: document contains %q:\n%s", label, bad, body)
		}
	}
	for _, bad := range []string{"@", "desk@fixture.example", "jsmith", "Some Person", "evil.example"} {
		if strings.Contains(body, bad) {
			t.Errorf("%s: document leaks %q:\n%s", label, bad, body)
		}
	}
}

func TestPerCollectionFeedIsServedLinkFree(t *testing.T) {
	fs := newFixtureServer(t)
	srv, app := newTestPlatform(t)

	resetSources(t, app,
		Source{Name: "News wire", URL: fs.URL + "/rss.xml", Kind: "feed", Collection: "news", ACL: "public", Enabled: true},
		Source{Name: "Gov wire", URL: fs.URL + "/atom.xml", Kind: "feed", Collection: "government", ACL: "public", Enabled: true},
	)
	// point the collection engines at the fixture server
	app.client = fs.Client()
	app.aggs = map[string]*aggregator{}

	code, body := call(t, srv, http.MethodGet, "/stenella/feeds/news.xml", "")
	if code != http.StatusOK {
		t.Fatalf("news.xml: %d %s", code, body)
	}
	if !strings.Contains(body, "<rss") || !strings.Contains(body, "Vendor") {
		t.Errorf("news.xml is not the expected document:\n%s", body)
	}
	feedInvariants(t, "news.xml", body)
	// only the news source contributes
	if strings.Contains(body, "Statement by") || strings.Contains(body, "RDF headline") {
		t.Errorf("news.xml mixed in another collection's items:\n%s", body)
	}

	// the JSON twin is the same sanitised item set
	code, body = call(t, srv, http.MethodGet, "/stenella/feeds/news.json", "")
	if code != http.StatusOK {
		t.Fatalf("news.json: %d %s", code, body)
	}
	var newsJSON struct {
		Collection string    `json:"collection"`
		Sources    []string  `json:"sources"`
		Count      int       `json:"count"`
		Items      []Article `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &newsJSON); err != nil {
		t.Fatalf("news.json is not JSON: %v\n%s", err, body)
	}
	if newsJSON.Collection != "news" || newsJSON.Count != len(newsJSON.Items) || newsJSON.Count == 0 {
		t.Errorf("news.json envelope = %+v", newsJSON)
	}
	if len(newsJSON.Sources) != 1 || newsJSON.Sources[0] != "News wire" {
		t.Errorf("news.json sources = %v, want the news source only", newsJSON.Sources)
	}
	feedInvariants(t, "news.json", body)

	// "all" is the union of every configured source
	code, all := call(t, srv, http.MethodGet, "/stenella/feeds/all.xml", "")
	if code != http.StatusOK {
		t.Fatalf("all.xml: %d %s", code, all)
	}
	if !strings.Contains(all, "Vendor") || !strings.Contains(all, "Statement by") {
		t.Errorf("all.xml is not the union of both sources:\n%s", all)
	}
	feedInvariants(t, "all.xml", all)

	// a per-collection feed serves only that collection
	code, gov := call(t, srv, http.MethodGet, "/stenella/feeds/government.xml", "")
	if code != http.StatusOK {
		t.Fatalf("government.xml: %d %s", code, gov)
	}
	if strings.Contains(gov, "Vendor") {
		t.Errorf("government.xml leaked the news source:\n%s", gov)
	}
	feedInvariants(t, "government.xml", gov)

	// and the JSON twin of the whole set is /stenella/api/aggregate
	code, agg := call(t, srv, http.MethodGet, "/stenella/api/aggregate", "")
	if code != http.StatusOK {
		t.Fatalf("aggregate: %d %s", code, agg)
	}
	if !strings.Contains(agg, "Vendor") || !strings.Contains(agg, "Statement by") {
		t.Errorf("aggregate is not the whole set:\n%s", agg)
	}
	feedInvariants(t, "aggregate", agg)
}

func TestFeedRejectsBadNamesAndUnknownCollections(t *testing.T) {
	fs := newFixtureServer(t)
	srv, app := newTestPlatform(t)
	resetSources(t, app,
		Source{Name: "News wire", URL: fs.URL + "/rss.xml", Kind: "feed", Collection: "news", Enabled: true},
	)
	app.client = fs.Client()
	app.aggs = map[string]*aggregator{}

	cases := []struct {
		path string
		want int
	}{
		{"/stenella/feeds/news.xml", http.StatusOK},
		{"/stenella/feeds/all.json", http.StatusOK},
		{"/stenella/feeds/sports.xml", http.StatusNotFound}, // no such collection
		{"/stenella/feeds/news", http.StatusNotFound},       // no format
		{"/stenella/feeds/news.atom", http.StatusNotFound},  // unsupported format
	}
	for _, c := range cases {
		code, body := call(t, srv, http.MethodGet, c.path, "")
		if code != c.want {
			t.Errorf("GET %s = %d, want %d (%s)", c.path, code, c.want, body)
		}
		if c.want == http.StatusNotFound && containsLink(body) {
			t.Errorf("GET %s: error body carries a link: %s", c.path, body)
		}
	}
}

// ---------------------------------------------------------------------------
// STX07 — exports
// ---------------------------------------------------------------------------

func exportFixture(t *testing.T) (*atp.Server, *App) {
	t.Helper()
	srv, app := newTestPlatform(t)
	when := time.Date(2026, 9, 20, 15, 0, 0, 0, time.UTC)

	entityID, err := app.store.observeEntity("place", "Ottawa", "item-export", when)
	if err != nil {
		t.Fatal(err)
	}
	it := putItem(t, app.store, "item-export", "Ransomware report from Ottawa", "CBC", "news", when, []string{"cyber"})
	it.Geo = formatGeo(45.4215, -75.6972)
	it.Entities = []string{entityID}
	if err := app.store.PutItem(it); err != nil {
		t.Fatal(err)
	}
	// one unrelated item, so a query has something to exclude
	putItem(t, app.store, "item-other", "Harvest forecast", "ANSA", "news", when, []string{"economy"})

	if code, body := call(t, srv, http.MethodPost, "/stenella/api/comments",
		`{"item":"item-export","body":"Worth a look."}`); code != http.StatusCreated {
		t.Fatalf("seed comment: %d %s", code, body)
	}
	return srv, app
}

func TestExportJSONIsTheSanitisedProjection(t *testing.T) {
	srv, _ := exportFixture(t)

	code, body := call(t, srv, http.MethodGet, "/stenella/api/export.json", "")
	if code != http.StatusOK {
		t.Fatalf("export.json: %d %s", code, body)
	}
	var doc struct {
		Feed       string       `json:"feed"`
		Collection string       `json:"collection"`
		Count      int          `json:"count"`
		Items      []ExportItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("export is not JSON: %v\n%s", err, body)
	}
	if doc.Feed != "stenella" || doc.Count != 2 || len(doc.Items) != 2 {
		t.Fatalf("export envelope = %+v", doc)
	}

	var got ExportItem
	for _, it := range doc.Items {
		if it.Title == "Ransomware report from Ottawa" {
			got = it
		}
	}
	if got.Title == "" {
		t.Fatalf("the item is missing from the export: %s", body)
	}
	if len(got.Entities) != 1 || got.Entities[0] != "place:Ottawa" {
		t.Errorf("entities = %v, want the resolved kind:name", got.Entities)
	}
	if got.Geo != formatGeo(45.4215, -75.6972) {
		t.Errorf("geo = %q", got.Geo)
	}
	if got.Comments != 1 {
		t.Errorf("comments = %d, want 1", got.Comments)
	}
	if got.Source != "CBC" || got.Collection != "news" || len(got.Categories) != 1 {
		t.Errorf("provenance/classification lost: %+v", got)
	}
	// the export is the sanitised projection: no link, no author field at all
	feedInvariants(t, "export.json", body)
	for _, bad := range []string{"\"link\"", "\"author\"", "\"guid\"", "\"body\""} {
		if strings.Contains(body, bad) {
			t.Errorf("export carries %s: %s", bad, body)
		}
	}

	// and the same query language filters the export
	if code, body := call(t, srv, http.MethodGet, "/stenella/api/export.json?q=tag:cyber", ""); code != http.StatusOK ||
		!strings.Contains(body, "Ransomware") || strings.Contains(body, "Harvest") {
		t.Errorf("query filter on export: %d %s", code, body)
	}
	if _, body := call(t, srv, http.MethodGet, "/stenella/api/export.json?q=tag:nothing", ""); mustJSON(t, body)["count"].(float64) != 0 {
		t.Errorf("empty query returned items: %s", body)
	}
}

func TestExportCSVIsParseableAndFixedShape(t *testing.T) {
	srv, _ := exportFixture(t)

	code, body := call(t, srv, http.MethodGet, "/stenella/api/export.csv?q=", "")
	if code != http.StatusOK {
		t.Fatalf("export.csv: %d %s", code, body)
	}
	if !strings.HasPrefix(body, "\ufeff") {
		t.Error("CSV is missing its UTF-8 BOM, so spreadsheets will mangle non-ASCII text")
	}
	// the CSV carries no link or address, exactly like every other export
	feedInvariants(t, "export.csv", body)

	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatalf("CSV does not parse: %v\n%s", err, body)
	}
	if len(rows) != 3 {
		t.Fatalf("CSV has %d rows, want a header + 2 items:\n%s", len(rows), body)
	}
	if strings.Join(rows[0], ",") != strings.Join(csvHeader, ",") {
		t.Errorf("CSV header = %v, want %v", rows[0], csvHeader)
	}

	var row []string
	for _, r := range rows[1:] {
		if r[0] == "Ransomware report from Ottawa" {
			row = r
		}
	}
	if row == nil {
		t.Fatalf("the item row is missing:\n%s", body)
	}
	col := map[string]string{}
	for i, name := range csvHeader {
		col[name] = row[i]
	}
	if col["source"] != "CBC" || col["collection"] != "news" || col["categories"] != "cyber" {
		t.Errorf("CSV provenance/class = %v", col)
	}
	if col["entities"] != "place:Ottawa" {
		t.Errorf("CSV entities = %q", col["entities"])
	}
	if col["lat"] != formatCoord(45.4215) || col["lon"] != formatCoord(-75.6972) {
		t.Errorf("CSV coordinates = %q,%q", col["lat"], col["lon"])
	}
	if col["published"] != "2026-09-20T15:00:00Z" {
		t.Errorf("CSV published = %q", col["published"])
	}
	if col["comments"] != "1" {
		t.Errorf("CSV comments = %q", col["comments"])
	}
}

// ---------------------------------------------------------------------------
// STX07 — OPML, the operator's subscription list
// ---------------------------------------------------------------------------

const opmlImportFixture = `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head><title>Import test</title></head>
  <body>
    <outline text="Canada" title="Canada">
      <outline type="rss" text="CSE advisories" title="CSE advisories" xmlUrl="https://import.example/cse.xml"/>
      <outline type="rss" text="GC news" title="GC news" xmlUrl="https://import.example/gc.xml"/>
    </outline>
    <outline text="Stand-alone wire" type="rss" xmlUrl="https://import.example/standalone.xml"/>
    <outline text="A folder with nothing in it" title="Empty"/>
    <outline text="Broken entry" type="rss" xmlUrl=""/>
  </body>
</opml>`

func TestOPMLExportGroupsSourcesAndIsAFullSubscriptionList(t *testing.T) {
	srv, app := newTestPlatform(t)

	code, body := call(t, srv, http.MethodGet, "/stenella/api/opml", "")
	if code != http.StatusOK {
		t.Fatalf("opml export: %d %s", code, body)
	}
	if !strings.Contains(body, "<opml") || !strings.Contains(body, "<outline") {
		t.Fatalf("not an OPML document:\n%s", body)
	}
	// OPML is the documented exception: it is the operator's subscription list,
	// so the source URLs are necessarily present.
	if !strings.Contains(body, "radioitalia.it") {
		t.Errorf("OPML does not carry the source URLs, so it would subscribe to nothing:\n%s", body)
	}

	// parse it back with the platform's own reader: the export and the import
	// must agree on the shape
	var doc opmlDoc
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("exported OPML does not parse with the platform's reader: %v\n%s", err, body)
	}
	if doc.Head.Title == "" || doc.Version != "2.0" {
		t.Errorf("OPML head = %+v", doc.Head)
	}

	// every configured source appears, grouped under its collection
	sources, err := app.store.Sources()
	if err != nil {
		t.Fatal(err)
	}
	grouped := map[string]map[string]bool{} // collection -> set of xmlUrls
	for _, folder := range doc.Body.Outlines {
		set := map[string]bool{}
		for _, leaf := range folder.Outlines {
			set[feedIdentity(leaf.XMLURL)] = true
		}
		grouped[folder.Title] = set
	}
	if len(grouped) == 0 {
		t.Fatalf("the export has no collection folders:\n%s", body)
	}
	for _, s := range sources {
		c := collectionOrDefault(s.Collection)
		set, ok := grouped[c]
		if !ok {
			t.Errorf("collection %q is not a folder in the OPML export", c)
			continue
		}
		if !set[feedIdentity(s.URL)] {
			t.Errorf("source %q is missing from the OPML export under %q", s.Name, c)
		}
		if !strings.Contains(body, xmlAttr(s.Name)) {
			t.Errorf("source name %q is missing from the OPML export", s.Name)
		}
	}
}

// xmlAttr escapes a string the way encoding/xml does inside an attribute, so a
// test can look for it in the marshalled document.
func xmlAttr(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func TestOPMLImportAddsSourcesOnceAndKeepsGrouping(t *testing.T) {
	srv, app := newTestPlatform(t)
	resetSources(t, app) // start from nothing, so the counts are unambiguous

	code, body := call(t, srv, http.MethodPost, "/stenella/api/opml", opmlImportFixture)
	if code != http.StatusCreated {
		t.Fatalf("opml import: %d %s", code, body)
	}
	res := mustJSON(t, body)
	if res["added"].(float64) != 3 {
		t.Errorf("added = %v, want the 3 outlines that carry an xmlUrl", res["added"])
	}
	if res["skipped"].(float64) != 2 {
		t.Errorf("skipped = %v, want the empty folder and the empty xmlUrl", res["skipped"])
	}

	sources, err := app.store.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("stored %d sources, want 3", len(sources))
	}
	byName := map[string]*Source{}
	for _, s := range sources {
		byName[s.Name] = s
	}
	// the folder becomes the collection; a flat outline lands in "all"
	if cse := byName["CSE advisories"]; cse == nil || cse.Collection != "canada" {
		t.Errorf("grouped source = %+v, want collection canada", cse)
	}
	if lone := byName["Stand-alone wire"]; lone == nil || lone.Collection != "all" {
		t.Errorf("flat source = %+v, want collection all", lone)
	}
	for _, s := range sources {
		if !s.Enabled || s.Kind != sourceKindFeed || s.ACL == "" {
			t.Errorf("imported source is not ready to collect: %+v", s)
		}
	}

	// importing the same document again adds nothing
	code, body = call(t, srv, http.MethodPost, "/stenella/api/opml", opmlImportFixture)
	if code != http.StatusCreated {
		t.Fatalf("second import: %d %s", code, body)
	}
	res = mustJSON(t, body)
	if res["added"].(float64) != 0 || res["skipped"].(float64) != 5 {
		t.Errorf("second import = added %v skipped %v, want 0 and 5", res["added"], res["skipped"])
	}
	if again, _ := app.store.Sources(); len(again) != 3 {
		t.Errorf("second import changed the source count: %d", len(again))
	}

	// a tracking URL that canonicalises to a known source is a duplicate too
	dup := `<opml version="2.0"><body>
	  <outline type="rss" text="CSE advisories (tracked)" xmlUrl="https://import.example/cse.xml?utm_source=x#top"/>
	</body></opml>`
	if code, body := call(t, srv, http.MethodPost, "/stenella/api/opml", dup); code != http.StatusCreated ||
		mustJSON(t, body)["added"].(float64) != 0 {
		t.Errorf("tracking URL was not recognised as a duplicate: %d %s", code, body)
	}

	// malformed OPML is refused with a readable message
	code, body = call(t, srv, http.MethodPost, "/stenella/api/opml", "this is not xml")
	if code != http.StatusBadRequest || !strings.Contains(body, "error") {
		t.Errorf("malformed OPML: %d %s", code, body)
	}
	if containsLink(body) {
		t.Errorf("OPML error body carries a link: %s", body)
	}
}

// An export followed by an import must reproduce the configuration: this is the
// operator's backup and migration path.
func TestOPMLExportImportsBackToTheSameConfiguration(t *testing.T) {
	srv, app := newTestPlatform(t)

	sources, err := app.store.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) == 0 {
		t.Fatal("the seeded platform has no sources to export")
	}

	code, doc := call(t, srv, http.MethodGet, "/stenella/api/opml", "")
	if code != http.StatusOK {
		t.Fatalf("export: %d %s", code, doc)
	}

	// move the configuration out of the way, then import the document back
	resetSources(t, app)
	if code, body := call(t, srv, http.MethodPost, "/stenella/api/opml", doc); code != http.StatusCreated {
		t.Fatalf("re-import: %d %s", code, body)
	}
	restored, err := app.store.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != len(sources) {
		t.Fatalf("restored %d sources, want %d", len(restored), len(sources))
	}
	want := map[string]string{}
	for _, s := range sources {
		want[canonicalLink(s.URL)] = collectionOrDefault(s.Collection)
	}
	for _, s := range restored {
		got, ok := want[canonicalLink(s.URL)]
		if !ok {
			t.Errorf("restored a source that was not exported: %s", s.URL)
			continue
		}
		if collectionOrDefault(s.Collection) != got {
			t.Errorf("collection not restored for %s: %q, want %q", s.URL, s.Collection, got)
		}
	}
}

// ---------------------------------------------------------------------------
// STX07 — shares
// ---------------------------------------------------------------------------

func TestSharesAreVerifiableRevocableCapabilityRecords(t *testing.T) {
	srv, app := newTestPlatform(t)
	putItem(t, app.store, "item-share", "Something to share", "CBC", "news", time.Now().UTC(), []string{"cyber"})

	// --- issue -------------------------------------------------------------
	code, body := call(t, srv, http.MethodPost, "/stenella/api/share",
		`{"kind":"item","id":"item-share","ttl":"1h"}`)
	if code != http.StatusCreated {
		t.Fatalf("issue share: %d %s", code, body)
	}
	share := mustJSON(t, body)
	raw, _ := share["token"].(string)
	if raw == "" {
		t.Fatalf("no token issued: %s", body)
	}
	if share["issuer"] == "" || share["subject"] != "stenella-share" {
		t.Errorf("share identity = %s", body)
	}
	if !strings.Contains(share["note"].(string), "no access control") {
		t.Errorf("share does not state what it is: %v", share["note"])
	}

	// the token verifies with atp's own verifier, and carries the target
	code, body = call(t, srv, http.MethodPost, "/api/token/verify", `{"token":"`+raw+`"}`)
	if code != http.StatusOK {
		t.Fatalf("verify: %d %s", code, body)
	}
	verified := mustJSON(t, body)
	if verified["valid"] != true {
		t.Fatalf("token did not verify: %s", body)
	}
	claims := verified["claims"].(map[string]any)
	if claims["sub"] != "stenella-share" {
		t.Errorf("claims subject = %v", claims["sub"])
	}
	meta, _ := claims["meta"].(map[string]any)
	if meta["kind"] != "item" || meta["target"] != "item-share" {
		t.Errorf("claims meta = %v, want the shared target", claims["meta"])
	}
	if scopes, _ := claims["scp"].([]any); len(scopes) == 0 || scopes[0] != "stenella" {
		t.Errorf("claims scopes = %v", claims["scp"])
	}
	// a 1h ttl was honoured
	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat < 3500 || exp-iat > 3700 {
		t.Errorf("token lifetime = %v seconds, want ~3600", exp-iat)
	}
	jti, _ := claims["jti"].(string)
	if jti == "" {
		t.Fatal("token has no jti, so it cannot be revoked")
	}

	// --- revoke ------------------------------------------------------------
	revoke := fmt.Sprintf(`{"jti":%q,"expires_at":%d}`, jti, int64(claims["exp"].(float64)))
	if code, body := call(t, srv, http.MethodPost, "/api/revoke", revoke); code != http.StatusOK {
		t.Fatalf("revoke: %d %s", code, body)
	}
	if code, body := call(t, srv, http.MethodPost, "/api/token/verify", `{"token":"`+raw+`"}`); code != http.StatusUnauthorized {
		t.Errorf("a revoked token still verified: %d %s", code, body)
	}

	// --- refusals ----------------------------------------------------------
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/share", `{"kind":"item"}`); code != http.StatusBadRequest {
		t.Errorf("share without an id accepted")
	}
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/share", `{"kind":"secret","id":"x"}`); code != http.StatusBadRequest {
		t.Errorf("unknown share kind accepted")
	}
	// a share can name a collection too
	if code, body := call(t, srv, http.MethodPost, "/stenella/api/share",
		`{"kind":"collection","id":"news"}`); code != http.StatusCreated {
		t.Errorf("collection share refused: %d %s", code, body)
	}
	// and the response never carries a link
	if code, body := call(t, srv, http.MethodPost, "/stenella/api/share",
		`{"kind":"item","id":"item-share"}`); code != http.StatusCreated || containsLink(body) {
		t.Errorf("share response: %d %s", code, body)
	}
}
