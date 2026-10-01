package feed

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The ingest cutoff: an item older than its source's retention window is never
// cached, and one inside it is. The engine's clock is injected so the assertion
// is about the rule rather than about today's date.
func TestIngestRetentionWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()

	// The fixture is dated 23/24 Sep 2026. Put the clock just after it.
	clock := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	root := t.TempDir()
	e, err := New(Options{Root: root, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	// A 7-day window keeps both items; a 1-day window keeps neither.
	src, err := e.AddSourceClassified("acme", "Fixture", srv.URL+"/feed.rss", KindRSS, 60, "", 7, "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Fetch("acme", src, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.Combined("acme", Query{Page: 1, PageSize: 100}).Total; got != 2 {
		t.Fatalf("with a 7-day window the feed holds %d items, want 2", got)
	}

	if err := e.UpdateSource("acme", src.ID, map[string]any{"retention_days": float64(1)}); err != nil {
		t.Fatal(err)
	}
	// Re-fetching under the tighter window replaces the cache. The 1-day cutoff
	// lands at midnight on the 24th, which splits the fixture: the 23 Sep story
	// falls outside it and the 24 Sep one stays.
	if _, err := e.Fetch("acme", src, nil); err != nil {
		t.Fatal(err)
	}
	got := itemTitles(e.Combined("acme", Query{Page: 1, PageSize: 100}).Items)
	if len(got) != 1 || got[0] != "Second story" {
		t.Fatalf("with a 1-day window the feed holds %v, want only Second story", got)
	}
}

// The engine's clock also stamps the fetched/published bookkeeping, so two
// fetches of the same feed under the same clock produce identical cache files.
// A test asserting on a timestamp would otherwise be asserting on a race.
func TestClockIsUsedForFetchStamps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()

	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	e, err := New(Options{Root: root, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	src, err := e.AddSourceClassified("acme", "Fixture", srv.URL+"/feed.rss", KindRSS, 60, "", 30, "public")
	if err != nil {
		t.Fatal(err)
	}
	if src.Added != clock {
		t.Errorf("source Added = %s, want the injected clock %s", src.Added, clock)
	}
	if _, err := e.Fetch("acme", src, nil); err != nil {
		t.Fatal(err)
	}
	stored := e.GetSource("acme", src.ID)
	if stored == nil {
		t.Fatal("GetSource returned nil")
	}
	if !stored.LastFetch.Equal(clock) {
		t.Errorf("LastFetch = %s, want %s", stored.LastFetch, clock)
	}
	for _, it := range e.Combined("acme", Query{Page: 1, PageSize: 100}).Items {
		if !it.Fetched.Equal(clock) {
			t.Errorf("item %q Fetched = %s, want %s", it.ID, it.Fetched, clock)
		}
	}
}
