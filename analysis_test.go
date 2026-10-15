package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"azzurrotech/atp"
)

// ---------------------------------------------------------------------------
// a real, in-process platform for handler tests
// ---------------------------------------------------------------------------

// newTestPlatform builds stenella the way main does: a real atp server with the
// platform registered on it. Handler tests therefore exercise the same routing,
// path parameters, body decoding and JSON encoding the live server uses — a
// handler that works here works when it is served.
func newTestPlatform(t *testing.T) (*atp.Server, *App) {
	t.Helper()
	srv, err := atp.New(atp.Options{
		Root:   filepath.Join(t.TempDir(), "root"),
		Addr:   ":0",
		Secret: "test-master-secret-at-least-32-bytes!!",
		Title:  "stenella test",
	})
	if err != nil {
		t.Fatalf("atp.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// register the hosted application exactly as main does, so the console's
	// app/head panels report the same thing in a test as in production
	if err := srv.Apps().Register(atp.App{
		Name:        "stenella",
		Title:       "stenella",
		ShortName:   "stenella",
		Description: "Open-source intelligence platform",
		ThemeColor:  "#0a84ff",
		Search:      "/stenella/?q={searchTerms}",
	}); err != nil {
		t.Fatalf("app register: %v", err)
	}

	app, err := NewApp(srv)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	if err := app.Register(); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return srv, app
}

// call performs one request through the live atp server.
func call(t *testing.T, srv *atp.Server, method, path, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func mustJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, body)
	}
	return m
}

// analyseFixture makes an App over a fresh store, with the seeded configuration
// removed so a test states exactly what exists.
func analyseFixture(t *testing.T) *App {
	t.Helper()
	a := &App{store: newTestStore(t), aggs: map[string]*aggregator{}}
	return a
}

func putItem(t *testing.T, s *Store, id, title, source, collection string, published time.Time, cats []string) *Item {
	t.Helper()
	it := &Item{
		ID: id, Title: title, Summary: "summary of " + title, Source: source,
		Collection: collection, Categories: cats, ACL: "public",
		ContentHash: shortHash(titleKey(title)), Published: published, Fetched: published,
		Attributions: []string{source},
	}
	if err := s.PutItem(it); err != nil {
		t.Fatalf("PutItem %s: %v", id, err)
	}
	return it
}

// ---------------------------------------------------------------------------
// STX05 — the query language
// ---------------------------------------------------------------------------

func TestParseQueryReadsEveryOperator(t *testing.T) {
	q := parseQuery(`cyber tag:critical source:CBC collection:news entity:ottawa acl:public from:2026-01-01 to:2026-03-01`)

	if len(q.Words) != 1 || q.Words[0] != "cyber" {
		t.Errorf("words = %v", q.Words)
	}
	if len(q.Tags) != 1 || q.Tags[0] != "critical" {
		t.Errorf("tags = %v", q.Tags)
	}
	if len(q.Sources) != 1 || q.Sources[0] != "CBC" {
		t.Errorf("sources = %v", q.Sources)
	}
	if q.Collection != "news" || q.ACL != "public" {
		t.Errorf("collection/acl = %q/%q", q.Collection, q.ACL)
	}
	if len(q.Entities) != 1 || q.Entities[0] != "ottawa" {
		t.Errorf("entities = %v", q.Entities)
	}
	if q.From.UTC().Format("2006-01-02") != "2026-01-01" {
		t.Errorf("from = %v", q.From)
	}
	// "to" is inclusive of the whole day named
	if q.To.UTC().Format(time.RFC3339) != "2026-03-01T23:59:59Z" {
		t.Errorf("to = %v", q.To)
	}

	// a quoted value keeps its spaces; an unknown prefix is plain text
	q = parseQuery(`label:"supply chain" nonsense:value plain`)
	if len(q.Tags) != 1 || q.Tags[0] != "supply chain" {
		t.Errorf("quoted label = %v", q.Tags)
	}
	if len(q.Words) != 2 {
		t.Errorf("unknown prefix should stay free text, words = %v", q.Words)
	}
	// a malformed date is dropped rather than silently matching nothing
	if q := parseQuery("from:notadate"); !q.From.IsZero() {
		t.Errorf("malformed date accepted: %v", q.From)
	}
}

