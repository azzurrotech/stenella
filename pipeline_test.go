package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// STX04 — collection: the five formats
// ---------------------------------------------------------------------------

// JSON Feed is the fourth format: a document that describes its items in JSON.
const jsonFeedFixture = `{
  "version": "https://jsonfeed.org/version/1.1",
  "title": "JSON Fixture",
  "items": [
    {
      "id": "json-1",
      "url": "https://json.example/1",
      "title": "JSON Feed headline",
      "content_text": "Plain body from a JSON Feed, mentioning Ottawa in passing.",
      "date_published": "2026-01-15T12:00:00Z",
      "tags": ["cyber"]
    },
    {
      "id": "json-2",
      "title": "Second JSON item",
      "content_html": "<p>HTML body with a <a href='https://json.example/2'>link</a>.</p>",
      "date_modified": "2026-01-14T12:00:00Z"
    }
  ]
}`

// The fifth format: a plain HTML page carrying article markup.
const pageArticleFixture = `<!doctype html>
<html lang="en">
<head>
  <title>Northwind News — latest</title>
  <meta property="article:published_time" content="2026-09-30T08:00:00Z">
  <script src="https://track.example/pixel.js">tracker.load("https://track.example/x")</script>
  <style>.nav{background:url(https://track.example/bg.png)}</style>
</head>
<body>
  <nav><ul><li><h3><a href="https://page.example/menu">Menu</a></h3></li></ul></nav>
  <main>
    <article id="story-1">
      <h2>Ransomware hits <em>Northwind Energy Ltd</em> &amp; suppliers</h2>
      <time datetime="2026-10-02T09:30:00Z">2 October 2026</time>
      <p>Attackers struck the supplier network in Ottawa. Contact
         <a href="https://page.example/p">desk</a> at press@northwind.example</p>
    </article>
    <article>
      <h2><a href="https://page.example/2">Minister of Defence briefs @csiscyber on #ransomware</a></h2>
      <p>Briefing held in Rome.</p>
    </article>
  </main>
  <footer>© Northwind Media</footer>
</body></html>`

// A page with no article markup: an index of headline links.
const pageHeadingsFixture = `<!doctype html><html><head><title>Index</title></head><body>
  <h1><a href="https://index.example/1">Top story from Ottawa</a></h1>
  <p>First paragraph of the top story.</p>
  <p>Second paragraph.</p>
  <h2><a href="https://index.example/2">Second story from Rome</a></h2>
  <p>Body of the second story.</p>
</body></html>`

// A page that carries nothing collectable — an error page, not an empty feed.
const pageNoItemsFixture = `<!doctype html><html><body><h1>404</h1><p>Not found</p></body></html>`

