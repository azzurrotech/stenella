package feed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Fixture Channel</title>
    <link>https://fixture.test/</link>
    <item>
      <title>First story</title>
      <link>https://fixture.test/1</link>
      <guid isPermaLink="false">tag:fixture,1</guid>
      <pubDate>Wed, 23 Sep 2026 12:30:00 -0400</pubDate>
      <description>&lt;p&gt;A summary &lt;b&gt;with markup&lt;/b&gt;.&lt;/p&gt;</description>
      <category>tech</category>
      <category>tech</category>
      <author>alice@fixture.test (Alice)</author>
      <encoded><![CDATA[<p>Full content here.</p>]]></encoded>
    </item>
    <item>
      <title>Second story</title>
      <link>https://fixture.test/2</link>
      <guid>url-2</guid>
      <pubDate>2026-09-24T09:00:00Z</pubDate>
      <description>Plain summary.</description>
      <category>science</category>
    </item>
  </channel>
</rss>`

const atomFixture = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Channel</title>
  <id>tag:atom.test,2026:feed</id>
  <updated>2026-09-24T10:00:00Z</updated>
  <entry>
    <title>Atom entry</title>
    <id>tag:atom.test,2026:1</id>
    <link rel="alternate" href="https://atom.test/1"/>
    <link rel="self" href="https://atom.test/feed"/>
    <summary type="html">&lt;p&gt;Atom summary.&lt;/p&gt;</summary>
    <author><name>Bob</name><email>bob@atom.test</email></author>
    <category term="news"/>
    <published>2026-09-24T08:00:00Z</published>
    <updated>2026-09-24T08:30:00Z</updated>
  </entry>
</feed>`

const rdfFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/">
  <channel rdf:about="https://rdf.test/">
    <title>RDF Channel</title>
  </channel>
  <item rdf:about="https://rdf.test/1">
    <title>RDF story</title>
    <link>https://rdf.test/1</link>
    <description>An RDF item.</description>
  </item>
</rdf:RDF>`

const jsonFeedFixture = `{
  "title": "JSON Channel",
  "items": [
    {"title": "Json one", "url": "https://json.test/1", "id": "j1", "date_published": "2026-09-24T07:00:00Z", "summary": "s1", "tags": ["a", "b"]},
    {"title": "Json two", "url": "https://json.test/2", "id": "j2", "date_published": "2026-09-24T06:00:00Z", "summary": "s2"}
  ]
}`

const jsonArrayFixture = `[
  {"title": "Array one", "url": "https://arr.test/1", "published": "2026-09-24T05:00:00Z"},
  {"title": "Array two", "url": "https://arr.test/2"}
]`

const opmlFixture = `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head><title>My feeds</title></head>
  <body>
    <outline text="Dev" title="Dev">
      <outline type="rss" text="Go Blog" title="Go Blog" xmlUrl="https://go.dev/feed.atom"/>
      <outline type="rss" text="News" title="News" xmlUrl="https://news.test/rss"/>
    </outline>
    <outline type="rss" text="Data" title="Data" xmlUrl="https://data.test/feed.json"/>
  </body>
