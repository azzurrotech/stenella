package main

import (
	"path/filepath"
	"testing"
	"time"

	"azzurrotech/pod"
)

// newTestStore returns a store over a fresh pod directory.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	p, err := pod.Open(filepath.Join(t.TempDir(), "pod"))
	if err != nil {
		t.Fatalf("pod.Open: %v", err)
	}
	s, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// --- codec -----------------------------------------------------------------

func TestCodecRoundTripEveryRecordType(t *testing.T) {
	when := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	cases := map[string]any{
		"Source": Source{
			ID: "src-1", Name: "ANSA", URL: "https://example.invalid/rss",
			Kind: "feed", Collection: "news", Reliability: "B", ACL: "public",
			Enabled: true, Every: "15m", Added: when, LastFetch: when,
			LastStatus: "ok", LastCount: 42,
		},
		"Item": Item{
			ID: "item-1", SourceID: "src-1", Source: "ANSA", Collection: "news",
			Title: "Headline", Summary: "Summary", Body: "Body",
			Categories: []string{"cyber", "economy"}, ACL: "public",
			LinkHash: "lh", GUIDHash: "gh", ContentHash: "ch",
			Attributions: []string{"ANSA", "CBC"}, Entities: []string{"person:x"},
			Geo: "45.4215,-75.6972", Published: when, Fetched: when, DupOf: "item-0",
		},
		"Entity": Entity{
			ID: "place:1", Kind: "place", Name: "Ottawa", Count: 3,
			Items: []string{"a", "b", "c"}, FirstSeen: when, LastSeen: when,
		},
		"Link": Link{
			ID: "l1", From: "item-1", To: "item-2", Relation: "supports",
			Origin: "human", Note: "analyst note", Created: when,
		},
		"Comment": Comment{ID: "c1", Item: "item-1", Author: "analyst", Body: "watch this", Created: when},
		"Note": Note{
			ID: "n1", Subject: "item-1", Title: "Note", Body: "body",
			Tags: []string{"insight", "high"}, Created: when, Updated: when,
		},
		"Group": Group{ID: "g1", Name: "Watch", Query: "tag:cyber source:CBC", Tags: []string{"cyber"}, Color: "#0a84ff", Created: when},
		"Rule": Rule{
			ID: "r1", Name: "rule", Text: "text", Severity: "high", Window: "24h",
			Enabled: true, Tags: []string{"cyber"}, Sources: []string{"CBC"},
			Entities: []string{"place:1"}, MatchAll: true, MinItems: 2, MinSources: 2,
			Created: when,
		},
		"Insight": Insight{
			ID: "ins1", Rule: "r1", Title: "title", Text: "text", Severity: "high",
			Items: []string{"a", "b"}, Sources: []string{"CBC"}, Count: 2,
			Status: "dismissed", Reason: "not relevant", NoteID: "n1",
			Created: when, Updated: when,
		},
	}

	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			fields, err := fieldsOf(rec)
			if err != nil {
				t.Fatalf("fieldsOf: %v", err)
			}
			if len(fields) == 0 {
				t.Fatal("fieldsOf produced no fields")
			}
			// the reconstructed value must equal the original
			zero := zeroLike(rec)
			if err := applyFields(fields, zero); err != nil {
				t.Fatalf("applyFields: %v", err)
			}
			assertStructsEqual(t, rec, zero)
		})
	}
}

// zeroLike returns a fresh pointer to the same record type.
func zeroLike(v any) any {
	switch v.(type) {
	case Source:
		return &Source{}
	case Item:
		return &Item{}
	case Entity:
		return &Entity{}
	case Link:
		return &Link{}
	case Comment:
		return &Comment{}
	case Note:
		return &Note{}
	case Group:
		return &Group{}
	case Rule:
		return &Rule{}
	case Insight:
		return &Insight{}
	}
	panic("zeroLike: unhandled type")
}

