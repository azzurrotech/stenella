package feed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const siteFixture = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Site title · Example</title>
  <meta property="og:title" content="The real headline">
  <meta property="og:url" content="https://site.test/posts/1/">
  <meta name="description" content="A short description of the page.">
  <meta name="author" content="Ada Lovelace">
  <meta name="keywords" content="engineering, history">
  <link rel="canonical" href="https://site.test/posts/1/">
  <script>var tracking = "should not appear in the body";</script>
  <style>.x { color: red }</style>
</head>
<body>
  <h1>Heading headline</h1>
  <article>
    <p>The body of the article, in one paragraph.</p>
    <p>A second paragraph with <em>emphasis</em> &amp; an ampersand.</p>
  </article>
  <time datetime="2026-09-30T08:15:00Z">30 September</time>
</body>
</html>`

// A page with nothing usable must say so rather than contribute an empty item.
const emptySiteFixture = `<!DOCTYPE html>
<html><head><meta charset="utf-8"></head><body><div>hello</div></body></html>`

func TestScrapeExtractsEnvelope(t *testing.T) {
	doc := parsePage(siteFixture)
	it, err := doc.item("https://site.test/posts/1", "text/html")
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	// OpenGraph wins over <title> and <h1>, in that order — that is the order a
	// publisher intends when they set them.
	if it.Title != "The real headline" {
		t.Errorf("Title = %q", it.Title)
	}
	if it.Link != "https://site.test/posts/1/" {
		t.Errorf("Link = %q, want the canonical URL", it.Link)
	}
	if it.Author != "Ada Lovelace" {
		t.Errorf("Author = %q", it.Author)
	}
	if it.Published.UTC().Format(time.RFC3339) != "2026-09-30T08:15:00Z" {
		t.Errorf("Published = %s", it.Published)
	}
	if !containsStr(it.Categories, "engineering") || !containsStr(it.Categories, "history") {
		t.Errorf("Categories = %v", it.Categories)
	}
	if !strings.Contains(it.Content, "body of the article") {
		t.Errorf("Content = %q", it.Content)
	}
	if !strings.Contains(it.Summary, "short description") {
		t.Errorf("Summary = %q, want the meta description", it.Summary)
	}
	// Content is the article, not the one-sentence meta description.
	if strings.Contains(it.Content, "short description") {
		t.Errorf("Content = %q, want the article body rather than the meta description", it.Content)
	}
}

// Script and style bodies are markup, not prose.
func TestScrapeStripsScriptAndStyle(t *testing.T) {
	doc := parsePage(siteFixture)
	body := doc.text("article")
	for _, leak := range []string{"tracking", "color: red", "function"} {
		if strings.Contains(body, leak) {
			t.Errorf("article body leaked %q: %s", leak, body)
		}
	}
}

// A page with no headline anywhere is an error the source status can report, not
// an empty item.
func TestScrapeNoHeadlineIsAnError(t *testing.T) {
	doc := parsePage(emptySiteFixture)
	if _, err := doc.item("https://empty.test/", "text/html"); err == nil {
		t.Fatal("expected an error for a page with no headline")
	} else if !strings.Contains(err.Error(), "no headline") {
		t.Errorf("error = %v", err)
	}
	// A page with only an <h1> is still usable.
	doc = parsePage(`<html><body><h1>Only a heading</h1></body></html>`)
	it, err := doc.item("https://h1.test/", "text/html")
	if err != nil {
		t.Fatalf("h1-only page: %v", err)
	}
	if it.Title != "Only a heading" {
		t.Errorf("Title = %q", it.Title)
	}
}

// A page with no date becomes a fresh item instead of failing: someone subscribed
// to it as a "what changed" signal.
func TestScrapeUndatedBecomesFresh(t *testing.T) {
	doc := parsePage(`<html><head><title>Undated</title></head><body><p>hi</p></body></html>`)
	it, err := doc.item("https://undated.test/", "text/html")
	if err != nil {
		t.Fatal(err)
	}
	if it.Published.IsZero() {
		t.Fatal("Published is zero; an undated page should be treated as fresh")
	}
	if d := time.Since(it.Published); d > time.Minute {
		t.Errorf("Published = %s ago, want ~now", d)
	}
}

func TestScrapeSummarize(t *testing.T) {
	if got := summarize(""); got != "" {
		t.Errorf("summarize(\"\") = %q", got)
	}
	if got := summarize("<p>short</p>"); got != "short" {
		t.Errorf("summarize = %q", got)
	}
	// Entities are decoded and whitespace collapsed.
	if got := summarize("a &amp;  b\n\tc"); got != "a & b c" {
		t.Errorf("summarize = %q", got)
	}
	// Long input is cut on a word boundary, not mid-word.
	long := strings.Repeat("word ", 2000)
	got := summarize(long)
	// The limit is in characters, not bytes: the elision is a three-byte rune.
	if n := utf8.RuneCountInString(got); n > scraperSummaryLimit {
		t.Errorf("summary length = %d runes, want <= %d", n, scraperSummaryLimit)
	}
	trimmed := strings.TrimSuffix(got, "…")
	if strings.HasSuffix(trimmed, "wor") {
		t.Errorf("summary cut mid-word: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated summary should be elided: %q", got[len(got)-10:])
	}
}

// The fetch path must route a real HTTP page through the scraper, and an Atom
// feed served at an extensionless URL must still be parsed as Atom.
func TestFetchRoutesByBody(t *testing.T) {
	siteSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(siteFixture))
	}))
	defer siteSrv.Close()
	atomSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(atomFixture))
	}))
	defer atomSrv.Close()

	root := t.TempDir()
	e, err := New(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	// Declared as a site: scraped.
	site, err := e.AddSourceClassified("acme", "Example", siteSrv.URL+"/posts/1", KindSite, 60, "", 1, "public")
	if err != nil {
		t.Fatalf("add site source: %v", err)
	}
	f, err := e.Fetch("acme", site, nil)
	if err != nil {
		t.Fatalf("fetch site: %v", err)
	}
	if len(f.Items) != 1 || f.Items[0].Title != "The real headline" {
		t.Fatalf("scraped items = %+v", titlesOf(f.Items))
	}

	// Declared as RSS but serving HTML: scraped rather than rejected, so a
	// misconfigured feed is still useful.
	mis, err := e.AddSourceClassified("acme", "Mis", siteSrv.URL+"/mis", KindRSS, 60, "", 1, "public")
	if err != nil {
		t.Fatal(err)
	}
	if f, err := e.Fetch("acme", mis, nil); err != nil || len(f.Items) != 1 {
		t.Fatalf("HTML body for an RSS source = %v, %+v", err, titlesOf(f.Items))
	}

	// A URL with no feed-ish extension serving Atom: still parsed as Atom.
	feed, err := e.AddSourceClassified("acme", "Atomish", atomSrv.URL+"/blog/latest", KindSite, 60, "", 1, "public")
	if err != nil {
		t.Fatal(err)
	}
	if f, err := e.Fetch("acme", feed, nil); err != nil {
		t.Fatalf("fetch atom at an extensionless URL: %v", err)
	} else if f.Items[0].Title != "Atom entry" {
		t.Fatalf("items = %+v, want the Atom entry", titlesOf(f.Items))
	}

	// Declared as RSS and really serving RSS: untouched.
	realSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer realSrv.Close()
	real, err := e.AddSourceClassified("acme", "Real", realSrv.URL+"/feed.rss", KindRSS, 60, "", 1, "public")
	if err != nil {
		t.Fatal(err)
	}
	if f, err := e.Fetch("acme", real, nil); err != nil || len(f.Items) != 2 {
		t.Fatalf("rss fetch = %v, %+v", err, titlesOf(f.Items))
	}
}

// A page larger than the scrape bound is truncated rather than parsed whole.
// The fetch path allows a much larger read so a feed document is never cut mid
// element; the extra bytes of an HTML page are inline trackers and base64
// images, so they are dropped before parsing instead of after.
//
// Truncation necessarily removes the closing tags, which is why elementText
// reads an unterminated block to the end of the input. Both halves are asserted
// here because either alone would pass while the pair is broken: without the
// bound the page is parsed whole, and without the unterminated fallback the
// bound silently yields an empty body.
func TestScrapeTruncatesOversizedPage(t *testing.T) {
	e := &Engine{}
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8">`)
	b.WriteString(`<meta property="og:title" content="Huge page">`)
	b.WriteString(`</head><body><article><p>Opening paragraph.</p>`)
	// Padding well past the bound, so a parse that ignored the bound produces a
	// visibly larger record.
	b.WriteString(strings.Repeat(" filler words here. ", scraperMaxBytes))
	b.WriteString(`</article></body></html>`)
	page := []byte(b.String())
	if len(page) <= scraperMaxBytes {
		t.Fatalf("fixture is %d bytes, which does not exceed the %d bound", len(page), scraperMaxBytes)
	}

	items, err := e.scrapeBytes("https://big.test/page", "text/html", page)
	if err != nil {
		t.Fatalf("scrapeBytes: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	// The headline lives in the head, well inside the bound.
	if it.Title != "Huge page" {
		t.Errorf("Title = %q", it.Title)
	}
	// The body starts inside the bound too, and the text that arrived is kept
	// even though the closing </article> was cut off.
	if !strings.Contains(it.Content, "Opening paragraph") {
		t.Errorf("Content = %.80q, want the prose before the bound", it.Content)
	}
	// The bound is what bounds the record: without it this is ~24x larger.
	if len(it.Content) > scraperMaxBytes {
		t.Errorf("Content is %d bytes, want at most the %d bound", len(it.Content), scraperMaxBytes)
	}

	// A page inside the bound keeps its closing tag and parses identically, so
	// the unterminated fallback above is not masking a general parse failure.
	small, err := e.scrapeBytes("https://small.test/page", "text/html", []byte(siteFixture))
	if err != nil || len(small) != 1 {
		t.Fatalf("small page = %v, %d items", err, len(small))
	}
	if small[0].Title != "The real headline" {
		t.Errorf("small page Title = %q", small[0].Title)
	}
	if !strings.Contains(small[0].Content, "body of the article") {
		t.Errorf("small page Content = %.80q", small[0].Content)
	}
}

func TestGuessKindRecognisesSites(t *testing.T) {
	cases := map[string]string{
		"https://site.test/feed.rss":           KindRSS,
		"https://site.test/feed.xml":           KindRSS,
		"https://site.test/feed.atom":          KindAtom,
		"https://site.test/feed.json":          KindJSON,
		"https://site.test/subscriptions.opml": KindOPML,
		"https://site.test/posts/1/":           KindSite,
		"https://site.test/blog/latest":        KindSite,
		"https://site.test/":                   KindSite,
	}
	for url, want := range cases {
		if got := GuessKind(url); got != want {
			t.Errorf("GuessKind(%q) = %q, want %q", url, got, want)
		}
	}
	// A query string does not hide the extension.
	if got := GuessKind("https://site.test/feed.atom?id=7"); got != KindAtom {
		t.Errorf("GuessKind with a query = %q", got)
	}
}

func TestLooksLikeHTML(t *testing.T) {
	if !looksLikeHTML("text/html", nil) {
		t.Error("content-type alone should be enough")
	}
	if !looksLikeHTML("", []byte("<!DOCTYPE html><html></html>")) {
		t.Error("doctype should be detected")
	}
	if looksLikeHTML("application/rss+xml", []byte(rssFixture)) {
		t.Error("an RSS body is not HTML")
	}
	if looksLikeHTML("", []byte("{\"items\":[]}")) {
		t.Error("a JSON body is not HTML")
	}
}

func titlesOf(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}
