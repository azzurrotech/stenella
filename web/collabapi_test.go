package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"azzurrotech/stenella/atpclient"
	"azzurrotech/stenella/feed"
	"azzurrotech/stenella/links"
)

// seedClientItem creates a client with one cached feed item and returns the item
// id. It goes through the feed engine rather than writing pod rows directly so
// the tests exercise the same path a real fetch does, ciphertext included.
func seedClientItem(t *testing.T, s *Server, client, title string, published time.Time) string {
	t.Helper()
	return seedClientItemRetention(t, s, client, title, published, 30)
}

// seedClientItemRetention is seedClientItem with an explicit retention window.
//
// Retention is enforced twice: once at ingest, where an item older than the
// window is never cached, and again in the hourly sweep, which reclaims items
// that aged out after they were cached. To exercise the second without waiting a
// window, a test seeds with a generous window and then tightens it — the same
// thing an operator does when a client asks for a smaller footprint.
func seedClientItemRetention(t *testing.T, s *Server, client, title string, published time.Time, retentionDays int) string {
	t.Helper()
	if err := s.atp.CreateClient(client, strings.ToUpper(client[:1])+client[1:], ""); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create client %q: %v", client, err)
	}
	if err := s.collab.EnsureSchema(client); err != nil {
		t.Fatalf("collab schema %q: %v", client, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Fixtures</title><link>https://fx.test/</link>` +
			`<item><title>` + title + `</title><link>https://fx.test/1</link>` +
			`<guid>g-` + title + `</guid><pubDate>` + published.UTC().Format(time.RFC1123Z) +
			`</pubDate><description>secret body text for ` + title + `</description>` +
			`</item></channel></rss>`))
	}))
	t.Cleanup(srv.Close)

	// A distinct path per item: a source's id is derived from its URL, so two
	// items fetched from one URL would share a cache and the second fetch would
	// evict the first.
	src, err := s.feeds.AddSourceClassified(client, "Fixture "+title,
		srv.URL+"/feed/"+title+".rss", "rss", 60, "", retentionDays, "private")
	if err != nil {
		t.Fatalf("add source: %v", err)
	}
	fetched, err := s.feeds.Fetch(client, src, nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(fetched.Items) != 1 {
		t.Fatalf("fetched %d items, want 1", len(fetched.Items))
	}
	// Mirror synchronously. The production mirror runs in the background, and a
	// pod row that has not landed yet is invisible to the links store, which
	// validates both endpoints against pod because its junction symlinks the
	// record's XML file.
	s.mirrorToPod(client, fetched)
	return fetched.Items[0].ID
}

// signedIn returns a request carrying a portal session cookie for client, so the
// clientGate passes without going through the login form.
func signedIn(t *testing.T, s *Server, client string) *http.Cookie {
	t.Helper()
	token, _ := s.sess.put("client "+client, client)
	return &http.Cookie{Name: sessionCookie, Value: token}
}

func doJSON(t *testing.T, s *Server, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return webReq(t, s.Handler(), method, path, body, cookies)
}