// assertStructsEqual compares the flattened field maps, which is exactly what
// the store persists — a difference anywhere is a data-loss bug.
func assertStructsEqual(t *testing.T, want, got any) {
	t.Helper()
	w, err := fieldsOf(want)
	if err != nil {
		t.Fatalf("fieldsOf(want): %v", err)
	}
	g, err := fieldsOf(got)
	if err != nil {
		t.Fatalf("fieldsOf(got): %v", err)
	}
	if len(w) != len(g) {
		t.Fatalf("field count %d != %d", len(w), len(g))
	}
	for k, v := range w {
		gv, ok := g[k]
		if !ok {
			t.Errorf("field %q missing after round trip", k)
			continue
		}
		if gv != v {
			t.Errorf("field %q = %q, want %q", k, gv, v)
		}
	}
}

func TestCodecOmitsEmptyOptionalFields(t *testing.T) {
	fields, err := fieldsOf(Item{ID: "x", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"geo", "acl", "summary", "body", "guidHash", "linkHash", "dupOf"} {
		if _, ok := fields[absent]; ok {
			t.Errorf("omitempty field %q should be absent, got %q", absent, fields[absent])
		}
	}
	if fields["title"] != "t" || fields["id"] != "x" {
		t.Errorf("required fields not written: %v", fields)
	}
}

// A field added to a record type later must not break reading an older row:
// absent fields keep their zero value rather than failing the decode.
func TestCodecToleratesOlderRecords(t *testing.T) {
	var it Item
	err := applyFields(map[string]string{"id": "item-old", "title": "old"}, &it)
	if err != nil {
		t.Fatalf("applyFields: %v", err)
	}
	if it.ID != "item-old" || it.Title != "old" {
		t.Errorf("got %+v", it)
	}
	if len(it.Categories) != 0 || it.Geo != "" {
		t.Errorf("unwritten fields must stay zero: %+v", it)
	}
}

// A time written by the codec must read back identically, including UTC.
func TestCodecTimeRoundTrip(t *testing.T) {
	when := time.Date(2026, 10, 15, 14, 30, 0, 0, time.UTC)
	var n Note
	n.Updated = when
	f, err := fieldsOf(n)
	if err != nil {
		t.Fatal(err)
	}
	var back Note
	if err := applyFields(f, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Updated.Equal(when) {
		t.Fatalf("time round trip: got %v want %v", back.Updated, when)
	}
}

// --- provisioning ----------------------------------------------------------

func TestStoreCreatesEveryTableInTheModel(t *testing.T) {
	s := newTestStore(t)
	want := []string{
		tableSources, tableItems, tableEntities, tableLinks,
		tableComments, tableNotes, tableGroups, tableRules, tableInsights,
	}
	tables, err := s.Pod().ListTables()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, name := range tables {
		have[name] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("table %q was not created (have %v)", w, tables)
		}
	}
	if got := s.Tables(); len(got) != 9 {
		t.Errorf("Tables() returned %d, want 9", len(got))
	}
	// provisioning twice is idempotent
	if err := s.ensureTables(); err != nil {
		t.Fatalf("ensureTables again: %v", err)
	}
}

// --- sources ---------------------------------------------------------------

func TestSourceCRUDAndCollections(t *testing.T) {
	s := newTestStore(t)

	src := &Source{Name: "CBC", URL: "https://example.invalid/cbc", Kind: "feed",
		Collection: "news", Reliability: "B", ACL: "public", Enabled: true}
	if err := s.PutSource(src); err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if src.ID == "" || src.Added.IsZero() {
		t.Fatalf("id/added not defaulted: %+v", src)
	}

	other := &Source{Name: "CSE", URL: "https://example.invalid/cse", Kind: "feed",
		Collection: "government", Reliability: "A", Enabled: true}
	if err := s.PutSource(other); err != nil {
		t.Fatal(err)
	}
	off := &Source{Name: "Disabled", URL: "https://example.invalid/x",
		Collection: "news", Enabled: false}
	if err := s.PutSource(off); err != nil {
		t.Fatal(err)
	}

	all, err := s.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d sources, want 3", len(all))
	}

	enabled, err := s.EnabledSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 2 {
		t.Errorf("EnabledSources = %d, want 2 (one is disabled)", len(enabled))
	}

	cols, err := s.Collections()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"all", "government", "news"}
	if len(cols) != len(want) {
		t.Fatalf("collections = %v, want %v", cols, want)
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Errorf("collections[%d] = %q, want %q", i, cols[i], want[i])
		}
	}

	// status update keeps the record and stamps the run
	if err := s.SetSourceStatus(src.ID, "ok", 7); err != nil {
		t.Fatalf("SetSourceStatus: %v", err)
	}
	reloaded, err := s.Source(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LastStatus != "ok" || reloaded.LastCount != 7 || reloaded.LastFetch.IsZero() {
		t.Errorf("status not recorded: %+v", reloaded)
	}

	if err := s.DeleteSource(off.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Sources()
	if len(after) != 2 {
		t.Errorf("after delete = %d, want 2", len(after))
	}
}

// --- items -----------------------------------------------------------------

func TestItemOrderingAndHashLookup(t *testing.T) {
	s := newTestStore(t)

	mk := func(id, src, coll string, published time.Time, guidHash string) *Item {
		it := &Item{ID: id, Source: src, Collection: coll, Title: id,
			Published: published, ContentHash: "ch-" + id, GUIDHash: guidHash}
		if err := s.PutItem(it); err != nil {
			t.Fatalf("PutItem %s: %v", id, err)
		}
		return it
	}
	mk("a", "CBC", "news", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "gh-a")
	mk("b", "CBC", "news", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "gh-b")
	mk("c", "CSE", "government", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), "")

	newest, err := s.Items(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(newest) != 2 || newest[0].ID != "b" || newest[1].ID != "c" {
		t.Fatalf("Items(2) order = %v, want [b c]", itemIDs(newest))
	}

	byColl, err := s.ItemsIn("news", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(byColl) != 2 {
		t.Errorf("ItemsIn(news) = %v, want 2", itemIDs(byColl))
	}

	byCollSrc, err := s.ItemsIn("news", "CBC", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(byCollSrc) != 2 {
		t.Errorf("ItemsIn(news,CBC) = %v, want 2", itemIDs(byCollSrc))
	}

	if it, ok := s.ItemByHash("guidHash", "gh-a"); !ok || it.ID != "a" {
		t.Errorf("ItemByHash(guidHash) = %v %v, want a/true", it, ok)
	}
	if _, ok := s.ItemByHash("guidHash", "gh-missing"); ok {
		t.Error("ItemByHash found a record that was never written")
	}
	if _, ok := s.ItemByHash("guidHash", ""); ok {
		t.Error("empty hash must not match")
	}

	if err := s.DeleteItem("a"); err != nil {
		t.Fatal(err)
	}
	rest, _ := s.Items(0)
	if len(rest) != 2 {
		t.Errorf("after delete = %d, want 2", len(rest))
	}
}

func itemIDs(items []*Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// --- entities --------------------------------------------------------------

// Two sources reporting the same entity must fold into one record with one
// list of items — the graph's whole point.
func TestObserveEntityFoldsOccurrences(t *testing.T) {
	s := newTestStore(t)

	when := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	id1, err := s.observeEntity("place", "ottawa", "item-1", when)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.observeEntity("Place", "OTTAWA", "item-2", when.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("entity id not stable: %q vs %q (kind+name must identify it)", id1, id2)
	}

	// the same entity on the same item must not double count
	if _, err := s.observeEntity("place", "ottawa", "item-1", when); err != nil {
		t.Fatal(err)
	}

	e, err := s.Entity(id1)
	if err != nil {
		t.Fatal(err)
	}
	if e.Count != 2 || len(e.Items) != 2 {
		t.Errorf("count=%d items=%v, want 2", e.Count, e.Items)
	}
	if !e.FirstSeen.Equal(when) || !e.LastSeen.Equal(when.Add(time.Hour)) {
		t.Errorf("first=%v last=%v", e.FirstSeen, e.LastSeen)
	}
	if e.Kind != "place" {
		t.Errorf("kind = %q, want place", e.Kind)
	}

	// entities rank by how often they appear
	mk := func(name string, n int) {
		for i := 0; i < n; i++ {
			if _, err := s.observeEntity("keyword", name, "item-"+name+string(rune('a'+i)), when); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("supply-chain", 3)
	mk("cyber", 5)
	top, err := s.Entities(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) < 3 {
		t.Fatalf("got %d entities, want >=3", len(top))
	}
	if top[0].Count < top[1].Count {
		t.Errorf("Entities not sorted by count: %v", top[0])
	}
}

func TestObserveEntityIgnoresBlankName(t *testing.T) {
	s := newTestStore(t)
	id, err := s.observeEntity("place", "   ", "item-1", time.Now())
	if err != nil || id != "" {
		t.Fatalf("got id=%q err=%v, want empty/no error", id, err)
	}
	ents, _ := s.Entities(0)
	if len(ents) != 0 {
		t.Errorf("blank name created %d entities", len(ents))
	}
}

// --- links, comments, notes, groups, rules, insights ------------------------

func TestAnalysisRecordsCRUD(t *testing.T) {
	s := newTestStore(t)

	// links
	l := &Link{From: "item-1", To: "item-2", Relation: "supports", Origin: "human"}
	if err := s.PutLink(l); err != nil {
		t.Fatal(err)
	}
	if l.ID == "" || l.Created.IsZero() || l.Origin != "human" {
		t.Errorf("link not defaulted: %+v", l)
	}
	_ = s.PutLink(&Link{From: "item-1", To: "item-3", Relation: "related", Origin: "auto"})
	_ = s.PutLink(&Link{From: "item-9", To: "item-1", Relation: "contradicts", Origin: "human"})

	touching, err := s.LinksFor("item-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(touching) != 3 {
		t.Errorf("LinksFor(item-1) = %d, want 3 (both directions)", len(touching))
	}
	all, _ := s.Links()
	if len(all) != 3 {
		t.Errorf("Links = %d, want 3", len(all))
	}
	if err := s.DeleteLink(l.ID); err != nil {
		t.Fatal(err)
	}
	all, _ = s.Links()
	if len(all) != 2 {
		t.Errorf("after delete = %d, want 2", len(all))
	}

	// comments
	c := &Comment{Item: "item-1", Author: "analyst", Body: "watch this"}
	if err := s.PutComment(c); err != nil {
		t.Fatal(err)
	}
	byItem, err := s.CommentsFor("item-1")
	if err != nil || len(byItem) != 1 {
		t.Fatalf("CommentsFor = %v / %v", byItem, err)
	}

	// notes
	n := &Note{Title: "Note", Body: "body", Tags: []string{"insight"}}
	if err := s.PutNote(n); err != nil {
		t.Fatal(err)
	}
	if n.ID == "" || n.Updated.IsZero() {
		t.Errorf("note not defaulted: %+v", n)
	}
	subject := n.ID
	_ = s.PutNote(&Note{Subject: subject, Title: "child", Body: "b"})
	attached, _ := s.NotesFor(subject)
	if len(attached) != 1 {
		t.Errorf("NotesFor = %d, want 1", len(attached))
	}
	if _, err := s.Note(n.ID); err != nil {
		t.Errorf("Note(): %v", err)
	}

	// groups
	g := &Group{Name: "Watch", Query: "tag:cyber"}
	if err := s.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	groups, _ := s.Groups()
	if len(groups) != 1 || groups[0].Name != "Watch" {
		t.Errorf("Groups = %+v", groups)
	}
}

func TestRuleAndInsightLifecycleRecords(t *testing.T) {
	s := newTestStore(t)

	r := &Rule{Name: "multi-source corroboration", Text: "two sources", Severity: "medium"}
	if err := s.PutRule(r); err != nil {
		t.Fatal(err)
	}
	if r.Severity != "medium" || r.Created.IsZero() {
		t.Errorf("rule defaults missing: %+v", r)
	}
	rules, _ := s.Rules()
	if len(rules) != 1 {
		t.Fatalf("Rules = %d, want 1", len(rules))
	}

	ins := &Insight{Rule: r.ID, Title: r.Name, Text: r.Text, Severity: "high",
		Items: []string{"a", "b"}, Sources: []string{"CBC"}, Count: 2}
	if err := s.PutInsight(ins); err != nil {
		t.Fatal(err)
	}
	if ins.Status != "new" || ins.ID == "" || ins.Created.IsZero() {
		t.Errorf("insight defaults missing: %+v", ins)
	}

	// one insight per rule: re-running the rule must refresh, not duplicate
	again := &Insight{Rule: r.ID, Title: r.Name, Text: r.Text, Severity: "high",
		Items: []string{"a", "b", "c"}, Count: 3}
	if existing, ok := s.InsightByRule(r.ID); ok {
		again.ID = existing.ID
		again.Created = existing.Created
	}
	if err := s.PutInsight(again); err != nil {
		t.Fatal(err)
	}
	open, _ := s.Insights("new")
	if len(open) != 1 {
		t.Fatalf("Insights(new) = %d, want 1 (refreshed, not duplicated)", len(open))
	}
	if open[0].Count != 3 || len(open[0].Items) != 3 {
		t.Errorf("citations not refreshed: %+v", open[0])
	}

	// status filtering
	open[0].Status = "dismissed"
	if err := s.PutInsight(open[0]); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Insights("new"); len(got) != 0 {
		t.Errorf("dismissed insight still listed as new: %d", len(got))
	}
	if got, _ := s.Insights("all"); len(got) != 1 {
		t.Errorf("Insights(all) = %d, want 1", len(got))
	}
}

// --- counts ----------------------------------------------------------------

func TestCountsReportsEveryTable(t *testing.T) {
	s := newTestStore(t)
	_ = s.PutSource(&Source{Name: "s", URL: "u", Collection: "news", Enabled: true})
	_ = s.PutItem(&Item{ID: "i", Title: "t", Collection: "news"})
	_ = s.PutRule(&Rule{Name: "r"})

	counts := s.Counts()
	if counts[tableSources] != 1 || counts[tableItems] != 1 || counts[tableRules] != 1 {
		t.Errorf("counts = %v, want 1/1/1 for sources/items/rules", counts)
	}
	if counts[tableEntities] != 0 || counts[tableInsights] != 0 {
		t.Errorf("empty tables should report 0: %v", counts)
	}
	if len(counts) != 9 {
		t.Errorf("counts has %d entries, want 9", len(counts))
	}
}

// --- conditions ------------------------------------------------------------

// The store's query helpers are what search and grouping run on, so their
// semantics are pinned here rather than left to pod.
func TestQueryConditions(t *testing.T) {
	s := newTestStore(t)
	_ = s.PutItem(&Item{ID: "i1", Title: "t", Source: "CBC", Collection: "news", ACL: "public"})
	_ = s.PutItem(&Item{ID: "i2", Title: "t", Source: "CSE", Collection: "government", ACL: "private"})

	got, err := listRecords[Item](s, tableItems, eq("source", "CBC"), nil)
	if err != nil || len(got) != 1 || got[0].ID != "i1" {
		t.Fatalf("eq = %v / %v", got, err)
	}
	got, err = listRecords[Item](s, tableItems,
		or(eq("source", "CBC"), eq("source", "CSE")), nil)
	if err != nil || len(got) != 2 {
		t.Fatalf("or = %d / %v, want 2", len(got), err)
	}
	got, err = listRecords[Item](s, tableItems,
		and(eq("collection", "news"), eq("acl", "public")), nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("and = %d / %v, want 1", len(got), err)
	}
	got, err = listRecords[Item](s, tableItems, like("source", "%SE"), nil)
	if err != nil || len(got) != 1 || got[0].ID != "i2" {
		t.Fatalf("like = %v / %v", got, err)
	}
	if _, ok := findOne[Item](s, tableItems, "source", "nope"); ok {
		t.Error("findOne matched nothing it should have")
	}
}