func TestQueryMatchesMetadataFields(t *testing.T) {
	when := time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC)
	it := &Item{
		ID: "i1", Title: "Ransomware hits a chip supplier", Summary: "details",
		Source: "ANSA", Attributions: []string{"ANSA", "CBC"}, Collection: "news",
		Categories: []string{"cyber", "supply-chain"}, ACL: "public",
		Published: when, Entities: []string{"place:abc"},
	}

	cases := []struct {
		query string
		want  bool
	}{
		{"", true},
		{"ransomware", true},              // free text over the title
		{"chip supplier", true},           // every word must appear
		{"ransomware missingword", false}, // one word missing is a miss
		{"tag:cyber", true},
		{"tag:health", false},
		{"source:CBC", true},  // matches an attribution, not just the source
		{"source:ANSA", true}, // and the primary source
		{"source:Rome", false},
		{"collection:news", true},
		{"collection:government", false},
		{"acl:public", true},
		{"acl:private", false},
		{"entity:place:abc", true},
		{"entity:place:zzz", false},
		{"from:2026-02-01", true},
		{"from:2026-03-01", false},
		{"to:2026-02-28", true},
		{"to:2026-01-31", false},
		{"tag:cyber collection:news ransomeware", false},
	}
	for _, c := range cases {
		if got := parseQuery(c.query).matches(it); got != c.want {
			t.Errorf("query %q matched=%v, want %v", c.query, got, c.want)
		}
	}

	// an item with no publication time is not silently excluded by a window
	undated := &Item{ID: "i2", Title: "Undated"}
	if !parseQuery("from:2026-01-01 to:2026-12-31").matches(undated) {
		t.Error("a window must not drop an item the wire never dated")
	}
}