// doForm posts a urlencoded form the way a browser submitting without JavaScript
// would. webReq always speaks JSON, so the form path needs its own helper or it
// would go untested.
func doForm(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// doAdmin is webReq with an atp admin bearer token, which is how the adminGate
// authenticates a non-browser caller.
func doAdmin(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	tok, err := s.atp.Token()
	if err != nil {
		t.Fatalf("atp admin token: %v", err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-ATP-Token", tok)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// queryAll is the engine query for "every item, every class", used where the
// assertions care about the item set rather than about ACL gating.
func queryAll() feed.Query { return feed.Query{Page: 1, PageSize: 200} }

// tableQueryAll reads a whole pod table.
func tableQueryAll() atpclient.TableQuery { return atpclient.TableQuery{Limit: 1000} }

// The sweeper's own scheduling and bookkeeping are covered here rather than
// through the HTTP surface, because the two properties that matter — a tick that
// arrives early does not sweep, and a sweep already in flight is not re-entered —
// are only observable with control over the clock.
func TestSweeperScheduleAndOverlapGuard(t *testing.T) {
	sw := newSweeper()
	sw.setInterval(time.Hour)

	now := time.Now().UTC()

	// A fresh sweeper has never run, so any tick is due — including one far in
	// the future, because there is no previous run to measure an interval from.
	// This is what makes the first sweep happen on startup rather than an hour
	// after it.
	if !sw.due(now) || !sw.due(now.Add(30*24*time.Hour)) {
		t.Fatal("a sweeper that has never run reported itself not due")
	}

	// begin claims the slot and stamps lastRun, which is what makes the next
	// tick not due for a full interval.
	if !sw.begin(now) {
		t.Fatal("begin on an idle sweeper failed")
	}
	if sw.due(now) {
		t.Fatal("a sweeper that just ran reported itself due")
	}
	if sw.due(now.Add(30 * time.Minute)) {
		t.Fatal("a tick 30m into a 1h interval reported itself due")
	}
	if !sw.due(now.Add(61 * time.Minute)) {
		t.Fatal("a tick past a 1h interval reported itself not due")
	}
	// The overlap guard: a second sweep while one is in flight is skipped rather
	// than allowed to race the first one's writes.
	if sw.begin(now) {
		t.Fatal("begin succeeded while a sweep was already in flight")
	}
	sw.end()
	if !sw.begin(now) {
		t.Fatal("begin failed after the in-flight sweep ended")
	}
	sw.end()
}

// Tombstones are the browser's signal to drop cached plaintext, so they must
// survive the polling window and then expire. A tombstone dropped early would
// leave a decrypted copy on disk forever; one kept forever would grow without
// bound.
func TestTombstoneWindowExpires(t *testing.T) {
	sw := newSweeper()
	now := time.Now().UTC()

	fresh := Tombstone{ItemID: "fresh", Removed: now.Format(time.RFC3339), Reason: "retention"}
	stale := Tombstone{ItemID: "stale", Removed: now.Add(-2 * TombstoneTTL).Format(time.RFC3339), Reason: "retention"}
	sw.addTombstones("acme", []Tombstone{stale, fresh})

	got := sw.Since("acme", time.Time{})
	if len(got) != 1 || got[0].ItemID != "fresh" {
		t.Fatalf("Since returned %+v, want only the in-window tombstone", got)
	}

	// A `since` in the future must return nothing: the caller is asking what
	// changed after a point that has not happened yet.
	if later := sw.Since("acme", now.Add(time.Hour)); len(later) != 0 {
		t.Fatalf("Since(future) = %+v, want empty", later)
	}

	// Tombstones are per client. Another client's removal must not surface here.
	if other := sw.Since("other", time.Time{}); len(other) != 0 {
		t.Fatalf("another client saw %+v", other)
	}
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

// A comment is stored as ciphertext and comes back as ciphertext. That is the
// encrypt-before-submit guarantee seen from the server: nothing in the response
// or in the pod row is the plaintext.
func TestCommentRoundTripIsCiphertext(t *testing.T) {
	s, _ := newTestWeb(t)
	item := seedClientItem(t, s, "acme", "Encrypt me", time.Now().UTC().Add(-time.Hour))
	c := signedIn(t, s, "acme")

	const envelope = "c2FsdA==.aXY=.Y2lwaGVydGV4dA=="
	rec := doJSON(t, s, http.MethodPost,
		"/s/api/portal/items/comments?client=acme",
		`{"item":"`+item+`","body_enc":"`+envelope+`","bytes":9,"author_token":"ada"}`, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create comment = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID      string `json:"id"`
		BodyEnc string `json:"body_enc"`
	}
	decodeInto(t, rec, &created)
	if created.ID == "" || created.BodyEnc != envelope {
		t.Fatalf("created = %+v", created)
	}
	if strings.Contains(rec.Body.String(), "Encrypt me") {
		t.Errorf("the response leaked unrelated plaintext: %s", rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/s/api/portal/items/comments?client=acme&item="+item, "", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("list comments = %d: %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Comments []struct {
			ID          string `json:"id"`
			ItemRef     string `json:"item_ref"`
			BodyEnc     string `json:"body_enc"`
			AuthorToken string `json:"author_token"`
		} `json:"comments"`
	}
	decodeInto(t, rec, &listed)
	if len(listed.Comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(listed.Comments))
	}
	if listed.Comments[0].BodyEnc != envelope {
		t.Errorf("body_enc round trip = %q, want the submitted envelope", listed.Comments[0].BodyEnc)
	}

	// The pod row holds the envelope, not a plaintext body.
	row, err := s.atp.GetRecord("acme/comments", created.ID)
	if err != nil {
		t.Fatalf("read comment row: %v", err)
	}
	if row["body_enc"] != envelope {
		t.Errorf("pod row body_enc = %q", row["body_enc"])
	}
	for _, col := range []string{"body", "text", "plaintext"} {
		if row[col] != "" {
			t.Errorf("pod row has a plaintext %q column: %q", col, row[col])
		}
	}

	rec = doJSON(t, s, http.MethodDelete, "/s/api/portal/items/comments/"+created.ID+"?client=acme", "", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete comment = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/s/api/portal/items/comments?client=acme&item="+item, "", c)
	decodeInto(t, rec, &listed)
	if len(listed.Comments) != 0 {
		t.Errorf("comments survived deletion: %+v", listed.Comments)
	}
}

// A comment must name an item that exists, and must carry a body. Both are
// refusals rather than empty successes, because an orphan comment would be
// invisible to the sweep and would never be cleaned up.
func TestCommentValidation(t *testing.T) {
	s, _ := newTestWeb(t)
	item := seedClientItem(t, s, "acme", "Real item", time.Now().UTC().Add(-time.Hour))
	c := signedIn(t, s, "acme")

	rec := doJSON(t, s, http.MethodPost, "/s/api/portal/items/comments?client=acme",
		`{"item":"rec_does_not_exist","body_enc":"a.b.c","bytes":3}`, c)
	if rec.Code != http.StatusNotFound {
		t.Errorf("comment on a missing item = %d, want 404: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/s/api/portal/items/comments?client=acme",
		`{"item":"`+item+`","body_enc":"","bytes":0}`, c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty comment body = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// The item id is required, not defaulted.
	rec = doJSON(t, s, http.MethodPost, "/s/api/portal/items/comments?client=acme",
		`{"body_enc":"a.b.c","bytes":3}`, c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("comment with no item = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// Pinning is idempotent: a second pin returns the existing pin rather than
// creating a duplicate, so a double click cannot wedge the UI.
func TestPinIsIdempotent(t *testing.T) {
	s, _ := newTestWeb(t)
	item := seedClientItem(t, s, "acme", "Pinnable", time.Now().UTC().Add(-time.Hour))
	c := signedIn(t, s, "acme")

	first := doJSON(t, s, http.MethodPost, "/s/api/portal/items/pin?client=acme", `{"item":"`+item+`"}`, c)
	if first.Code != http.StatusOK {
		t.Fatalf("pin = %d: %s", first.Code, first.Body.String())
	}
	second := doJSON(t, s, http.MethodPost, "/s/api/portal/items/pin?client=acme", `{"item":"`+item+`"}`, c)
	if second.Code != http.StatusOK {
		t.Fatalf("second pin = %d: %s", second.Code, second.Body.String())
	}
	var a, b struct {
		ID      string `json:"id"`
		ItemRef string `json:"item_ref"`
	}
	decodeInto(t, first, &a)
	decodeInto(t, second, &b)
	if a.ID != b.ID {
		t.Errorf("pinning twice created two pins: %q then %q", a.ID, b.ID)
	}

	rec := doJSON(t, s, http.MethodGet, "/s/api/portal/items/pins?client=acme", "", c)
	var listed struct {
		Pins []struct {
			ItemRef string `json:"item_ref"`
		} `json:"pins"`
	}
	decodeInto(t, rec, &listed)
	if len(listed.Pins) != 1 {
		t.Fatalf("pins = %+v, want exactly one", listed.Pins)
	}

	rec = doJSON(t, s, http.MethodDelete, "/s/api/portal/items/pin?client=acme&item="+item, "", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("unpin = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/s/api/portal/items/pins?client=acme", "", c)
	decodeInto(t, rec, &listed)
	if len(listed.Pins) != 0 {
		t.Errorf("pin survived unpin: %+v", listed.Pins)
	}
}

// Item links are the third retention exception, and they are directional: a link
// A→B keeps B alive when A is pinned, but pinning B does not keep A alive.
//
// The graph is the links store's, not a second one. That is asserted rather than
// assumed: the same row the portal created is the row the survival walk reads, so
// there is only ever one graph to keep consistent.
func TestItemLinkSurvivalSet(t *testing.T) {
	s, _ := newTestWeb(t)
	keep := seedClientItem(t, s, "acme", "Keep me", time.Now().UTC().Add(-time.Hour))
	linked := seedClientItem(t, s, "acme", "Linked to me", time.Now().UTC().Add(-time.Hour))
	orphan := seedClientItem(t, s, "acme", "Nobody wants me", time.Now().UTC().Add(-time.Hour))
	c := signedIn(t, s, "acme")

	rec := doJSON(t, s, http.MethodPost, "/s/api/portal/links?client=acme",
		`{"from_id":"`+keep+`","to_kind":"item","to_id":"`+linked+`","relation":"related"}`, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("link = %d: %s", rec.Code, rec.Body.String())
	}

	// One graph: the edges the portal wrote are the ones the walk reads.
	edges, err := s.links.Edges("acme")
	if err != nil {
		t.Fatalf("links.Edges: %v", err)
	}
	if len(edges) != 1 || edges[0].FromID != keep || edges[0].ToID != linked {
		t.Fatalf("edges = %+v", edges)
	}

	// The survival set is seeded from pins and comments, not from links alone: a
	// link between two unexempted items protects nothing, which is the point.
	set, err := s.survivalSet("acme")
	if err != nil {
		t.Fatalf("survivalSet: %v", err)
	}
	if len(set) != 0 {
		t.Errorf("survival set with no pins or comments = %v, want empty", set)
	}

	// Pinning the source makes the whole reachable component survive.
	if _, err := s.collab.PinItem("acme", keep, "test"); err != nil {
		t.Fatalf("PinItem: %v", err)
	}
	set, err = s.survivalSet("acme")
	if err != nil {
		t.Fatalf("survivalSet: %v", err)
	}
	if !set[keep] || !set[linked] {
		t.Errorf("a pinned item did not keep its linked item alive: %v", set)
	}
	if set[orphan] {
		t.Errorf("an unrelated item entered the survival set: %v", set)
	}

	// A URL edge carries no exemption: the sweep does not own what is on the
	// other end, so following it would resurrect items nothing pinned.
	ext := doJSON(t, s, http.MethodPost, "/s/api/portal/links?client=acme",
		`{"from_id":"`+keep+`","to_kind":"url","to_url":"https://elsewhere.test/x"}`, c)
	if ext.Code != http.StatusCreated {
		t.Fatalf("url link = %d: %s", ext.Code, ext.Body.String())
	}
	if set, err = s.survivalSet("acme"); err != nil {
		t.Fatalf("survivalSet: %v", err)
	} else if len(set) != 2 {
		t.Errorf("a url edge changed the survival set: %v", set)
	}

	// Deleting the edge and the pin releases it.
	for _, e := range mustEdges(t, s, "acme") {
		if e.ToKind == "item" {
			if err := s.links.Delete("acme", e.ID); err != nil {
				t.Fatalf("links.Delete: %v", err)
			}
		}
	}
	if err := s.collab.Unpin("acme", keep); err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	set, err = s.survivalSet("acme")
	if err != nil {
		t.Fatalf("survivalSet: %v", err)
	}
	if set[keep] || set[linked] {
		t.Errorf("unpinning left items in the survival set: %v", set)
	}
}

func mustEdges(t *testing.T, s *Server, client string) []links.Link {
	t.Helper()
	out, err := s.links.Edges(client)
	if err != nil {
		t.Fatalf("links.Edges: %v", err)
	}
	return out
}

// The sweep is the end-to-end retention contract: an expired item dies, a
// commented one lives, and the tombstone log lets a browser reconcile.
func TestRetentionSweepAndTombstones(t *testing.T) {
	s, _ := newTestWeb(t)
	// Seed generously so the week-old item survives ingest, then tighten every
	// source to one day so the sweep sees it as expired. The hour-old item stays
	// inside the window.
	dead := seedClientItemRetention(t, s, "acme", "Expire me", time.Now().UTC().Add(-8*24*time.Hour), 3650)
	alive := seedClientItemRetention(t, s, "acme", "Still fresh", time.Now().UTC().Add(-time.Hour), 3650)
	for _, src := range s.feeds.Sources("acme") {
		// retention_days is the patch key (a plain "retention" is silently
		// ignored), clamped to the smallest allowed window.
		if err := s.feeds.UpdateSource("acme", src.ID,
			map[string]any{"retention_days": float64(MinRetentionDays)}); err != nil {
			t.Fatalf("tighten retention: %v", err)
		}
	}
	c := signedIn(t, s, "acme")

	rec := doJSON(t, s, http.MethodPost, "/s/api/portal/items/comments?client=acme",
		`{"item":"`+dead+`","body_enc":"c2FsdA==.aXY=.Y2lwaGVydGV4dA==","bytes":4}`, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("comment: %d %s", rec.Code, rec.Body.String())
	}

	// With the item commented, the sweep must keep it.
	rep := s.SweepClient("acme")
	if rep == nil {
		t.Fatal("sweep returned nil, want a report")
	}
	if rep.KeptCmt != 1 {
		t.Errorf("KeptCommented = %d, want 1", rep.KeptCmt)
	}
	if rep.Removed != 0 {
		t.Errorf("Removed = %d, want 0: a commented item is exempt", rep.Removed)
	}
	if got := s.feeds.Combined("acme", queryAll()); got.Total != 2 {
		t.Errorf("feed total after a keep-only sweep = %d, want 2", got.Total)
	}

	// Remove the comment; now nothing exempts the old item and the sweep kills it.
	comments, err := s.collab.ListComments("acme", dead, 0)
	if err != nil || len(comments) != 1 {
		t.Fatalf("list comments = %v %+v", err, comments)
	}
	if err := s.collab.DeleteComment("acme", comments[0].ID); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}

	rep = s.SweepClient("acme")
	if rep == nil {
		t.Fatal("second sweep returned nil")
	}
	if rep.Removed != 1 || len(rep.Tombstones) != 1 || rep.Tombstones[0].ItemID != dead {
		t.Fatalf("sweep = %+v, want exactly %s removed", rep, dead)
	}
	if page := s.feeds.Combined("acme", queryAll()); page.Total != 1 {
		t.Errorf("feed total after the sweep = %d, want 1", page.Total)
	}
	if it := s.feeds.ItemByID("acme", dead); it != nil {
		t.Error("the swept item is still in the cache")
	}
	if it := s.feeds.ItemByID("acme", alive); it == nil {
		t.Error("the fresh item was swept")
	}
	// The pod row is gone too, not just the cache entry.
	if _, err := s.atp.GetRecord("acme/items", dead); err == nil {
		t.Error("the pod row survived the sweep")
	}

	// A browser that has decrypted the item can learn it is gone.
	rec = doJSON(t, s, http.MethodGet, "/s/api/portal/retention?client=acme", "", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("retention status = %d: %s", rec.Code, rec.Body.String())
	}
	var status struct {
		WindowDays int `json:"window_days"`
		Tombstones []struct {
			ItemID string `json:"item_id"`
			Reason string `json:"reason"`
		} `json:"tombstones"`
	}
	decodeInto(t, rec, &status)
	if len(status.Tombstones) != 1 || status.Tombstones[0].ItemID != dead {
		t.Fatalf("tombstones = %+v, want %s", status.Tombstones, dead)
	}
	if status.Tombstones[0].Reason != "retention" {
		t.Errorf("tombstone reason = %q", status.Tombstones[0].Reason)
	}
	// Asking with a `since` after the sweep returns nothing new.
	now := time.Now().UTC().Format(time.RFC3339)
	rec = doJSON(t, s, http.MethodGet, "/s/api/portal/retention?client=acme&since="+now, "", c)
	decodeInto(t, rec, &status)
	if len(status.Tombstones) != 0 {
		t.Errorf("a future `since` returned %+v", status.Tombstones)
	}
}

// The portal hands its own content passphrase to its own session and to nobody
// else. That is the whole of the encryption boundary in one assertion.
func TestContentKeyIsClientScoped(t *testing.T) {
	s, _ := newTestWeb(t)
	seedClientItem(t, s, "acme", "Acme item", time.Now().UTC())
	seedClientItem(t, s, "other", "Other item", time.Now().UTC())

	acme := signedIn(t, s, "acme")
	other := signedIn(t, s, "other")

	rec := doJSON(t, s, http.MethodGet, "/s/api/portal/vault?client=acme", "", acme)
	if rec.Code != http.StatusOK {
		t.Fatalf("vault = %d: %s", rec.Code, rec.Body.String())
	}
	var key struct {
		Client     string `json:"client"`
		Passphrase string `json:"passphrase"`
	}
	decodeInto(t, rec, &key)
	if key.Client != "acme" || key.Passphrase == "" {
		t.Fatalf("vault payload = %+v", key)
	}
	if !strings.Contains(rec.Body.String(), "AES-256-GCM") {
		t.Errorf("the vault response does not name the algorithm: %s", rec.Body.String())
	}

	// Another client's session must not get acme's key.
	rec = doJSON(t, s, http.MethodGet, "/s/api/portal/vault?client=acme", "", other)
	if rec.Code == http.StatusOK {
		t.Fatalf("another client read acme's key: %s", rec.Body.String())
	}
	// Nor may an anonymous caller.
	rec = doJSON(t, s, http.MethodGet, "/s/api/portal/vault?client=acme", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous vault read = %d, want 401", rec.Code)
	}
	// The two clients have different keys.
	var otherKey struct {
		Passphrase string `json:"passphrase"`
	}
	rec = doJSON(t, s, http.MethodGet, "/s/api/portal/vault?client=other", "", other)
	decodeInto(t, rec, &otherKey)
	if otherKey.Passphrase == key.Passphrase {
		t.Error("two clients share a content key")
	}
}

// Signup provisions everything a portal needs and hands back the two secrets
// exactly once. It accepts a plain form post so the page works without
// JavaScript.
func TestSignupProvisionsClient(t *testing.T) {
	s, _ := newTestWeb(t)

	rec := doForm(t, s, http.MethodPost, "/s/api/signup",
		"id=acme&name=Acme+Inc&notes=created+by+test")
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Client     string `json:"client"`
		Secret     string `json:"secret"`
		Passphrase string `json:"passphrase"`
	}
	decodeInto(t, rec, &out)
	if out.Client != "acme" || out.Secret == "" || out.Passphrase == "" {
		t.Fatalf("signup payload = %+v", out)
	}

	// The client record exists.
	if _, err := s.atp.GetClient("acme"); err != nil {
		t.Errorf("client record missing after signup: %v", err)
	}
	// The portal secret authenticates.
	login := doJSON(t, s, http.MethodPost, "/s/api/client/login",
		`{"client":"acme","secret":"`+out.Secret+`"}`)
	if login.Code != http.StatusOK {
		t.Errorf("the returned portal secret does not sign in: %d %s", login.Code, login.Body.String())
	}
	// The pod schema exists, so the portal does not 404 on an empty workspace.
	for _, table := range []string{"acme/items", "acme/comments", "acme/pins", "acme/item_links"} {
		if _, _, err := s.atp.QueryTable(table, tableQueryAll()); err != nil {
			t.Errorf("table %q missing after signup: %v", table, err)
		}
	}
	// The content key is provisioned, and the vault holds the same passphrase the
	// browser was given.
	stored, err := s.keys.Passphrase("acme")
	if err != nil {
		t.Fatalf("Passphrase after signup: %v", err)
	}
	if stored != out.Passphrase {
		t.Errorf("the key handed to the browser differs from the stored one")
	}
}

func TestSignupRejections(t *testing.T) {
	s, _ := newTestWeb(t)
	doForm(t, s, http.MethodPost, "/s/api/signup", "id=acme&name=Acme")
	// Clear the per-server budget: this test makes more attempts than a single
	// burst allows, and the limiter is doing its job in every one of them.
	s.signup = newSignupLimiter()

	cases := []struct {
		name string
		body string
		want int
	}{
		{"too short", "id=a&name=A", http.StatusBadRequest},
		{"bad characters", "id=ACME%20Corp&name=A", http.StatusBadRequest},
		{"reserved", "id=admin&name=A", http.StatusBadRequest},
		{"leading dash", "id=-acme&name=A", http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := doForm(t, s, http.MethodPost, "/s/api/signup", tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d: %s", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}

	// A taken id is refused without saying so, so ids cannot be enumerated.
	rec := doForm(t, s, http.MethodPost, "/s/api/signup", "id=acme&name=Acme again")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate signup = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	msg := rec.Body.String()
	for _, leak := range []string{"exists", "taken", "already"} {
		if strings.Contains(strings.ToLower(msg), leak) {
			t.Errorf("the duplicate-id error leaks %q: %s", leak, msg)
		}
	}
}

func TestSignupPageRenders(t *testing.T) {
	s, _ := newTestWeb(t)
	rec := doJSON(t, s, http.MethodGet, "/s/signup", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("signup page = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`name="id"`, `name="name"`, `action="/s/api/signup"`, "collaboration.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("signup page is missing %q", want)
		}
	}
}

// The admin usage payload is envelope-only, and every number is a magnitude the
// dashboard can meter rather than a string it has to parse.
func TestAdminUsageDashboard(t *testing.T) {
	s, _ := newTestWeb(t)
	seedClientItem(t, s, "acme", "One", time.Now().UTC().Add(-2*time.Hour))
	seedClientItem(t, s, "other", "Two", time.Now().UTC().Add(-2*time.Hour))

	rec := doAdmin(t, s, http.MethodGet, "/s/api/admin/usage", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("usage = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Clients []struct {
			Client    string  `json:"client"`
			Sources   int     `json:"sources"`
			Items     int     `json:"items"`
			DiskBytes int64   `json:"disk_bytes"`
			DiskGB    float64 `json:"disk_gb"`
		} `json:"clients"`
		TotalDiskBytes int64 `json:"total_disk_bytes"`
	}
	decodeInto(t, rec, &out)
	if len(out.Clients) != 2 {
		t.Fatalf("usage rows = %d, want one per client: %s", len(out.Clients), rec.Body.String())
	}
	seen := map[string]int{}
	for _, c := range out.Clients {
		seen[c.Client] = c.Items
		if c.Sources != 1 {
			t.Errorf("%s sources = %d, want 1", c.Client, c.Sources)
		}
		if c.Items != 1 {
			t.Errorf("%s items = %d, want 1", c.Client, c.Items)
		}
	}
	if seen["acme"] != 1 || seen["other"] != 1 {
		t.Errorf("per-client item counts = %v", seen)
	}
	if out.TotalDiskBytes < 0 {
		t.Errorf("total_disk_bytes = %d", out.TotalDiskBytes)
	}
	// It must not be a public endpoint.
	if rec := doJSON(t, s, http.MethodGet, "/s/api/admin/usage", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous usage read = %d, want 401", rec.Code)
	}
}