func TestParseFeedJSONFeed(t *testing.T) {
	items, err := parseFeed([]byte(jsonFeedFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].When.UTC().Format(time.RFC3339) != "2026-01-15T12:00:00Z" {
		t.Errorf("date_published not parsed: %v", items[0].When)
	}
	if items[0].Link != "https://json.example/1" || items[0].GUID != "json-1" {
		t.Errorf("identity fields wrong: %+v", items[0])
	}
	if len(items[0].Categories) != 1 || items[0].Categories[0] != "cyber" {
		t.Errorf("tags not carried: %v", items[0].Categories)
	}
	// content_text wins over content_html, so the body arrives as prose
	if strings.Contains(items[0].Body, "<") {
		t.Errorf("body is markup: %q", items[0].Body)
	}
	if items[1].When.UTC().Format(time.RFC3339) != "2026-01-14T12:00:00Z" {
		t.Errorf("date_modified not used as fallback: %v", items[1].When)
	}
}

func TestParseFeedCollectsArticlePage(t *testing.T) {
	items, err := parseFeed([]byte(pageArticleFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d page items, want 2", len(items))
	}

	first := items[0]
	if first.Title != "Ransomware hits Northwind Energy Ltd & suppliers" {
		t.Errorf("title = %q", first.Title)
	}
	if first.When.UTC().Format(time.RFC3339) != "2026-10-02T09:30:00Z" {
		t.Errorf("<time datetime> not parsed: %v", first.When)
	}
	if first.GUID != "story-1" {
		t.Errorf("article id not used as guid: %q", first.GUID)
	}
	if first.Link != "https://page.example/p" {
		t.Errorf("first anchor not used as link: %q", first.Link)
	}
	if !strings.Contains(first.Body, "supplier network in Ottawa") {
		t.Errorf("body lost the prose: %q", first.Body)
	}
	if strings.Contains(first.Body, "<") || strings.Contains(first.Body, "https://") {
		t.Errorf("body kept markup: %q", first.Body)
	}
	// nav, footer, scripts and styles are not the page's content
	if strings.Contains(first.Body, "Menu") || strings.Contains(first.Body, "Northwind Media") {
		t.Errorf("page furniture leaked into the body: %q", first.Body)
	}
	if strings.Contains(first.Title, "<") {
		t.Errorf("title kept markup: %q", first.Title)
	}

	// an entry with no <time> inherits the page's own publish time
	second := items[1]
	if second.When.UTC().Format(time.RFC3339) != "2026-09-30T08:00:00Z" {
		t.Errorf("page meta time not used: %v", second.When)
	}
	if second.Title != "Minister of Defence briefs @csiscyber on #ransomware" {
		t.Errorf("second title = %q", second.Title)
	}
	if second.GUID != second.Link || second.GUID == "" {
		t.Errorf("an article with no id must still get a stable guid: %+v", second)
	}
}

// A page can carry an article with neither an id nor a link. It is still an
// item: the headline is the story, and its identity falls to the title.
func TestParsePageArticleWithoutLinksStillYieldsAnItem(t *testing.T) {
	page := `<!doctype html><html><body>
<article><h2>Headline with nothing to click</h2><p>Prose that stands alone.</p></article>
</body></html>`
	items, err := parseFeed([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Title != "Headline with nothing to click" || items[0].Link != "" {
		t.Errorf("item = %+v", items[0])
	}

	src := &Source{ID: "s", Name: "S", Collection: "news"}
	it, ok := normalise(src, items[0], time.Now())
	if !ok {
		t.Fatal("normalise rejected a linkless page item")
	}
	if it.ID == "" || it.ContentHash == "" {
		t.Fatalf("no identity for a linkless item: %+v", it)
	}
	if it.GUIDHash != "" || it.LinkHash != "" {
		t.Errorf("invented an identity: %+v", it)
	}
}

func TestParseFeedCollectsHeadlineListingPage(t *testing.T) {
	items, err := parseFeed([]byte(pageHeadingsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Title != "Top story from Ottawa" {
		t.Errorf("title = %q", items[0].Title)
	}
	if items[0].Link != "https://index.example/1" || items[0].GUID != items[0].Link {
		t.Errorf("headline link/guid = %q / %q", items[0].Link, items[0].GUID)
	}
	// the prose after the heading, up to the next heading, is the body
	if !strings.Contains(items[0].Body, "First paragraph of the top story.") ||
		!strings.Contains(items[0].Body, "Second paragraph.") {
		t.Errorf("body = %q", items[0].Body)
	}
	if strings.Contains(items[0].Body, "Second story") {
		t.Errorf("body ran past the next heading: %q", items[0].Body)
	}
	if items[1].Title != "Second story from Rome" || items[1].Link != "https://index.example/2" {
		t.Errorf("second item = %+v", items[1])
	}
	// the page-level <title> is not an item
	for _, it := range items {
		if it.Title == "Index" {
			t.Error("the page title was collected as an item")
		}
	}
}

// A page with nothing collectable must fail loudly: reporting an empty success
// would look like "this source is quiet" instead of "this source is broken".
func TestParseFeedRejectsPageWithNoItems(t *testing.T) {
	if _, err := parseFeed([]byte(pageNoItemsFixture)); err == nil {
		t.Fatal("expected an error for a page with no items")
	}
}

func TestParseFeedRejectsBytesThatAreNotAPage(t *testing.T) {
	if _, err := parseFeed([]byte("just some plain text, not markup at all")); err == nil {
		t.Fatal("expected an error for plain text")
	}
	if _, err := parsePage([]byte("plain text")); err == nil {
		t.Fatal("parsePage must not accept text that is not a page")
	}
}

// ---------------------------------------------------------------------------
// STX04 — processing: normalise, dedupe, classify, extract
// ---------------------------------------------------------------------------

func TestNormaliseProducesALinkFreeRecord(t *testing.T) {
	src := &Source{ID: "src-1", Name: "Fixture News", Collection: "news", ACL: "public"}
	raw := rawItem{
		Title:      "Vendor <b>confirms</b> breach — see https://evil.example/x?y=1",
		Body:       `<p>Contact <a href="https://fixture.example/p">us</a> at desk@fixture.example or https://fixture.example/contact</p><script>steal()</script>`,
		When:       time.Date(2025, 10, 6, 10, 0, 0, 0, time.UTC),
		GUID:       "https://fixture.example/a",
		Link:       "https://fixture.example/a",
		Categories: []string{"Cyber Security"},
	}
	fetched := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)

	it, ok := normalise(src, raw, fetched)
	if !ok {
		t.Fatal("normalise rejected an item that has a title")
	}

	for _, field := range []string{it.Title, it.Summary, it.Body} {
		if containsLink(field) {
			t.Errorf("stored field still carries a link: %q", field)
		}
		if strings.Contains(field, "@") {
			t.Errorf("stored field still carries an address or handle: %q", field)
		}
		if strings.Contains(field, "<script") {
			t.Errorf("stored field kept script: %q", field)
		}
	}

	// identity survives even though the link does not: guid, link and title each
	// hash to their own stable key
	var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)
	for label, v := range map[string]string{
		"guidHash": it.GUIDHash, "linkHash": it.LinkHash, "contentHash": it.ContentHash,
	} {
		if !hex32.MatchString(v) {
			t.Errorf("%s = %q, want 32 hex characters", label, v)
		}
	}
	if it.GUIDHash != shortHash(strings.TrimSpace(raw.GUID)) {
		t.Errorf("guidHash = %q, want the hash of the raw guid", it.GUIDHash)
	}
	if it.LinkHash != shortHash(canonicalLink(raw.Link)) {
		t.Errorf("linkHash = %q, want the hash of the canonical link", it.LinkHash)
	}
	if it.ID != "item-"+it.ContentHash {
		t.Errorf("id = %q, want item-<contentHash>", it.ID)
	}
	if it.Source != "Fixture News" || it.Collection != "news" || it.ACL != "public" {
		t.Errorf("provenance/class lost: %+v", it)
	}
	if len(it.Attributions) != 1 || it.Attributions[0] != "Fixture News" {
		t.Errorf("attribution = %v", it.Attributions)
	}
	if !it.Published.Equal(raw.When) {
		t.Errorf("published = %v, want %v", it.Published, raw.When)
	}
	if !it.Fetched.Equal(fetched) {
		t.Errorf("fetched = %v, want %v", it.Fetched, fetched)
	}

	// the text drives the labels: "breach" and "vendor" are in the vocabulary
	cats := strings.Join(it.Categories, ",")
	if !strings.Contains(cats, "cyber") {
		t.Errorf("expected the cyber label, got %q", cats)
	}
	if !strings.Contains(cats, "supply-chain") {
		t.Errorf("expected the supply-chain label, got %q", cats)
	}
	// the source's own category survives alongside them
	if !strings.Contains(strings.ToLower(cats), "cyber security") {
		t.Errorf("source category lost: %q", cats)
	}
}

func TestNormaliseGivesUndatedItemsTheFetchTime(t *testing.T) {
	src := &Source{ID: "s", Name: "S", Collection: "news"}
	fetched := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

	it, ok := normalise(src, rawItem{Title: "Undated"}, fetched)
	if !ok {
		t.Fatal("rejected")
	}
	if !it.Published.Equal(fetched) {
		t.Errorf("published = %v, want the fetch time %v", it.Published, fetched)
	}
	// the fetch time must be stored as UTC so it sorts correctly as a string
	if it.Published.Location() != time.UTC {
		t.Errorf("published is not UTC: %v", it.Published.Location())
	}
}

func TestNormaliseRejectsUntitledEntries(t *testing.T) {
	src := &Source{ID: "s", Name: "S"}
	for _, raw := range []rawItem{
		{Title: ""},
		{Title: "   "},
		{Title: `<script>x</script>`}, // sanitises away to nothing
		{Title: `https://only.example/a`},
	} {
		if it, ok := normalise(src, raw, time.Now()); ok {
			t.Errorf("accepted an untitled entry: %+v -> %q", raw, it.Title)
		}
	}
}

func TestNormaliseIsIdempotentForTheSameHeadline(t *testing.T) {
	src := &Source{ID: "s", Name: "S", Collection: "news"}
	a, _ := normalise(src, rawItem{Title: "Same headline"}, time.Now())
	b, _ := normalise(src, rawItem{Title: "  SAME   headline "}, time.Now())
	if a.ID != b.ID || a.ContentHash != b.ContentHash {
		t.Errorf("the same headline produced two identities: %q vs %q", a.ID, b.ID)
	}
}

// The README's identity chain: guid, then canonical link, then title.
func TestFindDuplicateWalksTheIdentityChain(t *testing.T) {
	a := &App{store: newTestStore(t)}
	put := func(id, title, guid, link string) {
		it := &Item{
			ID: id, Title: title, Collection: "news",
			GUIDHash:    guid,
			LinkHash:    link,
			ContentHash: shortHash(titleKey(title)),
		}
		if err := a.store.PutItem(it); err != nil {
			t.Fatal(err)
		}
	}
	put("item-guid", "First wire", "gh-1", "lh-1")
	put("item-link", "Second wire", "", "lh-2")
	put("item-title", "Third wire", "", "")

	check := func(label string, probe *Item, wantID string) {
		t.Helper()
		got := a.findDuplicate(probe)
		if got == nil {
			t.Fatalf("%s: no duplicate found", label)
		}
		if got.ID != wantID {
			t.Errorf("%s: matched %q, want %q", label, got.ID, wantID)
		}
	}

	check("guid match", &Item{Title: "noise", GUIDHash: "gh-1", LinkHash: "nope"}, "item-guid")
	check("link match (guid absent)", &Item{Title: "noise", LinkHash: "lh-2"}, "item-link")
	check("link match (guid misses)", &Item{Title: "noise", GUIDHash: "gh-absent", LinkHash: "lh-2"}, "item-link")
	check("content match", &Item{Title: "Third wire", ContentHash: shortHash(titleKey("Third wire"))}, "item-title")

	if got := a.findDuplicate(&Item{Title: "Nothing like it", ContentHash: shortHash(titleKey("Nothing like it"))}); got != nil {
		t.Errorf("matched an item that is not there: %q", got.ID)
	}
}

// A story seen on two wires becomes one record with both attributions, and the
// first wording is kept — a later wire only adds provenance.
func TestMergeIntoKeepsEveryAttributionAndTheFirstWording(t *testing.T) {
	a := &App{store: newTestStore(t)}
	existing := &Item{
		ID: "item-1", Title: "First wording", Summary: "First summary",
		Source: "Wire A", Attributions: []string{"Wire A"},
		LinkHash: "lh-1", Collection: "news",
		Published: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}
	seen := &Item{
		ID: "item-1", Title: "Different wording, same story", Summary: "Second summary",
		Source: "Wire B", Attributions: []string{"Wire B"},
		LinkHash: "lh-1", Collection: "news",
		Published: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	}

	if !a.mergeInto(existing, seen) {
		t.Fatal("merge reported no change")
	}
	if len(existing.Attributions) != 2 {
		t.Fatalf("attributions = %v, want both", existing.Attributions)
	}
	if existing.Title != "First wording" || existing.Summary != "First summary" {
		t.Errorf("a link-matched sighting overwrote the first wording: %+v", existing)
	}
	if !existing.Published.Equal(seen.Published) {
		t.Errorf("newer publication time not adopted: %v", existing.Published)
	}
	if a.mergeInto(existing, seen) {
		t.Error("re-reading the same sighting must not report a change")
	}
	if len(existing.Attributions) != 2 {
		t.Errorf("attributions duplicated: %v", existing.Attributions)
	}
}

// A guid match is the same record re-read: the wire may carry a new headline.
func TestMergeIntoUpdatesProseOnAGuidMatch(t *testing.T) {
	a := &App{store: newTestStore(t)}
	existing := &Item{ID: "i", Title: "Old headline", Summary: "", GUIDHash: "gh-1",
		Attributions: []string{"Wire A"}}
	seen := &Item{ID: "i", Title: "Corrected headline", Summary: "Corrected summary",
		GUIDHash: "gh-1", Attributions: []string{"Wire A"}}

	a.mergeInto(existing, seen)
	if existing.Title != "Corrected headline" || existing.Summary != "Corrected summary" {
		t.Errorf("guid match did not update the prose: %+v", existing)
	}
	if len(existing.Attributions) != 1 {
		t.Errorf("attribution duplicated on a re-read: %v", existing.Attributions)
	}
}

func TestClassifyAddsCanonicalLabelsFromText(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"Ransomware encrypts hospital systems", "cyber"},
		{"Chip supplier delays shipments", "supply-chain"},
		{"NATO ministers meet on defence", "security"},
		{"Inflation cools as GDP grows", "economy"},
		{"Minister announces new policy", "government"},
		{"Wildcard term no one thought of", ""},
	}
	for _, c := range cases {
		got := classify(c.text, "", nil)
		joined := strings.Join(got, ",")
		if c.want == "" {
			if joined != "" {
				t.Errorf("%q classified as %q, want nothing", c.text, joined)
			}
			continue
		}
		found := false
		for _, g := range got {
			if g == c.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q classified as %q, want label %q", c.text, joined, c.want)
		}
	}

	// supplied categories are kept alongside the derived ones
	got := classify("Anything", "", []string{"press-release"})
	if !containsFold(got, "press-release") {
		t.Errorf("supplied category dropped: %v", got)
	}
	// one label per vocabulary term, not one per occurrence
	got = classify("ransomware and ransomware and ransomware", "", nil)
	count := 0
	for _, g := range got {
		if g == "cyber" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("cyber label counted %d times, want 1", count)
	}
}

func TestExtractEntitiesFindsEveryKindTheREADMETions(t *testing.T) {
	raw := rawItem{
		Title: "Prime Minister Mark Carney visits Ottawa with @csiscyber #ransomware",
		Body:  "Northwind Energy Ltd filed the report. Write to press@northwind.example again @csiscyber",
	}
	hits := extractEntities(raw)

	byKind := map[string][]string{}
	for _, h := range hits {
		byKind[h.Kind] = append(byKind[h.Kind], h.Name)
	}

	want := map[string]string{
		"person":  "Prime Minister Mark Carney",
		"place":   "Ottawa",
		"handle":  "@csiscyber",
		"keyword": "ransomware",
		"org":     "Northwind Energy Ltd",
	}
	for kind, name := range want {
		found := false
		for _, n := range byKind[kind] {
			if n == name {
				found = true
			}
		}
		if !found {
			t.Errorf("no %s entity %q (got %v)", kind, name, byKind[kind])
		}
	}

	// an address is not a handle: the email must not be reported as @…
	for _, n := range byKind["handle"] {
		if strings.Contains(n, "press") || strings.Contains(n, "northwind.example") {
			t.Errorf("an email address was reported as a handle: %q", n)
		}
	}

	// the same handle twice in one item is one entity
	if len(byKind["handle"]) != 1 {
		t.Errorf("handle dedupe failed: %v", byKind["handle"])
	}
	// categories become keywords too
	withCats := extractEntities(rawItem{Title: "x", Categories: []string{"cyber"}})
	found := false
	for _, h := range withCats {
		if h.Kind == "keyword" && strings.EqualFold(h.Name, "cyber") {
			found = true
		}
	}
	if !found {
		t.Errorf("source categories not extracted as keywords: %+v", withCats)
	}
}

func TestExtractEntitiesIgnoresEmptyAndOversizedText(t *testing.T) {
	if hits := extractEntities(rawItem{}); hits != nil {
		t.Errorf("empty item produced hits: %+v", hits)
	}
	huge := rawItem{Title: strings.Repeat("word ", 60) + "@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, h := range extractEntities(huge) {
		if len(h.Name) > 80 {
			t.Errorf("oversized name stored: %d chars", len(h.Name))
		}
	}
}

// Coordinates come from the item's wording: an explicit pair first, then the
// offline gazetteer. There is no geocoder and no network lookup.
func TestExtractIntoWritesCoordinates(t *testing.T) {
	cases := []struct {
		name string
		raw  rawItem
		want float64
		like float64
	}{
		{
			name: "explicit pair",
			raw:  rawItem{Title: "Blast site at 45.4215, -75.6972 near Ottawa", Body: "Officials confirmed."},
			want: 45.4215, like: -75.6972,
		},
		{
			name: "gazetteer place",
			raw:  rawItem{Title: "Talks resume in Rome", Body: "Delegates gathered."},
			want: 41.9028, like: 12.4964,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &App{store: newTestStore(t)}
			it := &Item{ID: "item-x", Title: "t", Collection: "news"}
			if n := a.extractInto(it, c.raw); n == 0 {
				t.Fatal("no entities extracted, so the coordinate step never ran")
			}
			if it.Geo == "" {
				t.Fatal("no coordinates recorded")
			}
			lat, lon, ok := parseGeo(it.Geo)
			if !ok {
				t.Fatalf("stored geo %q does not parse", it.Geo)
			}
			if diff(lat, c.want) > 1e-4 || diff(lon, c.like) > 1e-4 {
				t.Errorf("geo = %v,%v, want %v,%v", lat, lon, c.want, c.like)
			}
			if len(it.Entities) == 0 {
				t.Error("entity ids not attached to the item")
			}
		})
	}
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestExtractIntoLeavesItemsWithoutCoordinates(t *testing.T) {
	a := &App{store: newTestStore(t)}
	it := &Item{ID: "item-y", Title: "t"}
	a.extractInto(it, rawItem{Title: "No place named anywhere", Body: "Nothing to map."})
	if it.Geo != "" {
		t.Errorf("invented coordinates: %q", it.Geo)
	}
}

// ---------------------------------------------------------------------------
// STX04 — the pipeline end to end
// ---------------------------------------------------------------------------

// pipeFixture serves one endpoint per collection format plus a broken source.
func pipeFixture(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(path, contentType, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write([]byte(body))
		})
	}
	serve("/rss.xml", "application/rss+xml", rssFixture)
	serve("/atom.xml", "application/atom+xml", atomFixture)
	serve("/json.json", "application/json", jsonFeedFixture)
	serve("/page.html", "text/html; charset=utf-8", pageArticleFixture)
	serve("/broken.html", "text/html; charset=utf-8", htmlNotAFeed)
	mux.HandleFunc("/fail.xml", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func pipeApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	fs := pipeFixture(t)
	store := newTestStore(t)

	for _, s := range []struct{ name, path, coll string }{
		{"rss", "/rss.xml", "news"},
		{"atom", "/atom.xml", "news"},
		{"json", "/json.json", "news"},
		{"page", "/page.html", "news"},
		{"broken", "/broken.html", "news"},
	} {
		if err := store.PutSource(&Source{
			Name: s.name, URL: fs.URL + s.path, Kind: "feed",
			Collection: s.coll, ACL: "public", Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range defaultRules() {
		rule := r
		if err := store.PutRule(&rule); err != nil {
			t.Fatal(err)
		}
	}

	a := &App{
		store:   store,
		client:  fs.Client(),
		fetcher: newAggregator(nil, fs.Client(), time.Minute),
		aggs:    map[string]*aggregator{},
	}
	return a, fs
}

func TestRefreshCollectsEveryFormatAndReportsTheBrokenSource(t *testing.T) {
	a, _ := pipeApp(t)

	rep, err := a.refresh(context.Background(), "all")
	if err != nil {
		t.Fatal(err)
	}

	if rep.Sources != 5 {
		t.Errorf("sources = %d, want 5", rep.Sources)
	}
	wantPerSource := map[string]int{"rss": 3, "atom": 2, "json": 2, "page": 2}
	for name, want := range wantPerSource {
		if got := rep.PerSource[name]; got != want {
			t.Errorf("PerSource[%s] = %d, want %d", name, got, want)
		}
	}
	if _, ok := rep.PerSource["broken"]; ok {
		t.Error("a failed source must not report an item count")
	}
	if rep.Fetched != 9 {
		t.Errorf("fetched = %d, want 9", rep.Fetched)
	}
	// atom and rss both carry "Shared headline": four formats, one record
	if rep.Collapsed < 1 {
		t.Errorf("collapsed = %d, want at least the cross-format story", rep.Collapsed)
	}
	if rep.New < 8 {
		t.Errorf("new = %d, want 8 (9 fetched, 1 collapsed)", rep.New)
	}
	if rep.Entities == 0 {
		t.Error("no entities extracted")
	}
	if rep.Links == 0 {
		t.Error("no candidate links proposed for items that share a place")
	}
	if rep.Insights == 0 {
		t.Error("the seeded rules produced no insights")
	}
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0], "broken") {
		t.Errorf("errors = %v, want exactly the broken source", rep.Errors)
	}
	for _, e := range rep.Errors {
		if containsLink(e) {
			t.Errorf("a fetch error smuggled a link: %q", e)
		}
	}
	if rep.Finished.Before(rep.Started) {
		t.Error("report time travel")
	}

	// the broken source is marked failed; the working ones are marked ok
	sources, err := a.store.Sources()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sources {
		switch s.Name {
		case "broken":
			if !strings.HasPrefix(s.LastStatus, "error:") {
				t.Errorf("broken status = %q", s.LastStatus)
			}
		default:
			if s.LastStatus != "ok" {
				t.Errorf("%s status = %q", s.Name, s.LastStatus)
			}
		}
		if containsLink(s.LastStatus) {
			t.Errorf("%s status carries a link: %q", s.Name, s.LastStatus)
		}
	}

	// 9 entries fetched across four formats, one of them a second wire carrying
	// a story the atom source already reported -> 8 records
	// every stored record is link-free and private-free — the README's guarantee
	items, err := a.store.ItemsIn("", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 8 {
		t.Fatalf("stored %d items, want 8", len(items))
	}
	for _, it := range items {
		for _, field := range []string{it.Title, it.Summary, it.Body} {
			if containsLink(field) {
				t.Errorf("%s stored a link: %q", it.ID, field)
			}
		}
		if len(it.Attributions) == 0 {
			t.Errorf("%s has no provenance", it.ID)
		}
	}
}

// Running the same collection twice must add nothing: identity is content, so
// the second pass re-reads the same records instead of duplicating them.
func TestRefreshIsIdempotentAcrossRuns(t *testing.T) {
	a, _ := pipeApp(t)

	// the first run is the baseline; it must have collected the corpus once
	if _, err := a.refresh(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
	second, err := a.refresh(context.Background(), "all")
	if err != nil {
		t.Fatal(err)
	}

	if second.New != 0 {
		t.Errorf("second run created %d new items, want 0", second.New)
	}
	if second.Collapsed != 0 {
		t.Errorf("second run collapsed %d items, want 0", second.Collapsed)
	}
	if second.New+second.Collapsed != 0 {
		t.Errorf("second run = %+v", second)
	}
	items, _ := a.store.ItemsIn("", "", 0)
	if len(items) != 8 {
		t.Errorf("corpus grew to %d items, want 8", len(items))
	}
}

// Two wires describing one story must collapse onto one record carrying both
// names, and a third read of the same wire must change nothing.
func TestProcessSourceCollapsesTwoWiresIntoOneRecord(t *testing.T) {
	a := &App{store: newTestStore(t)}
	when := time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC)
	fetched := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

	wireA := &Source{ID: "sa", Name: "Wire A", Collection: "news", ACL: "public"}
	wireB := &Source{ID: "sb", Name: "Wire B", Collection: "news", ACL: "public"}

	story := func(guid, link, wire string) rawItem {
		return rawItem{
			Title: "Shared headline", Body: "Reporting from Ottawa.",
			When: when, GUID: guid, Link: link,
		}
	}

	newA, collA, entA := a.processSource(wireA, []rawItem{story("g-a", "https://a.example/1", "A")}, fetched)
	if newA != 1 || collA != 0 || entA == 0 {
		t.Fatalf("first wire: new=%d collapsed=%d entities=%d, want 1/0/>0", newA, collA, entA)
	}

	newB, collB, entB := a.processSource(wireB, []rawItem{story("g-b", "https://b.example/2", "B")}, fetched)
	if newB != 0 || collB != 1 {
		t.Fatalf("second wire: new=%d collapsed=%d, want 0/1", newB, collB)
	}
	_ = entB

	items, err := a.store.ItemsIn("", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("corpus = %d items, want 1 collapsed record", len(items))
	}
	got := items[0]
	if len(got.Attributions) != 2 || !containsFold(got.Attributions, "Wire A") ||
		!containsFold(got.Attributions, "Wire B") {
		t.Errorf("attributions = %v, want both wires", got.Attributions)
	}
	if len(got.Entities) == 0 {
		t.Error("the collapsed record kept no entities")
	}

	// reading wire A again changes nothing
	if n, c, _ := a.processSource(wireA, []rawItem{story("g-a", "https://a.example/1", "A")}, fetched); n != 0 || c != 0 {
		t.Errorf("re-read was not idempotent: new=%d collapsed=%d", n, c)
	}
	items, _ = a.store.ItemsIn("", "", 0)
	if len(items) != 1 {
		t.Errorf("corpus = %d items after a re-read, want 1", len(items))
	}
	if len(items[0].Attributions) != 2 {
		t.Errorf("attributions after re-read = %v", items[0].Attributions)
	}
}

// An item with no title is dropped by processing rather than stored blank.
func TestProcessSourceSkipsUntitledEntries(t *testing.T) {
	a := &App{store: newTestStore(t)}
	src := &Source{ID: "s", Name: "S", Collection: "news"}
	newCount, _, _ := a.processSource(src, []rawItem{{Body: "no title"}, {Title: ""}}, time.Now())
	if newCount != 0 {
		t.Errorf("stored %d untitled items", newCount)
	}
	items, _ := a.store.ItemsIn("", "", 0)
	if len(items) != 0 {
		t.Errorf("corpus = %d, want 0", len(items))
	}
}

// ---------------------------------------------------------------------------
// STX04 — source kinds
// ---------------------------------------------------------------------------

// The README fixes three kinds: feed, page and dataset (imported). The API has
// to say so plainly rather than storing whatever arrives and failing later.
func TestCreateSourceValidatesKindAndURL(t *testing.T) {
	a := &App{store: newTestStore(t)}

	cases := []struct {
		name   string
		body   string
		status int
	}{
		{"no name", `{"kind":"feed","url":"https://x.example/f"}`, http.StatusBadRequest},
		{"unknown kind", `{"name":"X","kind":"rss","url":"https://x.example/f"}`, http.StatusBadRequest},
		{"feed without url", `{"name":"X","kind":"feed"}`, http.StatusBadRequest},
		{"page without url", `{"name":"X","kind":"page"}`, http.StatusBadRequest},
		{"feed with url", `{"name":"X","kind":"feed","url":"https://x.example/f"}`, http.StatusCreated},
		{"page with url", `{"name":"P","kind":"page","url":"https://x.example/p"}`, http.StatusCreated},
		{"kind defaults to feed", `{"name":"Y","url":"https://y.example/f"}`, http.StatusCreated},
		{"dataset is imported, so it needs no url", `{"name":"Imported","kind":"dataset"}`, http.StatusCreated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/stenella/api/sources", strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			a.createSource(rec, req)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.status, rec.Body.String())
			}
			// an error must be readable prose, never a raw panic or a link
			if c.status == http.StatusBadRequest {
				if !strings.Contains(rec.Body.String(), "error") {
					t.Errorf("error body = %s", rec.Body.String())
				}
			}
		})
	}

	sources, err := a.store.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 4 {
		t.Fatalf("stored %d sources, want the 4 accepted ones", len(sources))
	}
	kinds := map[string]string{}
	for _, s := range sources {
		kinds[s.Name] = s.Kind
		if !isSourceKind(s.Kind) {
			t.Errorf("%s stored with unknown kind %q", s.Name, s.Kind)
		}
	}
	if kinds["Y"] != sourceKindFeed {
		t.Errorf("default kind = %q, want feed", kinds["Y"])
	}
	if kinds["Imported"] != sourceKindDataset {
		t.Errorf("dataset kind = %q", kinds["Imported"])
	}
}

// A dataset has no wire to read: collection must count it and leave it alone,
// not mark it failed because an import is not a fetch.
func TestRefreshCountsImportedDatasetsWithoutFailingThem(t *testing.T) {
	fs := pipeFixture(t)
	store := newTestStore(t)
	if err := store.PutSource(&Source{
		Name: "imported-ledger", Kind: sourceKindDataset, Collection: "news",
		Reliability: "B", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSource(&Source{
		Name: "rss", URL: fs.URL + "/rss.xml", Kind: sourceKindFeed,
		Collection: "news", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	a := &App{store: store, client: fs.Client(),
		fetcher: newAggregator(nil, fs.Client(), time.Minute), aggs: map[string]*aggregator{}}

	rep, err := a.refresh(context.Background(), "all")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sources != 2 || rep.Imported != 1 {
		t.Errorf("report = %+v, want 2 sources of which 1 imported", rep)
	}
	if len(rep.Errors) != 0 {
		t.Errorf("a dataset must not produce a collection error: %v", rep.Errors)
	}
	if _, ok := rep.PerSource["imported-ledger"]; ok {
		t.Error("a dataset must not report fetched items")
	}
	if rep.PerSource["rss"] != 3 {
		t.Errorf("rss fetched %d, want 3", rep.PerSource["rss"])
	}

	sources, _ := store.Sources()
	for _, s := range sources {
		switch s.Name {
		case "imported-ledger":
			if s.LastStatus != "imported" {
				t.Errorf("dataset status = %q, want imported", s.LastStatus)
			}
		case "rss":
			if s.LastStatus != "ok" {
				t.Errorf("rss status = %q, want ok", s.LastStatus)
			}
		}
	}

	// fetching the dataset by hand is equally a no-op, not an error
	one, err := a.refreshOne(context.Background(), findSourceID(t, store, "imported-ledger"))
	if err != nil {
		t.Fatal(err)
	}
	if one.Imported != 1 || one.Fetched != 0 {
		t.Errorf("refreshOne(dataset) = %+v", one)
	}
}

func findSourceID(t *testing.T, s *Store, name string) string {
	t.Helper()
	sources, err := s.Sources()
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sources {
		if src.Name == name {
			return src.ID
		}
	}
	t.Fatalf("source %q not found", name)
	return ""
}
