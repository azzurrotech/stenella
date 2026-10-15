package main

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fixtures — deliberately nasty, in the shapes the real sources use
// ---------------------------------------------------------------------------

const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:content="http://purl.org/rss/1.0/modules/content/">
  <channel>
    <title>Fixture News</title>
    <link>https://fixture.example/</link>
    <description>fixture channel</description>
    <item>
      <title>Vendor &lt;b&gt;confirms&lt;/b&gt; breach &#8212; see https://evil.example/x?y=1</title>
      <link>https://fixture.example/a</link>
      <description>&lt;p&gt;Contact &lt;a href="https://fixture.example/p"&gt;us&lt;/a&gt; at desk@fixture.example or visit https://fixture.example/contact&lt;/p&gt;&lt;script&gt;steal()&lt;/script&gt;</description>
      <enclosure url="https://cdn.fixture.example/img.jpg" length="10" type="image/jpeg"/>
      <guid isPermaLink="true">https://fixture.example/a</guid>
      <comments>https://fixture.example/a#comments</comments>
      <dc:creator>jsmith@fixture.example (J. Smith)</dc:creator>
      <pubDate>Mon, 06 Oct 2025 10:00:00 GMT</pubDate>
    </item>
    <item>
      <title>Second headline</title>
      <description>Plain text with no links.</description>
      <pubDate>Sun, 05 Oct 2025 09:00:00 GMT</pubDate>
    </item>
    <item>
      <title>Shared headline</title>
      <description>Older copy of a story that also appears elsewhere.</description>
      <pubDate>Sat, 04 Oct 2025 08:00:00 GMT</pubDate>
    </item>
  </channel>
</rss>`

const atomFixture = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Fixture</title>
  <id>https://fixture.example/atom</id>
  <link href="https://fixture.example/atom.xml" rel="self"/>
  <entry>
    <title>Statement by @minister on Cyber Month</title>
    <id>https://www.canada.ca/en/communications-security/news/2026/10/x.html</id>
    <summary type="html">Cyber threats &lt;a href="https://phish.example"&gt;link&lt;/a&gt; reach us at a@b.ca</summary>
    <author><name>Some Person</name></author>
    <category term="statements"/>
    <updated>2026-10-01T10:12:19-04:00</updated>
    <link href="https://www.canada.ca/en/communications-security/news/2026/10/x.html"/>
  </entry>
  <entry>
    <title>Shared headline</title>
    <summary>Newer copy of the same story.</summary>
    <updated>2025-10-04T12:00:00Z</updated>
  </entry>
</feed>`

const rdfFixture = `<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/">
  <channel rdf:about="https://rdf.example/">
    <title>RDF Fixture</title>
    <link>https://rdf.example/</link>
    <description>rdf</description>
  </channel>
  <item rdf:about="https://rdf.example/1">
    <title>RDF headline</title>
    <link>https://rdf.example/1</link>
    <description>RDF body</description>
    <dc:date>2025-09-01T00:00:00Z</dc:date>
  </item>
</rdf:RDF>`

const htmlNotAFeed = `<!doctype html><html><head><title>404</title></head><body>not a feed</body></html>`

// ---------------------------------------------------------------------------
// fixture servers
// ---------------------------------------------------------------------------

type fixtureServer struct {
	*httptest.Server
	hits atomic.Int64
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFixture))
	})
	mux.HandleFunc("/atom.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(atomFixture))
	})
	mux.HandleFunc("/rdf.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rdfFixture))
	})
	mux.HandleFunc("/broken.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(htmlNotAFeed))
	})
	mux.HandleFunc("/fail.xml", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	fs.Server = httptest.NewServer(mux)
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fixtureServer) sources(names ...string) []rssSource {
	var out []rssSource
	for _, n := range names {
		out = append(out, rssSource{Name: n, URL: fs.URL + "/" + n + ".xml"})
	}
	return out
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected to find %q in:\n%s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(strings.ToLower(haystack), strings.ToLower(needle)) {
		t.Fatalf("expected NOT to find %q in:\n%s", needle, haystack)
	}
}

// ---------------------------------------------------------------------------
// STE07 — sanitising
// ---------------------------------------------------------------------------

