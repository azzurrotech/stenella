package feed

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"azzurrotech/stenella/crypt"
)

// newMemVault is an in-memory stand-in for ATP's encrypted secret store, so the
// engine's cipher path is exercised without a whole atp stack behind it.
type newVault struct{ m map[string]map[string]string }

func newMemVault(t *testing.T) *newVault {
	t.Helper()
	return &newVault{m: map[string]map[string]string{}}
}

func (v *newVault) GetSecret(client, name string) (string, error) {
	val, ok := v.m[client][name]
	if !ok {
		return "", errors.New("no such secret")
	}
	return val, nil
}

func (v *newVault) SetSecret(client, name, value, _ string) error {
	if v.m[client] == nil {
		v.m[client] = map[string]string{}
	}
	v.m[client][name] = value
	return nil
}

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

// MetadataSearchable tells the UI whether a server-side query can answer it or
// whether the browser has to filter decrypted bodies. One or two words are
// almost always a category or source name; a multi-word query of ordinary word
// length is prose.
func TestMetadataSearchable(t *testing.T) {
	metadata := []string{"", "tech", "science", "fixture", "tag:fixture,1"}
	for _, q := range metadata {
		if !MetadataSearchable(q) {
			t.Errorf("MetadataSearchable(%q) = false, want true", q)
		}
	}
	prose := []string{"plain summary", "quantum entanglement", "the next big thing in storage"}
	for _, q := range prose {
		if MetadataSearchable(q) {
			t.Errorf("MetadataSearchable(%q) = true, want false", q)
		}
	}
}

// A cache written through a cipher must hold no plaintext, and must read back
// intact. This is the at-rest guarantee the whole package rests on.
func TestCacheIsEncryptedAtRest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()

	root := t.TempDir()
	ks := crypt.New(newMemVault(t), strings.Repeat("m", 40))
	e, err := New(Options{Root: root, Cipher: ks})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	src := rssFixtureSource(t, e, "acme", srv.URL+"/feed.rss")
	if _, err := e.Fetch("acme", src, nil); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	// Every body word the fixture contains must be absent from the cache file.
	body, err := os.ReadFile(filepath.Join(root, "cache", safe(src.ID)+".json"))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	for _, leak := range []string{"First story", "Second story", "Full content here", "Plain summary", "summary with markup"} {
		if bytes.Contains(body, []byte(leak)) {
			t.Errorf("cache file leaked body text %q:\n%s", leak, body)
		}
	}
	// Envelope metadata is in the clear so retention, ACL gating and indexing work
	// without opening anything.
	for _, want := range []string{"tag:fixture,1", "url-2", "tech", "science"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("cache file is missing envelope metadata %q", want)
		}
	}

	// Reading still works: a fresh engine over the same root opens the bodies.
	e2, err := New(Options{Root: root, Cipher: ks})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	page := e2.Combined("acme", Query{Page: 1, PageSize: 100})
	got := itemTitles(page.Items)
	want := []string{"Second story", "First story"}
	if len(got) != len(want) {
		t.Fatalf("reopened titles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reopened titles = %v, want %v", got, want)
		}
	}
}

// ContentBytes survives the round trip, so the UI can size a body it cannot read.
func TestContentBytesRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()
	root := t.TempDir()
	ks := crypt.New(newMemVault(t), strings.Repeat("m", 40))
	e, err := New(Options{Root: root, Cipher: ks})
	if err != nil {
		t.Fatal(err)
	}
	src := rssFixtureSource(t, e, "acme", srv.URL+"/feed.rss")
	page, err := e.Fetch("acme", src, nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for i := range page.Items {
		it := page.Items[i]
		if it.ContentBytes != len(it.Content) {
			t.Errorf("item %q: ContentBytes = %d, want %d", it.ID, it.ContentBytes, len(it.Content))
		}
	}
}