</opml>`

func TestParseRSS(t *testing.T) {
	items, err := ParseRSS([]byte(rssFixture))
	if err != nil {
		t.Fatalf("ParseRSS: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	first := items[0]
	if first.Title != "First story" {
		t.Errorf("title = %q", first.Title)
	}
	if first.GUID != "tag:fixture,1" {
		t.Errorf("guid = %q", first.GUID)
	}
	if first.Author != "alice@fixture.test (Alice)" {
		t.Errorf("author = %q", first.Author)
	}
	if first.SourceName != "Fixture Channel" {
		t.Errorf("source = %q", first.SourceName)
	}
	wantPub := time.Date(2026, 9, 23, 16, 30, 0, 0, time.UTC)
	if !first.Published.Equal(wantPub) {
		t.Errorf("published = %v, want %v", first.Published, wantPub)
	}
	if len(first.Categories) != 1 {
		t.Errorf("categories = %v, want deduped single", first.Categories)
	}
	if !strings.Contains(first.Content, "Full content") {
		t.Errorf("content = %q", first.Content)
	}
	// items by <guid> permissiveness: item without guid falls back to link.
	if items[1].GUID != "url-2" {
		t.Errorf("second guid = %q", items[1].GUID)
	}
}

func TestParseRSSRDF(t *testing.T) {
	items, err := ParseRSS([]byte(rdfFixture))
	if err != nil {
		t.Fatalf("ParseRSS(rdf): %v", err)
	}
	if len(items) != 1 || items[0].Title != "RDF story" {
		t.Fatalf("rdf items = %+v", items)
	}
	if items[0].GUID != "" && items[0].GUID != "https://rdf.test/1" {
		t.Errorf("guid = %q", items[0].GUID)
	}
}

func TestParseAtom(t *testing.T) {
	items, err := ParseAtom([]byte(atomFixture))
	if err != nil {
		t.Fatalf("ParseAtom: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	it := items[0]
	if it.Title != "Atom entry" || it.Link != "https://atom.test/1" {
		t.Errorf("title/link = %q / %q", it.Title, it.Link)
	}
	if it.Summary != "Atom summary." {
		t.Errorf("summary = %q", it.Summary)
	}
	if it.Author != "Bob" {
		t.Errorf("author = %q", it.Author)
	}
	if len(it.Categories) != 1 || it.Categories[0] != "news" {
		t.Errorf("categories = %v", it.Categories)
	}
	if it.GUID != "tag:atom.test,2026:1" {
		t.Errorf("guid = %q", it.GUID)
	}
	want := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	if !it.Published.Equal(want) {
		t.Errorf("published = %v", it.Published)
	}
}

func TestParseJSONFeed(t *testing.T) {
	items, err := ParseJSONFeed([]byte(jsonFeedFixture))
	if err != nil {
		t.Fatalf("ParseJSONFeed(feed): %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	if items[0].Title != "Json one" || items[0].Link != "https://json.test/1" || items[0].GUID != "j1" {
		t.Errorf("first = %+v", items[0])
	}
	if len(items[0].Categories) != 2 {
		t.Errorf("categories = %v", items[0].Categories)
	}

	arr, err := ParseJSONFeed([]byte(jsonArrayFixture))
	if err != nil {
		t.Fatalf("ParseJSONFeed(array): %v", err)
	}
	if len(arr) != 2 || arr[0].Title != "Array one" || arr[1].GUID != "https://arr.test/2" {
		t.Errorf("array items = %+v", arr)
	}
	if arr[0].Published.IsZero() {
		t.Errorf("array published missing")
	}
}

func TestParseOPML(t *testing.T) {
	srcs, err := ParseOPML([]byte(opmlFixture))
	if err != nil {
		t.Fatalf("ParseOPML: %v", err)
	}
	if len(srcs) != 3 {
		t.Fatalf("sources = %d", len(srcs))
	}
	byURL := map[string]Source{}
	for _, s := range srcs {
		byURL[s.URL] = s
	}
	if byURL["https://go.dev/feed.atom"].Kind != KindAtom {
		t.Errorf("atom kind = %q", byURL["https://go.dev/feed.atom"].Kind)
	}
	if byURL["https://news.test/rss"].Kind != KindRSS {
		t.Errorf("rss kind = %q", byURL["https://news.test/rss"].Kind)
	}
	if byURL["https://data.test/feed.json"].Kind != KindJSON {
		t.Errorf("json kind = %q", byURL["https://data.test/feed.json"].Kind)
	}
	if byURL["https://go.dev/feed.atom"].Name != "Go Blog" {
		t.Errorf("name = %q", byURL["https://go.dev/feed.atom"].Name)
	}
}

func TestGuessKind(t *testing.T) {
	cases := map[string]string{
		"/feed.atom":       KindAtom,
		"/feed.xml.atom":   KindAtom,
		"/rss":             KindRSS,
		"/subs.opml":       KindOPML,
		"/feeds.json":      KindJSON,
		"/api?format=json": KindJSON,
	}
	for in, want := range cases {
		if got := GuessKind(in); got != want {
			t.Errorf("GuessKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestItemIDStable(t *testing.T) {
	a := itemID("acme", "src1", "guid", "https://x.test/1")
	b := itemID("acme", "src1", "guid", "https://x.test/1")
	if a == "" || a != b {
		t.Fatalf("itemID not stable: %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("itemID length = %d, want 16", len(a))
	}
	c := itemID("acme", "src2", "guid", "https://x.test/1")
	if c == a {
		t.Errorf("itemID must differ across sources")
	}
}

func TestParseAnyTime(t *testing.T) {
	cases := []string{
		"2026-09-24T09:00:00Z",
		"Wed, 23 Sep 2026 12:30:00 -0400",
		"Wed, 3 Sep 2026 12:30:00 -0400", // single-digit day tolerance
		"2026-09-24",                     // date only
		"2026-09-24 09:00:00",            // space separated
	}
	for _, c := range cases {
		if _, err := parseAnyTime(c); err != nil {
			t.Errorf("parseAnyTime(%q): %v", c, err)
		}
	}
	if _, err := parseAnyTime("not a date at all"); err == nil {
		t.Errorf("junk date should fail")
	}
}

func TestEngineCombined(t *testing.T) {
	rssSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer rssSrv.Close()
	atomSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(atomFixture))
	}))
	defer atomSrv.Close()

	root := t.TempDir()
	e, err := New(Options{Root: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rss := rssFixtureSource(t, e, "acme", rssSrv.URL+"/feed.rss")
	atom := atomFixtureSource(t, e, "acme", atomSrv.URL+"/feed.atom")

	if _, err := e.Fetch("acme", rss, nil); err != nil {
		t.Fatalf("fetch rss: %v", err)
	}
	if _, err := e.Fetch("acme", atom, nil); err != nil {
		t.Fatalf("fetch atom: %v", err)
	}

	all := e.Combined("acme", Query{Page: 1, PageSize: 100})
	if all.Total != 3 {
		t.Fatalf("combined total = %d, want 3", all.Total)
	}
	// Newest-first across sources: Atom entry (08:00) is newer than the RSS
	// second story (09:00 on the 24th? no) — rss second is 2026-09-24T09:00Z,
	// which is newer than Atom's 08:00Z, and rss first is 16:30Z on the 23rd.
	got := itemTitles(all.Items)
	wantOrder := []string{"Second story", "Atom entry", "First story"}
	for i, w := range wantOrder {
		if got[i] != w {
			t.Fatalf("order[%d] = %q, want %q (got %v)", i, got[i], w, got)
		}
	}

	// Pagination: page size 2 → two pages, has_more flips.
	p1 := e.Combined("acme", Query{Page: 1, PageSize: 2})
	if len(p1.Items) != 2 || !p1.HasMore {
		t.Fatalf("page1 = %d items, hasMore=%v", len(p1.Items), p1.HasMore)
	}
	p2 := e.Combined("acme", Query{Page: 2, PageSize: 2})
	if len(p2.Items) != 1 || p2.HasMore {
		t.Fatalf("page2 = %d items, hasMore=%v", len(p2.Items), p2.HasMore)
	}

	// Search filter.
	sq := e.Combined("acme", Query{Page: 1, PageSize: 100, Q: "plain"})
	if sq.Total != 1 || sq.Items[0].Title != "Second story" {
		t.Fatalf("search 'plain' → %+v (want Second story)", itemTitles(sq.Items))
	}
	sq = e.Combined("acme", Query{Page: 1, PageSize: 100, Q: "atom"})
	if sq.Total != 1 || sq.Items[0].Title != "Atom entry" {
		t.Fatalf("search 'atom' → %+v", itemTitles(sq.Items))
	}

	// Category filter.
	cq := e.Combined("acme", Query{Page: 1, PageSize: 100, Category: "news"})
	if cq.Total != 1 || cq.Items[0].Title != "Atom entry" {
		t.Fatalf("category filter → %+v", itemTitles(cq.Items))
	}

	// Source filter.
	onlyRSS := e.Combined("acme", Query{Page: 1, PageSize: 100, Source: rss.ID})
	if onlyRSS.Total != 2 {
		t.Fatalf("source filter total = %d", onlyRSS.Total)
	}

	// Dedup: two identical <item> entries in one feed collapse to one row.
	dup := `<?xml version="1.0"?><rss version="2.0"><channel><title>D</title>` +
		`<item><title>dup</title><link>https://d.test/1</link><guid>g1</guid>` +
		`<pubDate>Thu, 24 Sep 2026 09:00:00 GMT</pubDate></item>` +
		`<item><title>dup</title><link>https://d.test/1</link><guid>g1</guid>` +
		`<pubDate>Thu, 24 Sep 2026 09:00:00 GMT</pubDate></item>` +
		`</channel></rss>`
	dupSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(dup))
	}))
	defer dupSrv.Close()
	dupSrc, err := e.AddSource("acme", "Dup", dupSrv.URL+"/dup", "", 0, "", 0)
	if err != nil {
		t.Fatalf("AddSource dup: %v", err)
	}
	if _, err := e.Fetch("acme", dupSrc, nil); err != nil {
		t.Fatalf("fetch dup: %v", err)
	}
	dd := e.Combined("acme", Query{Page: 1, PageSize: 100, Source: dupSrc.ID})
	if dd.Total != 1 {
		t.Fatalf("dedup: total = %d, want 1", dd.Total)
	}

	// ItemByID.
	if it := e.ItemByID("acme", all.Items[0].ID); it == nil || it.Title != all.Items[0].Title {
		t.Errorf("ItemByID failed for %q", all.Items[0].ID)
	}

	// Retention pruning: an old item is dropped from the cache.
	oldSrc := oldItemSource(t, e, "acme")
	if _, err := e.Fetch("acme", oldSrc, nil); err != nil {
		t.Fatalf("fetch old: %v", err)
	}
	oldPage := e.Combined("acme", Query{Page: 1, PageSize: 100, Source: oldSrc.ID})
	if oldPage.Total != 0 {
		t.Fatalf("retention: old item survived, total = %d", oldPage.Total)
	}
}

func rssFixtureSource(t *testing.T, e *Engine, client, url string) *Source {
	t.Helper()
	src, err := e.AddSource(client, "Fixture RSS", url, KindRSS, 1, "", 0)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	return src
}

func atomFixtureSource(t *testing.T, e *Engine, client, url string) *Source {
	t.Helper()
	src, err := e.AddSource(client, "Fixture Atom", url, "", 1, "", 0)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	if src.Kind != KindAtom {
		t.Fatalf("kind = %q, want atom", src.Kind)
	}
	return src
}

// oldItemSource serves an item published over 30 days ago (default retention).
func oldItemSource(t *testing.T, e *Engine, client string) *Source {
	t.Helper()
	old := `<rss version="2.0"><channel><title>Old</title>` +
		`<item><title>ancient</title><link>https://old.test/1</link><guid>o1</guid>` +
		`<pubDate>Tue, 01 Jan 2019 00:00:00 GMT</pubDate></item></channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(old))
	}))
	t.Cleanup(srv.Close)
	src, err := e.AddSource(client, "Old", srv.URL+"/old.rss", KindRSS, 1, "", 0)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	return src
}

func itemTitles(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}