func TestSanitizeTextStripsLinksAndPrivateElements(t *testing.T) {
	raw := `<p>Contact <a href="https://fixture.example/p">us</a> at ` +
		`desk@fixture.example or https://fixture.example/contact, or www.fixture.example, ` +
		`or fixtures.example — ping @analyst</p><script>steal()</script>`
	got := sanitizeText(raw, 0)

	for _, bad := range []string{"http", "www.", "fixture.example", "@", "<", ">", "href", "script", "steal"} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(bad)) {
			t.Errorf("sanitised text still contains %q: %q", bad, got)
		}
	}
	mustContain(t, got, "Contact")
	mustContain(t, got, "us")
	if containsLink(got) {
		t.Errorf("containsLink(%q) = true, want false", got)
	}
}

func TestSanitizeTextDecodesEntitiesAndTags(t *testing.T) {
	got := sanitizeText("JOVANOTTI, IL MESSAGGIO A UN ANNO DALL&#39;INCIDENTE", 0)
	if got != "JOVANOTTI, IL MESSAGGIO A UN ANNO DALL'INCIDENTE" {
		t.Fatalf("got %q", got)
	}
	got = sanitizeText("&lt;b&gt;bold&lt;/b&gt; plain", 0)
	if got != "bold plain" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeTextKeepsOrdinaryProse(t *testing.T) {
	for _, s := range []string{
		"Flood warnings issued for the Ottawa River",
		"Il presidente ha parlato a Roma",
		"Wholesale trade rose 3.5 per cent in August",
		"U.S. and Canada sign a deal",
	} {
		if got := sanitizeText(s, 0); got != s {
			t.Errorf("sanitizeText(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestSanitizeTextIsIdempotent(t *testing.T) {
	raw := `<p>See <a href="https://x.example">x.example</a> — mail a@b.ca @user</p>`
	once := sanitizeText(raw, 0)
	twice := sanitizeText(once, 0)
	if once != twice {
		t.Fatalf("not idempotent: %q -> %q", once, twice)
	}
}

func TestSanitizeTextTruncates(t *testing.T) {
	got := sanitizeText(strings.Repeat("a", 900), 480)
	if n := len([]rune(got)); n > 483 {
		t.Fatalf("expected truncation to ~480 runes, got %d", n)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected ellipsis, got %q", got[len(got)-10:])
	}
}

func TestSanitizeTextDropsIllegalXMLCharacters(t *testing.T) {
	got := sanitizeText("clean\x00\x01\x02 text\x0b here", 0)
	if strings.ContainsAny(got, "\x00\x01\x02\x0b") {
		t.Fatalf("control characters survived: %q", got)
	}
	mustContain(t, got, "clean")
}

func TestContainsLink(t *testing.T) {
	for _, s := range []string{
		"see https://example.org", "go to www.example.org",
		"mail a@b.ca", "read example.ca/page", "x <a href=\"y\">z</a>",
	} {
		if !containsLink(s) {
			t.Errorf("containsLink(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"plain headline", "3.5 per cent", "U.S. and Canada", "Ottawa River",
	} {
		if containsLink(s) {
			t.Errorf("containsLink(%q) = true, want false", s)
		}
	}
}

// ---------------------------------------------------------------------------
// STE06 — parsing
// ---------------------------------------------------------------------------

func TestParseFeedRSS(t *testing.T) {
	items, err := parseFeed([]byte(rssFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if items[0].When.IsZero() {
		t.Fatal("pubDate was not parsed")
	}
	if items[0].When.UTC().Format(time.RFC3339) != "2025-10-06T10:00:00Z" {
		t.Fatalf("wrong date: %s", items[0].When)
	}
}

func TestParseFeedAtom(t *testing.T) {
	items, err := parseFeed([]byte(atomFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].When.UTC().Format(time.RFC3339) != "2026-10-01T14:12:19Z" {
		t.Fatalf("wrong atom date: %s", items[0].When)
	}
}

func TestParseFeedRDF(t *testing.T) {
	items, err := parseFeed([]byte(rdfFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.Contains(items[0].Title, "RDF headline") {
		t.Fatalf("got %+v", items)
	}
	if items[0].When.UTC().Format(time.RFC3339) != "2025-09-01T00:00:00Z" {
		t.Fatalf("wrong rdf date: %s", items[0].When)
	}
}

// A 404 page is not an empty feed: it has to report an error so the source is
// marked broken rather than "quiet today".
func TestParseFeedRejectsHTMLPageWithoutItems(t *testing.T) {
	if _, err := parseFeed([]byte(htmlNotAFeed)); err == nil {
		t.Fatal("expected an error for an HTML page carrying no items")
	}
}

// ---------------------------------------------------------------------------
// STE06 — the aggregator
// ---------------------------------------------------------------------------

func TestAggregatorMergesSortsAndDedupes(t *testing.T) {
	fs := newFixtureServer(t)
	a := newAggregator(fs.sources("rss", "atom", "rdf"), fs.Client(), time.Minute)

	items := a.Articles(context.Background())
	if len(items) == 0 {
		t.Fatal("no items")
	}
	// newest first
	for i := 1; i < len(items); i++ {
		if items[i-1].Published.Before(items[i].Published) {
			t.Fatalf("items not sorted newest-first: %v then %v", items[i-1], items[i])
		}
	}
	// "Shared headline" appears in both rss and atom; only the newer survives
	// (the atom copy, 2025-10-04T12:00Z), so exactly one remains.
	var shared []Article
	for _, it := range items {
		if it.Title == "Shared headline" {
			shared = append(shared, it)
		}
	}
	if len(shared) != 1 {
		t.Fatalf("expected dedupe to leave 1 'Shared headline', got %d", len(shared))
	}
	if shared[0].Source != "atom" {
		t.Fatalf("dedupe kept the wrong copy: %+v", shared[0])
	}
	// sources are all represented
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Source] = true
	}
	for _, want := range []string{"rss", "atom", "rdf"} {
		if !seen[want] {
			t.Errorf("no items from source %q", want)
		}
	}
}

func TestAggregatorOutputIsLinkFreeAndPrivateFree(t *testing.T) {
	fs := newFixtureServer(t)
	a := newAggregator(fs.sources("rss", "atom", "rdf"), fs.Client(), time.Minute)

	for _, it := range a.Articles(context.Background()) {
		if containsLink(it.Title) {
			t.Errorf("title carries a link: %q", it.Title)
		}
		if containsLink(it.Summary) {
			t.Errorf("summary carries a link: %q", it.Summary)
		}
		for _, bad := range []string{"jsmith", "desk@", "Some Person", "@minister", "evil.example"} {
			if strings.Contains(it.Title, bad) || strings.Contains(it.Summary, bad) {
				t.Errorf("item still leaks %q: %+v", bad, it)
			}
		}
	}
}

func TestAggregatorToleratesPartialFailure(t *testing.T) {
	fs := newFixtureServer(t)
	a := newAggregator(fs.sources("rss", "fail", "broken"), fs.Client(), time.Minute)

	items := a.Articles(context.Background())
	if len(items) == 0 {
		t.Fatal("expected items from the one healthy source")
	}
	errs := a.Errors()
	if len(errs) != 2 {
		t.Fatalf("expected 2 recorded source errors, got %d: %v", len(errs), errs)
	}
	joined := strings.Join(errs, " ")
	mustContain(t, joined, "http 500")
	mustContain(t, joined, "unrecognised feed format")
}

func TestAggregatorServesStaleCacheWhenEverySourceFails(t *testing.T) {
	fs := newFixtureServer(t)
	a := newAggregator(fs.sources("rss"), fs.Client(), time.Nanosecond)

	first := a.Articles(context.Background())
	if len(first) == 0 {
		t.Fatal("no items on the first pass")
	}
	time.Sleep(2 * time.Millisecond) // let the tiny ttl lapse
	fs.Close()                       // now every source fails

	second := a.Articles(context.Background())
	if len(second) != len(first) {
		t.Fatalf("expected stale cache (%d items), got %d", len(first), len(second))
	}
}

func TestAggregatorCachesWithinTTL(t *testing.T) {
	fs := newFixtureServer(t)
	var hits atomic.Int64
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer counting.Close()

	a := newAggregator([]rssSource{{Name: "rss", URL: counting.URL}}, counting.Client(), time.Minute)
	for i := 0; i < 5; i++ {
		if items := a.Articles(context.Background()); len(items) == 0 {
			t.Fatal("no items")
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("expected 1 upstream fetch for 5 calls, got %d", n)
	}
	_ = fs
}

func TestAggregatorCapsItemsPerSource(t *testing.T) {
	fs := newFixtureServer(t)
	a := newAggregator(fs.sources("rss"), fs.Client(), time.Minute)
	a.perSource = 2
	if items := a.Articles(context.Background()); len(items) != 2 {
		t.Fatalf("expected the per-source cap of 2, got %d", len(items))
	}
}

func TestAggregatorSkipsUntitledItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss version="2.0"><channel>
		  <item><title>   </title><description>blank title</description></item>
		  <item><title>Real</title><description>ok</description></item>
		</channel></rss>`))
	}))
	defer srv.Close()
	a := newAggregator([]rssSource{{Name: "x", URL: srv.URL}}, srv.Client(), time.Minute)
	items := a.Articles(context.Background())
	if len(items) != 1 || items[0].Title != "Real" {
		t.Fatalf("got %+v", items)
	}
}

func TestAggregatorRetriesATransientFailure(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "flaky", http.StatusBadGateway) // first attempt fails…
			return
		}
		_, _ = w.Write([]byte(rssFixture)) // …the retry succeeds
	}))
	defer srv.Close()

	a := newAggregator([]rssSource{{Name: "flaky", URL: srv.URL}}, srv.Client(), time.Minute)
	items := a.Articles(context.Background())
	if len(items) == 0 {
		t.Fatalf("expected the retry to recover the source (calls=%d, errs=%v)", calls.Load(), a.Errors())
	}
	if calls.Load() != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", calls.Load())
	}
	if len(a.Errors()) != 0 {
		t.Fatalf("recovered source should not be reported as an error: %v", a.Errors())
	}
}

func TestAggregatorErrorsCarryNoLinks(t *testing.T) {
	fs := newFixtureServer(t)
	// The source URL itself is unroutable so the error text embeds it.
	a := newAggregator([]rssSource{{Name: "dead", URL: "https://dead.invalid/feed.xml"}}, fs.Client(), time.Minute)
	a.Articles(context.Background())
	errs := a.Errors()
	if len(errs) == 0 {
		t.Fatal("expected a recorded error")
	}
	joined := strings.Join(errs, " ")
	if containsLink(joined) {
		t.Fatalf("error text still carries a link/URL: %q", joined)
	}
	mustNotContain(t, joined, "dead.invalid")
}

// ---------------------------------------------------------------------------
// STE07 — the rendered document is the guarantee
// ---------------------------------------------------------------------------

func TestRenderRSSCarriesNoLinksAndNoPrivateElements(t *testing.T) {
	// Deliberately feed it UNsanitised values: renderRSS must defend itself.
	arts := []Article{{
		Source: "Fixture <b>News</b>",
		Title:  `<a href="https://evil.example">Click</a> www.evil.example now`,
		Summary: "Reach jsmith@evil.example or https://evil.example/x, " +
			"<script>alert(1)</script> or @handle — <enclosure url=\"https://evil.example/a.jpg\">",
		Published: time.Date(2025, 10, 6, 10, 0, 0, 0, time.UTC),
	}}
	body, err := renderRSS(arts, []string{"Fixture News"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	doc := string(body)

	mustContain(t, doc, "<rss version=\"2.0\">")
	mustContain(t, doc, "<title>")
	mustContain(t, doc, "Click")
	mustContain(t, doc, "no links and no private elements")

	// no links, in any form
	for _, bad := range []string{"http", "www.", "href", "@", "evil.example",
		"<link", "<guid", "<enclosure", "<comments", "<atom:", "<docs", "<cloud",
		"<id>", "url="} {
		mustNotContain(t, doc, bad)
	}
	// no private elements
	for _, bad := range []string{"<author", "<dc:creator", "<managingEditor",
		"<webMaster", "<copyright", "<generator", "<script", "<b>"} {
		mustNotContain(t, doc, bad)
	}

	// and it is still well-formed RSS whose items survive
	var back struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Items []struct {
				Title    string `xml:"title"`
				Category string `xml:"category"`
				PubDate  string `xml:"pubDate"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &back); err != nil {
		t.Fatalf("rendered feed is not valid XML: %v", err)
	}
	if len(back.Channel.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(back.Channel.Items))
	}
	it := back.Channel.Items[0]
	if it.Title == "" || it.PubDate == "" {
		t.Fatalf("item lost its title/date: %+v", it)
	}
	if containsLink(it.Title) || containsLink(it.Category) {
		t.Fatalf("item still carries a link: %+v", it)
	}
}

func TestRenderRSSKeepsProvenanceByNameOnly(t *testing.T) {
	fs := newFixtureServer(t)
	a := newAggregator(fs.sources("rss", "atom", "rdf"), fs.Client(), time.Minute)
	body, err := renderRSS(a.Articles(context.Background()), a.Sources(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	doc := string(body)
	for _, name := range []string{"rss", "atom", "rdf"} {
		mustContain(t, doc, "<category>"+name+"</category>")
	}
	mustNotContain(t, doc, fs.URL) // the fixture host must not leak
}

// ---------------------------------------------------------------------------
// STE06/08 — the HTTP endpoints
// ---------------------------------------------------------------------------

func TestServeRSSTypeAndBody(t *testing.T) {
	fs := newFixtureServer(t)
	a := newAggregator(fs.sources("rss", "atom"), fs.Client(), time.Minute)

	rec := httptest.NewRecorder()
	a.serveRSS(rec, httptest.NewRequest(http.MethodGet, "/stenella/feeds/all.xml", nil))

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/rss+xml") {
		t.Fatalf("content-type %q", ct)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "public") {
		t.Fatalf("cache-control %q", cc)
	}
	mustNotContain(t, rec.Body.String(), "http")
}

func TestServeJSONExposesSanitisedItems(t *testing.T) {
	fs := newFixtureServer(t)
	a := newAggregator(fs.sources("rss"), fs.Client(), time.Minute)

	rec := httptest.NewRecorder()
	a.serveJSON(rec, httptest.NewRequest(http.MethodGet, "/stenella/api/aggregate", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, `"source": "rss"`)
	mustNotContain(t, body, "http")
	mustNotContain(t, body, "desk@")
}

// ---------------------------------------------------------------------------
// configuration
// ---------------------------------------------------------------------------

func TestDefaultSourcesAreTheRequestedFive(t *testing.T) {
	srcs := defaultSources()
	want := []string{
		"RadioItalia (RAI)",
		"ANSA",
		"CBC",
		"Communications Security Establishment",
		"Government of Canada",
	}
	if len(srcs) != len(want) {
		t.Fatalf("got %d sources, want %d: %+v", len(srcs), len(want), srcs)
	}
	for i, w := range want {
		if srcs[i].Name != w {
			t.Errorf("source %d = %q, want %q", i, srcs[i].Name, w)
		}
		if !strings.HasPrefix(srcs[i].URL, "https://") {
			t.Errorf("source %q URL is not https: %q", srcs[i].Name, srcs[i].URL)
		}
	}
	joined := ""
	for _, s := range srcs {
		joined += s.Name + " " + s.URL + "\n"
	}
	mustContain(t, joined, "radioitalia.it")
	mustContain(t, joined, "ansa.it")
	mustContain(t, joined, "cbc.ca")
	mustContain(t, joined, "communicationssecurity") // CSE
	mustContain(t, joined, "National%20News")        // Government of Canada
}

func TestSourcesFromEnv(t *testing.T) {
	got, ok := sourcesFromEnv("Home=https://a.example/feed.xml,Wire=https://b.example/rss?x=1&y=2")
	if !ok || len(got) != 2 {
		t.Fatalf("ok=%v got=%+v", ok, got)
	}
	if got[1].Name != "Wire" || got[1].URL != "https://b.example/rss?x=1&y=2" {
		t.Fatalf("query string not preserved: %+v", got[1])
	}
	if _, ok := sourcesFromEnv("   "); ok {
		t.Fatal("empty env should not be accepted")
	}
	if _, ok := sourcesFromEnv("Broken"); ok {
		t.Fatal("a pair without '=' should be rejected")
	}
}

func TestRSSSourcesPrefersEnv(t *testing.T) {
	t.Setenv("STELLENA_RSS_SOURCES", "Only=https://one.example/feed")
	got := rssSources()
	if len(got) != 1 || got[0].Name != "Only" {
		t.Fatalf("got %+v", got)
	}
	t.Setenv("STELLENA_RSS_SOURCES", "")
	if len(rssSources()) != len(defaultSources()) {
		t.Fatal("empty env should fall back to the built-in sources")
	}
}