// Retention helpers: Expired lists only items past the cutoff and Prune drops
// exactly the ids it is given, from memory and from disk.
func TestExpiredAndPrune(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()
	root := t.TempDir()
	e, err := New(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	src := rssFixtureSource(t, e, "acme", srv.URL+"/feed.rss")
	if _, err := e.Fetch("acme", src, nil); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	// The fixture is dated 23/24 Sep 2026, so a cutoff in the middle of it
	// separates the two items.
	mid := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	expired := e.Expired("acme", src.ID, mid)
	if len(expired) != 1 {
		t.Fatalf("Expired = %v, want exactly the 23 Sep item", expired)
	}
	first := e.ItemByID("acme", expired[0])
	if first == nil || first.Title != "First story" {
		t.Fatalf("expired id %v is not First story (%v)", expired, first)
	}
	// A cutoff before both items expires both; after both, neither.
	if got := e.Expired("acme", src.ID, time.Now().UTC().Add(time.Hour)); len(got) != 2 {
		t.Fatalf("Expired with a future cutoff = %v, want 2", got)
	}
	if got := e.Expired("acme", src.ID, mid.Add(-72*time.Hour)); len(got) != 0 {
		t.Fatalf("Expired with an early cutoff = %v, want none", got)
	}
	e.Prune("acme", src.ID, expired)
	page := e.Combined("acme", Query{Page: 1, PageSize: 100})
	if page.Total != 1 || page.Items[0].Title != "Second story" {
		t.Fatalf("after prune = %+v, want only Second story", itemTitles(page.Items))
	}
	// Pruning an unknown id is a no-op, not an error.
	e.Prune("acme", src.ID, []string{"rec_nope"})
	if got := e.Combined("acme", Query{Page: 1, PageSize: 100}); got.Total != 1 {
		t.Fatalf("pruning an unknown id changed the feed: %d", got.Total)
	}
	// And it is gone from disk too, not just memory.
	e2, err := New(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	got := e2.Combined("acme", Query{Page: 1, PageSize: 100})
	if got.Total != 1 || got.Items[0].Title != "Second story" {
		t.Fatalf("reopened = %+v, want only Second story", itemTitles(got.Items))
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

	// Search matches envelope metadata only. "Plain summary." lives in an item
	// body, and bodies are encrypted at rest — a server query deliberately
	// cannot see it (plan §4.4). Full-text search runs in the browser over
	// decrypted bodies; see TestMetadataSearchable for the flag the UI reads.
	body := e.Combined("acme", Query{Page: 1, PageSize: 100, Q: "summary"})
	if body.Total != 0 {
		t.Errorf("server search matched an encrypted body: %+v", itemTitles(body.Items))
	}
	// The same word as a *category* is in the clear and does match.
	byCat := e.Combined("acme", Query{Page: 1, PageSize: 100, Q: "science"})
	if byCat.Total != 1 || byCat.Items[0].Title != "Second story" {
		t.Errorf("search 'science' → %+v (want Second story)", itemTitles(byCat.Items))
	}
	// Source name is envelope metadata too.
	sq := e.Combined("acme", Query{Page: 1, PageSize: 100, Q: "atom"})
	if sq.Total != 1 || sq.Items[0].Title != "Atom entry" {
		t.Errorf("search 'atom' → %+v", itemTitles(sq.Items))
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

// ---- ACL classification --------------------------------------------------------

func TestNormalizeACLClass(t *testing.T) {
	cases := map[string]string{
		"":          ACLPublic, // unset defaults to the only open class
		"public":    ACLPublic,
		"private":   ACLPrivate,
		"protected": ACLProtected,
		"Private":   ACLPublic, // case-sensitive: an odd value is not trusted
		"secret":    ACLPublic,
		" public ":  ACLPublic,
	}
	for in, want := range cases {
		if got := NormalizeACLClass(in); got != want {
			t.Errorf("NormalizeACLClass(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []string{ACLPublic, ACLPrivate, ACLProtected} {
		if !ValidACLClass(c) {
			t.Errorf("ValidACLClass(%q) = false", c)
		}
	}
	if ValidACLClass("") || ValidACLClass("admin") {
		t.Error("ValidACLClass accepted a value it should reject")
	}
}

func TestAddSourceClassified(t *testing.T) {
	root := t.TempDir()
	e, err := New(Options{Root: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	pub, err := e.AddSourceClassified("acme", "P", "https://p.test/f", KindRSS, 5, "", 0, ACLPrivate)
	if err != nil {
		t.Fatalf("AddSourceClassified: %v", err)
	}
	if pub.AclClass != ACLPrivate {
		t.Errorf("class = %q, want private", pub.AclClass)
	}

	// An unknown class must never be stored verbatim; it normalises to public.
	odd, err := e.AddSourceClassified("acme", "O", "https://o.test/f", KindRSS, 5, "", 0, "bogus")
	if err != nil {
		t.Fatalf("AddSourceClassified odd: %v", err)
	}
	if odd.AclClass != ACLPublic {
		t.Errorf("unknown class stored as %q, want public", odd.AclClass)
	}

	// Classification survives a reload from disk.
	e2, err := New(Options{Root: root})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := e2.GetSource("acme", pub.ID); got == nil || got.AclClass != ACLPrivate {
		t.Errorf("class lost on reload: %+v", got)
	}
}

// A source saved before acl_class existed must read back as public, not as an
// empty class that every downstream comparison would silently mismatch.
func TestLoadStateNormalizesMissingACLClass(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "feeds"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `[{"id":"legacy1","name":"Legacy","url":"https://l.test/f","kind":"rss","enabled":true}]`
	if err := os.WriteFile(filepath.Join(root, "feeds", "acme.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	e, err := New(Options{Root: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	src := e.GetSource("acme", "legacy1")
	if src == nil {
		t.Fatal("legacy source not loaded")
	}
	if src.AclClass != ACLPublic {
		t.Fatalf("legacy class = %q, want public", src.AclClass)
	}
}

func TestUpdateSourceACL(t *testing.T) {
	e, err := New(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	src, err := e.AddSource("acme", "S", "https://s.test/f", KindRSS, 5, "", 0)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	if err := e.UpdateSource("acme", src.ID, map[string]any{"acl_class": ACLProtected}); err != nil {
		t.Fatalf("update to protected: %v", err)
	}
	if got := e.GetSource("acme", src.ID); got.AclClass != ACLProtected {
		t.Fatalf("class = %q, want protected", got.AclClass)
	}
	// An invalid class is an error and must not mutate the stored value.
	if err := e.UpdateSource("acme", src.ID, map[string]any{"acl_class": "root"}); err == nil {
		t.Fatal("invalid class accepted")
	}
	if got := e.GetSource("acme", src.ID); got.AclClass != ACLProtected {
		t.Fatalf("class mutated by rejected update: %q", got.AclClass)
	}
	// A patch without acl_class leaves it alone.
	if err := e.UpdateSource("acme", src.ID, map[string]any{"name": "Renamed"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := e.GetSource("acme", src.ID); got.AclClass != ACLProtected || got.Name != "Renamed" {
		t.Fatalf("after rename: %+v", got)
	}
}

// Items carry their source's class so a reader can group and gate them, and
// the class is read off the item at query time rather than off the source.
func TestItemsCarrySourceACL(t *testing.T) {
	body := `<?xml version="1.0"?><rss version="2.0"><channel><title>S</title>` +
		`<item><title>hidden</title><link>https://h.test/1</link><guid>h1</guid>` +
		`<pubDate>Thu, 24 Sep 2026 09:00:00 GMT</pubDate></item></channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	e, err := New(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	src, err := e.AddSourceClassified("acme", "Hidden", srv.URL+"/f", KindRSS, 5, "", 0, ACLPrivate)
	if err != nil {
		t.Fatalf("AddSourceClassified: %v", err)
	}
	ff, err := e.Fetch("acme", src, nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ff.Items) != 1 || ff.Items[0].Source == nil {
		t.Fatalf("item has no source projection: %+v", ff.Items)
	}
	if ff.Items[0].Source.AclClass != ACLPrivate || ff.Items[0].Source.Name != "Hidden" {
		t.Fatalf("projection = %+v", ff.Items[0].Source)
	}

	// No class filter → everything (authenticated view).
	if got := e.Combined("acme", Query{Page: 1, PageSize: 10}); got.Total != 1 {
		t.Fatalf("unfiltered total = %d, want 1", got.Total)
	}
	// Public filter → nothing.
	if got := e.Combined("acme", Query{Page: 1, PageSize: 10, AclClass: ACLPublic}); got.Total != 0 {
		t.Fatalf("public total = %d, want 0", got.Total)
	}
	if got := e.Combined("acme", Query{Page: 1, PageSize: 10, AclClass: ACLPrivate}); got.Total != 1 {
		t.Fatalf("private total = %d, want 1", got.Total)
	}
}

func TestAllowedACLDefaultsToPublicOnly(t *testing.T) {
	private := Item{Source: &ItemSource{AclClass: ACLPrivate}}
	public := Item{Source: &ItemSource{AclClass: ACLPublic}}
	legacy := Item{} // no source at all

	if AllowedACL(private) {
		t.Error("private item allowed with no clearance")
	}
	if !AllowedACL(public) {
		t.Error("public item refused with no clearance")
	}
	if !AllowedACL(legacy) {
		t.Error("legacy item with no source should default to public")
	}
	if !AllowedACL(private, ACLPrivate, ACLPublic) {
		t.Error("private item refused for a caller cleared for private")
	}
	if AllowedACL(public, ACLPrivate) {
		t.Error("public item served to a private-only caller")
	}
}