// The query language says `entity:ottawa`, but an item stores entity ids. The
// server has to bridge that, or the documented query only works for scripts.
func TestRunQueryResolvesEntityNamesToIDs(t *testing.T) {
	a := analyseFixture(t)
	when := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	id, err := a.store.observeEntity("place", "Ottawa", "item-1", when)
	if err != nil {
		t.Fatal(err)
	}
	hit := putItem(t, a.store, "item-1", "Talks in Ottawa", "CBC", "news", when, nil)
	hit.Entities = []string{id}
	if err := a.store.PutItem(hit); err != nil {
		t.Fatal(err)
	}
	putItem(t, a.store, "item-2", "Talks in Rome", "ANSA", "news", when, nil)

	for _, query := range []string{"entity:ottawa", "entity:Ottawa", "entity:" + id, "entity:place:Ottawa"} {
		got, err := a.runQuery("", query, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "item-1" {
			t.Errorf("query %q returned %d items, want item-1", query, len(got))
		}
	}

	// an entity name that matches no record simply matches nothing
	got, err := a.runQuery("", "entity:atlantis", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("entity:atlantis matched %d items", len(got))
	}
}

func TestRunQueryOrdersNewestFirstAndLimits(t *testing.T) {
	a := analyseFixture(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	putItem(t, a.store, "old", "A story", "S", "news", base, nil)
	putItem(t, a.store, "new", "A story", "S", "news", base.Add(48*time.Hour), nil)
	putItem(t, a.store, "mid", "A story", "S", "news", base.Add(24*time.Hour), nil)

	got, err := a.runQuery("", "story", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
	if got[0].ID != "new" || got[1].ID != "mid" || got[2].ID != "old" {
		t.Errorf("order = %s,%s,%s want new,mid,old", got[0].ID, got[1].ID, got[2].ID)
	}

	limited, err := a.runQuery("", "story", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 || limited[0].ID != "new" {
		t.Errorf("limit = %d items, first %q", len(limited), limited[0].ID)
	}

	// the collection filter narrows the corpus before the query runs
	putItem(t, a.store, "gov", "A story from government", "S", "government", base, nil)
	inNews, _ := a.runQuery("news", "story", 0)
	if len(inNews) != 3 {
		t.Errorf("collection filter returned %d, want 3", len(inNews))
	}
	inGov, _ := a.runQuery("government", "story", 0)
	if len(inGov) != 1 || inGov[0].ID != "gov" {
		t.Errorf("government collection = %v", inGov)
	}
}

// ---------------------------------------------------------------------------
// STX05 — groups are saved queries
// ---------------------------------------------------------------------------

func TestGroupsAreSavedQueriesWithLiveCounts(t *testing.T) {
	a := analyseFixture(t)
	when := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	putItem(t, a.store, "i1", "Ransomware report", "CBC", "news", when, []string{"cyber"})
	putItem(t, a.store, "i2", "Vulnerability report", "CBC", "news", when, []string{"cyber"})
	putItem(t, a.store, "i3", "Harvest report", "ANSA", "news", when, []string{"economy"})

	g := &Group{Name: "Cyber watchlist", Query: "tag:cyber", Tags: []string{"cyber"}, Color: "#0a84ff"}
	if err := a.store.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	if g.ID == "" {
		t.Fatal("group stored without an id")
	}

	groups, err := a.store.Groups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	// a group carries its query and its colour (the saved-query / tag facet)
	if groups[0].Query != "tag:cyber" || groups[0].Color != "#0a84ff" {
		t.Errorf("group lost its definition: %+v", groups[0])
	}

	// the count is live: the same saved query the search bar uses
	matches, err := a.runQuery("", groups[0].Query, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Errorf("group query matched %d, want 2", len(matches))
	}

	// and it follows new material
	putItem(t, a.store, "i4", "Another ransomware report", "ANSA", "news", when, []string{"cyber"})
	matches, _ = a.runQuery("", groups[0].Query, 0)
	if len(matches) != 3 {
		t.Errorf("group count did not follow new items: %d, want 3", len(matches))
	}

	if err := a.store.DeleteGroup(g.ID); err != nil {
		t.Fatal(err)
	}
	if groups, _ := a.store.Groups(); len(groups) != 0 {
		t.Errorf("group not deleted: %d remain", len(groups))
	}
}

// ---------------------------------------------------------------------------
// STX05 — the analysis surface, over the real server
// ---------------------------------------------------------------------------

func TestAnalysisAPISurface(t *testing.T) {
	srv, _ := newTestPlatform(t)

	// two items to analyse; the API accepts analyst material directly
	code, body := call(t, srv, http.MethodPost, "/stenella/api/items",
		`{"title":"Vendor confirms breach","summary":"A ransomware report","collection":"news","categories":["cyber"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create item: %d %s", code, body)
	}
	first := mustJSON(t, body)
	id1, _ := first["id"].(string)
	if id1 == "" {
		t.Fatalf("created item has no id: %s", body)
	}

	code, body = call(t, srv, http.MethodPost, "/stenella/api/items",
		`{"title":"Vendor confirms breach, follow-up","collection":"news","categories":["cyber"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create second item: %d %s", code, body)
	}
	id2, _ := mustJSON(t, body)["id"].(string)

	// --- links -------------------------------------------------------------
	code, body = call(t, srv, http.MethodPost, "/stenella/api/links",
		`{"from":"`+id1+`","to":"`+id2+`","relation":"supports"}`)
	if code != http.StatusCreated {
		t.Fatalf("create link: %d %s", code, body)
	}
	link := mustJSON(t, body)
	linkID, _ := link["id"].(string)
	if link["origin"] != "human" {
		t.Errorf("an analyst edge must be a human assertion: %v", link["origin"])
	}

	// a self-link is rejected
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/links",
		`{"from":"`+id1+`","to":"`+id1+`"}`); code != http.StatusBadRequest {
		t.Errorf("self-link accepted with status %d", code)
	}
	// missing endpoints are rejected with a readable error
	code, body = call(t, srv, http.MethodPost, "/stenella/api/links", `{"from":"`+id1+`"}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "error") {
		t.Errorf("incomplete link accepted: %d %s", code, body)
	}

	code, body = call(t, srv, http.MethodGet, "/stenella/api/links?item="+id1, "")
	if code != http.StatusOK || !strings.Contains(body, linkID) {
		t.Errorf("links for item: %d %s", code, body)
	}

	// --- comments ----------------------------------------------------------
	code, body = call(t, srv, http.MethodPost, "/stenella/api/comments",
		`{"item":"`+id1+`","body":"Check the supplier angle."}`)
	if code != http.StatusCreated {
		t.Fatalf("create comment: %d %s", code, body)
	}
	comment := mustJSON(t, body)
	if comment["author"] != "analyst" {
		t.Errorf("comment author default = %v", comment["author"])
	}
	commentID, _ := comment["id"].(string)
	if code, body := call(t, srv, http.MethodGet, "/stenella/api/comments?item="+id1, ""); code != http.StatusOK ||
		!strings.Contains(body, "supplier angle") {
		t.Errorf("comments for item: %d %s", code, body)
	}
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/comments", `{"item":"`+id1+`"}`); code != http.StatusBadRequest {
		t.Errorf("empty comment accepted")
	}

	// --- notes -------------------------------------------------------------
	code, body = call(t, srv, http.MethodPost, "/stenella/api/notes",
		`{"title":"Supplier angle","body":"See https://evil.example/x and desk@evil.example","tags":["supplier"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create note: %d %s", code, body)
	}
	note := mustJSON(t, body)
	noteID, _ := note["id"].(string)
	// analyst prose is sanitised on the way in, like every other field
	if text, _ := note["body"].(string); containsLink(text) || strings.Contains(text, "@") {
		t.Errorf("note kept a link or address: %q", text)
	}
	if code, body := call(t, srv, http.MethodGet, "/stenella/api/notes/"+noteID, ""); code != http.StatusOK ||
		!strings.Contains(body, "Supplier angle") {
		t.Errorf("get note: %d %s", code, body)
	}
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/notes", `{"body":"no title"}`); code != http.StatusBadRequest {
		t.Errorf("untitled note accepted")
	}
	if code, body := call(t, srv, http.MethodGet, "/stenella/api/notes", ""); code != http.StatusOK ||
		!strings.Contains(body, noteID) {
		t.Errorf("list notes: %d %s", code, body)
	}

	// --- groups ------------------------------------------------------------
	code, body = call(t, srv, http.MethodPost, "/stenella/api/groups",
		`{"name":"Cyber watchlist","query":"tag:cyber","color":"#0a84ff"}`)
	if code != http.StatusCreated {
		t.Fatalf("create group: %d %s", code, body)
	}
	groupID, _ := mustJSON(t, body)["id"].(string)
	code, body = call(t, srv, http.MethodGet, "/stenella/api/groups", "")
	if code != http.StatusOK {
		t.Fatalf("list groups: %d %s", code, body)
	}
	if !strings.Contains(body, "Cyber watchlist") || !strings.Contains(body, `"count"`) {
		t.Errorf("groups listing lost the group or its live count: %s", body)
	}
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/groups", `{"query":"tag:cyber"}`); code != http.StatusBadRequest {
		t.Errorf("unnamed group accepted")
	}

	// --- the item's own page ----------------------------------------------
	code, body = call(t, srv, http.MethodGet, "/stenella/api/items/"+id1, "")
	if code != http.StatusOK {
		t.Fatalf("get item: %d %s", code, body)
	}
	page := mustJSON(t, body)
	for _, key := range []string{"item", "links", "comments", "notes"} {
		if _, ok := page[key]; !ok {
			t.Errorf("item page is missing %q: %s", key, body)
		}
	}

	// --- deletes -----------------------------------------------------------
	for _, path := range []string{
		"/stenella/api/comments/" + commentID,
		"/stenella/api/notes/" + noteID,
		"/stenella/api/groups/" + groupID,
		"/stenella/api/links/" + linkID,
	} {
		if code, body := call(t, srv, http.MethodDelete, path, ""); code != http.StatusOK {
			t.Errorf("delete %s: %d %s", path, code, body)
		}
	}
	if code, body := call(t, srv, http.MethodGet, "/stenella/api/links?item="+id1, ""); code != http.StatusOK ||
		strings.Contains(body, linkID) {
		t.Errorf("link survived deletion: %s", body)
	}
	if code, body := call(t, srv, http.MethodGet, "/stenella/api/items/"+id1, ""); code != http.StatusOK ||
		strings.Contains(body, "supplier angle") {
		t.Errorf("comment survived deletion: %s", body)
	}
}

// A candidate edge is a proposal: accepting it makes it the analyst's
// assertion, rejecting it removes it.
func TestLinkCandidatesAreAcceptedOrRejected(t *testing.T) {
	srv, app := newTestPlatform(t)

	mk := func(title string) string {
		t.Helper()
		code, body := call(t, srv, http.MethodPost, "/stenella/api/items",
			`{"title":"`+title+`","collection":"news"}`)
		if code != http.StatusCreated {
			t.Fatalf("create item: %d %s", code, body)
		}
		id, _ := mustJSON(t, body)["id"].(string)
		return id
	}
	a, b, c := mk("Proposal one"), mk("Proposal two"), mk("Proposal three")

	// two automatic proposals, exactly as the pipeline would write them
	auto1 := &Link{From: a, To: b, Relation: "related", Origin: "auto", Note: "shares an entity"}
	auto2 := &Link{From: a, To: c, Relation: "related", Origin: "auto", Note: "shares an entity"}
	for _, l := range []*Link{auto1, auto2} {
		if err := app.store.PutLink(l); err != nil {
			t.Fatal(err)
		}
	}

	code, body := call(t, srv, http.MethodPost, "/stenella/api/links/"+auto1.ID, `{"action":"accept"}`)
	if code != http.StatusOK {
		t.Fatalf("accept: %d %s", code, body)
	}
	if origin, _ := mustJSON(t, body)["origin"].(string); origin != "human" {
		t.Errorf("accepted link origin = %q, want human", origin)
	}

	code, body = call(t, srv, http.MethodPost, "/stenella/api/links/"+auto2.ID, `{"action":"reject"}`)
	if code != http.StatusOK {
		t.Fatalf("reject: %d %s", code, body)
	}
	links, err := app.store.Links()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ID != auto1.ID || links[0].Origin != "human" {
		t.Errorf("after accept/reject links = %+v", links)
	}

	// an unknown link and an unknown action are both refused
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/links/link-nope", `{"action":"accept"}`); code != http.StatusNotFound {
		t.Errorf("unknown link accepted with %d", code)
	}
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/links/"+auto1.ID, `{"action":"shrug"}`); code != http.StatusBadRequest {
		t.Errorf("unknown link action accepted with %d", code)
	}
}

// The search bar and the saved groups hit the same endpoint, over the live
// server, and the answer is the sanitised item view.
func TestSearchAPISurface(t *testing.T) {
	srv, _ := newTestPlatform(t)

	mk := func(title, collection string, cats []string) {
		t.Helper()
		body := `{"title":"` + title + `","summary":"details about ` + title + `","collection":"` +
			collection + `","categories":["` + strings.Join(cats, `","`) + `"]}`
		if code, resp := call(t, srv, http.MethodPost, "/stenella/api/items", body); code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", title, code, resp)
		}
	}
	mk("Ransomware in the supply chain", "news", []string{"cyber"})
	mk("Election results announced", "government", []string{"election"})
	mk("Vulnerability patched", "news", []string{"cyber"})

	cases := []struct {
		query string
		want  int
	}{
		{"tag:cyber", 2},
		{"collection:government", 1},
		{"ransomware", 1},
		{"vulnerability election", 0}, // both words must appear in one item
		{"", 3},
	}
	for _, c := range cases {
		code, body := call(t, srv, http.MethodGet, "/stenella/api/search?q="+urlEscape(c.query), "")
		if code != http.StatusOK {
			t.Fatalf("search %q: %d %s", c.query, code, body)
		}
		res := mustJSON(t, body)
		got, _ := res["count"].(float64)
		if int(got) != c.want {
			t.Errorf("search %q returned %v hits, want %d (%s)", c.query, res["count"], c.want, body)
		}
		// the search answer is the same link-free item view the views use
		if containsLink(body) {
			t.Errorf("search response carries a link: %s", body)
		}
	}

	// the limit is respected and reported honestly
	code, body := call(t, srv, http.MethodGet, "/stenella/api/search?q=&limit=2", "")
	if code != http.StatusOK {
		t.Fatal(code, body)
	}
	if got, _ := mustJSON(t, body)["count"].(float64); int(got) != 2 {
		t.Errorf("limited search count = %v, want 2", got)
	}
}

func urlEscape(s string) string {
	// only the characters this test's queries use need escaping; a full
	// url.QueryEscape would also encode the ':' that the query language needs
	return strings.NewReplacer(" ", "%20", "#", "%23", "&", "%26").Replace(s)
}

// The README names guid and link as searchable envelope metadata, while the
// model deliberately stores neither. Hashing the query bridges the two: the
// search is real, and nothing is retained.
func TestQueryMatchesGuidAndLinkByHashWithoutStoringThem(t *testing.T) {
	a := analyseFixture(t)
	when := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	rawGUID := "https://cbc.example/news/story-1"
	rawLink := "https://cbc.example/news/story-1?utm_source=newsletter#top"
	it := putItem(t, a.store, "item-hashed", "Hashed identity", "CBC", "news", when, nil)
	it.GUIDHash = shortHash(rawGUID)
	it.LinkHash = shortHash(canonicalLink(rawLink))
	if err := a.store.PutItem(it); err != nil {
		t.Fatal(err)
	}
	other := putItem(t, a.store, "item-other", "Other story", "ANSA", "news", when, nil)
	other.GUIDHash = shortHash("https://ansa.example/2")
	if err := a.store.PutItem(other); err != nil {
		t.Fatal(err)
	}

	// the stored record never carries the value, only its hash
	if it.GUIDHash == rawGUID || it.LinkHash == rawLink || strings.Contains(it.GUIDHash, "cbc") {
		t.Fatalf("a guid or link value was stored in the clear: %+v", it)
	}

	cases := []string{
		"guid:" + rawGUID,                        // exact guid
		`guid:"` + rawGUID + `"`,                 // quoted, so it parses as one token
		"link:" + rawLink,                        // tracking URL finds the record
		"link:" + canonicalLink(rawLink),         // so does the clean form
		"link:https://cbc.example/news/story-1/", // trailing slash canonicalises away
	}
	for _, query := range cases {
		got, err := a.runQuery("", query, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "item-hashed" {
			t.Errorf("query %q returned %d items, want item-hashed", query, len(got))
		}
	}

	// a guid that is not in the corpus matches nothing
	if got, _ := a.runQuery("", "guid:https://nowhere.example/9", 0); len(got) != 0 {
		t.Errorf("unknown guid matched: %v", got)
	}
}

// An operator the model cannot answer is reported rather than silently
// changing what the query means.
func TestUnavailableOperatorsAreReportedNotIgnored(t *testing.T) {
	q := parseQuery("author:jsmith host:cbc.example tag:cyber")
	if len(q.Unavailable) != 2 {
		t.Fatalf("unavailable = %v, want author and host", q.Unavailable)
	}
	// they must not have become free text
	for _, w := range q.Words {
		if strings.Contains(w, "author:") || strings.Contains(w, "host:") {
			t.Errorf("%q was treated as free text", w)
		}
	}

	notes := queryNotes(q)
	if len(notes) != 2 {
		t.Fatalf("notes = %v", notes)
	}
	joined := strings.Join(notes, " ")
	for _, want := range []string{"author", "host"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no note explains %q: %v", want, notes)
		}
	}

	// and the API surfaces them, so the search bar can say why
	srv, _ := newTestPlatform(t)
	if code, body := call(t, srv, http.MethodPost, "/stenella/api/items",
		`{"title":"Something to find","collection":"news","categories":["cyber"]}`); code != http.StatusCreated {
		t.Fatalf("create item: %d %s", code, body)
	}
	code, body := call(t, srv, http.MethodGet, "/stenella/api/search?q=author:jsmith", "")
	if code != http.StatusOK {
		t.Fatalf("search: %d %s", code, body)
	}
	res := mustJSON(t, body)
	notesAny, _ := res["notes"].([]any)
	if len(notesAny) != 1 || !strings.Contains(notesAny[0].(string), "author") {
		t.Errorf("search notes = %v, want an explanation about author", res["notes"])
	}
	// the unusable operator does not filter, so the search still finds material
	if count, _ := res["count"].(float64); count != 1 {
		t.Errorf("count = %v, want the corpus", res["count"])
	}
}
