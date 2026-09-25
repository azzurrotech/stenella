package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCombinedFeedsIncludeClientPosts(t *testing.T) {
	s, _ := newTestWeb(t)
	if err := s.atp.CreateClient("acme", "Acme", ""); err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := s.atp.CreateTable("acme/posts", []string{"title", "slug", "date", "category", "excerpt", "body"}); err != nil {
		t.Fatalf("create posts table: %v", err)
	}
	posts := []map[string]string{
		{
			"id": "newest", "title": "Newest post", "slug": "newest-post",
			"date": "2026-06-01", "category": "News", "excerpt": "A short summary.",
			"body": "The complete article body.",
		},
		{
			"id": "older", "title": "Older post", "slug": "older-post",
			"date": "2026-05-01", "category": "Opinion", "excerpt": "Older summary.",
		},
	}
	for _, post := range posts {
		if _, err := s.atp.UpsertRecord("acme/posts", post); err != nil {
			t.Fatalf("upsert %s: %v", post["id"], err)
		}
	}

	rss := webReq(t, s.Handler(), http.MethodGet, "/s/feed/acme/combined.xml", "", nil)
	if rss.Code != http.StatusOK {
		t.Fatalf("RSS status = %d: %s", rss.Code, rss.Body.String())
	}
	for _, want := range []string{"Newest post", "Older post", "post.html?slug=newest-post", "A short summary."} {
		if !strings.Contains(rss.Body.String(), want) {
			t.Errorf("RSS does not contain %q: %s", want, rss.Body.String())
		}
	}
	if strings.Index(rss.Body.String(), "Newest post") > strings.Index(rss.Body.String(), "Older post") {
		t.Errorf("posts are not newest-first: %s", rss.Body.String())
	}

	atom := webReq(t, s.Handler(), http.MethodGet, "/s/feed/acme/combined.atom", "", nil)
	if atom.Code != http.StatusOK {
		t.Fatalf("Atom status = %d: %s", atom.Code, atom.Body.String())
	}
	if !strings.Contains(atom.Body.String(), "Newest post") || !strings.Contains(atom.Body.String(), "post.html?slug=newest-post") || !strings.Contains(atom.Body.String(), "complete article body") {
		t.Fatalf("Atom does not include posts: %s", atom.Body.String())
	}

	items := webReq(t, s.Handler(), http.MethodGet, "/s/feed/acme/items", "", nil)
	if items.Code != http.StatusOK {
		t.Fatalf("items status = %d: %s", items.Code, items.Body.String())
	}
	var payload struct {
		Total int `json:"total"`
		Items []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := json.Unmarshal(items.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	if payload.Total != 2 || len(payload.Items) != 2 || payload.Items[0].Title != "Newest post" || payload.Items[0].ID != "post:newest" {
		t.Fatalf("items payload = %+v", payload)
	}
}

func TestCombinedFeedsFilterClientPosts(t *testing.T) {
	s, _ := newTestWeb(t)
	if err := s.atp.CreateClient("acme", "Acme", ""); err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := s.atp.CreateTable("acme/posts", []string{"title", "slug", "date", "category", "excerpt"}); err != nil {
		t.Fatalf("create posts table: %v", err)
	}
	for _, post := range []map[string]string{
		{"id": "one", "title": "Security notice", "slug": "security", "date": "2026-06-01", "category": "Security", "excerpt": "alert"},
		{"id": "two", "title": "Opinion piece", "slug": "opinion", "date": "2026-05-01", "category": "Opinion", "excerpt": "thought"},
	} {
		if _, err := s.atp.UpsertRecord("acme/posts", post); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	rec := webReq(t, s.Handler(), http.MethodGet, "/s/feed/acme/items?q=security&pageSize=30", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered items status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Security notice") || strings.Contains(rec.Body.String(), "Opinion piece") {
		t.Fatalf("query filter did not select only the matching post: %s", rec.Body.String())
	}

	rec = webReq(t, s.Handler(), http.MethodGet, "/s/feed/acme/items?category=Opinion&pageSize=30", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Opinion piece") || strings.Contains(rec.Body.String(), "Security notice") {
		t.Fatalf("category filter did not select only Opinion: %s", rec.Body.String())
	}

	rec = webReq(t, s.Handler(), http.MethodGet, "/s/feed/acme/items?since=2026-05-15&pageSize=30", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Security notice") || strings.Contains(rec.Body.String(), "Opinion piece") {
		t.Fatalf("since filter did not select only the newer post: %s", rec.Body.String())
	}

	// RSS and Atom use the same query contract as the JSON lazy loader.
	for _, suffix := range []string{"combined.xml?q=security", "combined.atom?q=security"} {
		rec = webReq(t, s.Handler(), http.MethodGet, "/s/feed/acme/"+suffix, "", nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Security notice") || strings.Contains(rec.Body.String(), "Opinion piece") {
			t.Fatalf("%s query filter failed: %d %s", suffix, rec.Code, rec.Body.String())
		}
	}
}
