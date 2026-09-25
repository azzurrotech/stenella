package web

import (
	"encoding/xml"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"azzurrotech/stenella/atpclient"
	"azzurrotech/stenella/feed"
	"azzurrotech/stenella/links"
)

// ---- public combined feeds ----------------------------------------------------

// feedTitle builds the combined-feed title for a client.
func (s *Server) feedTitle(client string) string {
	name := client
	if rec, err := s.atp.GetClient(client); err == nil {
		if m, ok := rec["client"].(map[string]any); ok {
			if n, ok := m["name"].(string); ok && n != "" {
				name = n
			}
		}
	}
	return name + " — combined feed"
}

// postsFeedSource is the synthetic source used for posts published in a
// client's pod table. Keeping it distinct from configured external sources
// lets the public feed include a fresh deployment's content without pretending
// that the posts table is an RSS source.
const postsFeedSource = "posts"

const maxCombinedItems = 10000

// combinedFeed merges configured source caches with the client's public posts
// table. The feed engine intentionally knows only about configured sources;
// the web layer owns the pod bridge, so it applies the same filtering and
// pagination semantics to both kinds of content.
func (s *Server) combinedFeed(client string, q feed.Query) feed.Page {
	all := make([]feed.Item, 0, 64)
	seen := make(map[string]bool)

	// Fetch enough configured items to merge and paginate in one place. The
	// engine already applies Q/Category/Since/Source filtering for this query.
	if q.Source == "" || q.Source != postsFeedSource {
		cacheQuery := q
		cacheQuery.Page = 1
		cacheQuery.PageSize = maxCombinedItems
		for _, item := range s.feeds.Combined(client, cacheQuery).Items {
			if !seen[item.ID] {
				seen[item.ID] = true
				all = append(all, item)
			}
		}
	}

	if q.Source == "" || q.Source == postsFeedSource {
		for _, item := range s.postFeedItems(client) {
			if !postMatches(item, q) || seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			all = append(all, item)
		}
	}

	sort.SliceStable(all, func(i, j int) bool {
		a, b := feedItemTime(all[i]), feedItemTime(all[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return all[i].ID < all[j].ID
	})

	total := len(all)
	pageSize := q.PageSize
	if pageSize <= 0 {
		pageSize = feed.PageSizeDefault()
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	start := (page - 1) * pageSize
	if start < 0 || start > total {
		start = total
	}
	end := start + pageSize
	if end < start || end > total {
		end = total
	}
	return feed.Page{
		Items:   all[start:end],
		Total:   total,
		Page:    page,
		HasMore: end < total,
	}
}

func feedItemTime(item feed.Item) time.Time {
	if !item.Published.IsZero() {
		return item.Published
	}
	if !item.Updated.IsZero() {
		return item.Updated
	}
	return item.Fetched
}

// postFeedItems converts a client's posts table into normalized feed items.
// Missing tables are normal for a new client, so a query error simply yields
// no local items while configured external sources remain usable.
func (s *Server) postFeedItems(client string) []feed.Item {
	const pageSize = 500
	recs := make([]map[string]string, 0)
	for offset := 0; offset < maxCombinedItems; offset += pageSize {
		batch, count, err := s.atp.QueryTable(client+"/posts", atpclient.TableQuery{
			Limit:   pageSize,
			Offset:  offset,
			OrderBy: "date",
			Desc:    true,
		})
		if err != nil {
			return nil
		}
		recs = append(recs, batch...)
		if len(batch) == 0 || offset+len(batch) >= count {
			break
		}
	}

	now := time.Now().UTC()
	out := make([]feed.Item, 0, len(recs))
	for _, rec := range recs {
		id := strings.TrimSpace(rec["id"])
		title := strings.TrimSpace(rec["title"])
		if id == "" || title == "" {
			continue
		}
		slug := strings.TrimSpace(rec["slug"])
		if slug == "" {
			slug = id
		}
		linkPath := "/post.html?slug=" + url.QueryEscape(slug)
		link := linkPath
		if s.baseURL != "" {
			link = s.baseURL + linkPath
		}
		published := parsePostTime(firstPostValue(rec, "date", "published", "created"))
		updated := parsePostTime(firstPostValue(rec, "updated", "date", "published"))
		if updated.IsZero() {
			updated = published
		}
		if published.IsZero() {
			published = now
		}
		categories := postCategories(rec)
		out = append(out, feed.Item{
			ID:         "post:" + id,
			SourceID:   postsFeedSource,
			SourceName: "Posts",
			Title:      title,
			Link:       link,
			GUID:       "post:" + id,
			Summary:    strings.TrimSpace(rec["excerpt"]),
			Content:    strings.TrimSpace(rec["body"]),
			Categories: categories,
			Published:  published,
			Updated:    updated,
			Fetched:    now,
		})
	}
	return out
}

func firstPostValue(rec map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(rec[key]); value != "" {
			return value
		}
	}
	return ""
}

func postCategories(rec map[string]string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 2)
	for _, key := range []string{"categories", "category", "tags"} {
		for _, part := range strings.FieldsFunc(rec[key], func(r rune) bool {
			return r == '|' || r == ',' || r == ';'
		}) {
			category := strings.TrimSpace(part)
			if category == "" || seen[strings.ToLower(category)] {
				continue
			}
			seen[strings.ToLower(category)] = true
			out = append(out, category)
		}
	}
	return out
}

func parsePostTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC1123Z,
		time.RFC1123,
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func postMatches(item feed.Item, q feed.Query) bool {
	if q.Category != "" && !containsPostCategory(item.Categories, q.Category) {
		return false
	}
	if !q.Since.IsZero() && feedItemTime(item).Before(q.Since) {
		return false
	}
	if q.Q == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		item.Title, item.Summary, item.Content, item.Author, strings.Join(item.Categories, " "),
	}, "\n"))
	return strings.Contains(haystack, strings.ToLower(q.Q))
}

func containsPostCategory(categories []string, want string) bool {
	for _, category := range categories {
		if strings.EqualFold(strings.TrimSpace(category), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

type rssChannelOut struct {
	Title       string       `xml:"title"`
	Link        string       `xml:"link"`
	Description string       `xml:"description"`
	LastBuild   string       `xml:"lastBuildDate"`
	Items       []rssItemOut `xml:"item"`
}

type rssItemOut struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	Description string   `xml:"description"`
	Author      string   `xml:"author,omitempty"`
	PubDate     string   `xml:"pubDate"`
	Source      string   `xml:"source,omitempty"`
	Categories  []string `xml:"category,omitempty"`
}

type atomEntryOut struct {
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Link    atomLinkOut `xml:"link"`
	Updated string      `xml:"updated"`
	Summary atomTextOut `xml:"summary"`
	Content atomTextOut `xml:"content,omitempty"`
}

type atomLinkOut struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomTextOut struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// publicFeedQuery builds the query used by the public feed endpoints. Keep all
// filtering in one place so the human page, JSON lazy loader, RSS, and Atom
// views cannot drift apart. The page size is bounded even when a caller sends
// an arbitrary query parameter.
func publicFeedQuery(r *http.Request, page, defaultSize int) feed.Query {
	if page < 1 {
		page = 1
	}
	if defaultSize < 1 {
		defaultSize = feed.PageSizeDefault()
	}
	pageSize := intParam(r, "pageSize", defaultSize)
	if pageSize < 1 {
		pageSize = defaultSize
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return feed.Query{
		Page:     page,
		PageSize: pageSize,
		Q:        strings.TrimSpace(r.URL.Query().Get("q")),
		Source:   strings.TrimSpace(r.URL.Query().Get("source")),
		Category: strings.TrimSpace(r.URL.Query().Get("category")),
		Since:    parsePostTime(strings.TrimSpace(r.URL.Query().Get("since"))),
	}
}

// handleCombinedRSS serves the client's combined feed as RSS 2.0 (public).
func (s *Server) handleCombinedRSS(w http.ResponseWriter, r *http.Request) {
	client := r.PathValue("client")
	if !s.clientExists(client) {
		s.writeErr(w, http.StatusNotFound, "client not found")
		return
	}
	pg := s.combinedFeed(client, publicFeedQuery(r, 1, 100))
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(xml.Header))
	out := rssItemOuts(pg.Items)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name      `xml:"rss"`
		Version string        `xml:"version,attr"`
		Channel rssChannelOut `xml:"channel"`
	}{
		Version: "2.0",
		Channel: rssChannelOut{
			Title:       s.feedTitle(client),
			Link:        s.baseURL + "/s/feed/" + url.PathEscape(client),
			Description: "Single feed of every source " + client + " subscribes to, plus posts published in its pod table.",
			LastBuild:   time.Now().UTC().Format(time.RFC1123Z),
			Items:       out,
		},
	})
}

func rssItemOuts(items []feed.Item) []rssItemOut {
	out := make([]rssItemOut, 0, len(items))
	for _, it := range items {
		pub := it.Published
		if pub.IsZero() {
			pub = it.Fetched
		}
		out = append(out, rssItemOut{
			Title: it.Title, Link: it.Link, GUID: it.ID, Description: it.Summary,
			Author: it.Author, PubDate: pub.Format(time.RFC1123Z), Source: it.SourceName, Categories: it.Categories,
		})
	}
	if out == nil {
		out = []rssItemOut{}
	}
	return out
}

// handleCombinedAtom serves the combined feed as Atom 1.0 (public).
func (s *Server) handleCombinedAtom(w http.ResponseWriter, r *http.Request) {
	client := r.PathValue("client")
	if !s.clientExists(client) {
		s.writeErr(w, http.StatusNotFound, "client not found")
		return
	}
	pg := s.combinedFeed(client, publicFeedQuery(r, 1, 100))
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(xml.Header))
	entries := make([]atomEntryOut, 0, len(pg.Items))
	for _, it := range pg.Items {
		entries = append(entries, atomEntryOut{
			Title: it.Title, ID: "urn:stenella:" + it.ID,
			Link:    atomLinkOut{Href: it.Link, Rel: "alternate"},
			Updated: it.Updated.Format(time.RFC3339),
			Summary: atomTextOut{Type: "text", Text: it.Summary},
			Content: atomTextOut{Type: "text", Text: it.Content},
		})
	}
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name       `xml:"feed"`
		Xmlns   string         `xml:"xmlns,attr"`
		Title   string         `xml:"title"`
		ID      string         `xml:"id"`
		Updated string         `xml:"updated"`
		Link    atomLinkOut    `xml:"link"`
		Entries []atomEntryOut `xml:"entry"`
	}{
		Xmlns:   "http://www.w3.org/2005/Atom",
		Title:   s.feedTitle(client),
		ID:      s.baseURL + "/s/feed/" + url.PathEscape(client),
		Updated: time.Now().UTC().Format(time.RFC3339),
		Link:    atomLinkOut{Href: s.baseURL + "/s/feed/" + url.PathEscape(client) + "/combined.atom", Rel: "self"},
		Entries: entries,
	})
}

// handleFeedItemsPublic serves more combined-feed pages as JSON for the
// public feed page's lazy loader (no auth — feeds are meant to be public).
func (s *Server) handleFeedItemsPublic(w http.ResponseWriter, r *http.Request) {
	client := r.PathValue("client")
	if !s.clientExists(client) {
		s.writeErr(w, http.StatusNotFound, "client not found")
		return
	}
	q := publicFeedQuery(r, intParam(r, "page", 1), 30)
	pg := s.combinedFeed(client, q)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"client": client, "page": pg.Page, "page_size": q.PageSize,
		"total": pg.Total, "has_more": pg.HasMore, "items": pg.Items,
	})
}

// ---- human feed page -----------------------------------------------------------

type feedPageData struct {
	Title   string
	Client  string
	Items   []feed.Item
	RSSURL  string
	AtomURL string
	Total   int
	HasMore bool
	Q       string
}

func (s *Server) handleClientFeedPage(w http.ResponseWriter, r *http.Request) {
	client := r.PathValue("client")
	if !s.clientExists(client) {
		s.writeErr(w, http.StatusNotFound, "client not found")
		return
	}
	query := publicFeedQuery(r, 1, 30)
	pg := s.combinedFeed(client, query)
	if pg.Total == 0 {
		s.renderPage(w, r, "feed", feedPageData{
			Title: "No feed yet", Client: client, Items: []feed.Item{},
			RSSURL:  "/s/feed/" + url.PathEscape(client) + "/combined.xml",
			AtomURL: "/s/feed/" + url.PathEscape(client) + "/combined.atom",
			Q:       query.Q,
		})
		return
	}
	s.renderPage(w, r, "feed", feedPageData{
		Title:   s.feedTitle(client),
		Client:  client,
		Items:   pg.Items,
		RSSURL:  "/s/feed/" + url.PathEscape(client) + "/combined.xml",
		AtomURL: "/s/feed/" + url.PathEscape(client) + "/combined.atom",
		Total:   pg.Total,
		HasMore: pg.HasMore,
		Q:       query.Q,
	})
}

// order: handleClientFeedPage must come AFTER handleCombinedRSS/Atom in the mux,
// which it does (combined.xml/.atom are exact-suffix patterns).

// ---- share page + JSON ----------------------------------------------------------

func (s *Server) resolveShare(id string) (*links.Share, string, error) {
	client, ok := s.shares.ClientFor(id)
	if !ok || !s.clientExists(client) {
		return nil, "", errNotFound
	}
	sh, err := s.shares.Get(client, id)
	if err != nil {
		return nil, "", errNotFound
	}
	if (sh.Kind == links.ShareItem || sh.Kind == links.ShareLink || sh.Kind == links.ShareTable) &&
		(!validPublicTablePath(sh.Target) || strings.Contains(sh.Target, "/")) {
		return nil, "", errNotFound
	}
	return sh, client, nil
}

var errNotFound = fmt.Errorf("not found")

func (s *Server) handleShareJSON(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sh, client, err := s.resolveShare(id)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, "share not found")
		return
	}
	if !sh.Valid(r.URL.Query().Get("t")) {
		s.writeErr(w, http.StatusForbidden, "invalid or expired share token")
		return
	}
	base := map[string]any{
		"kind": sh.Kind, "title": sh.Title, "created": sh.Created, "expires": sh.Expires,
	}
	switch sh.Kind {
	case links.ShareItem:
		rec, err := s.atp.GetRecord(links.ItemsTable(client), sh.Target)
		if err != nil {
			s.writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		base["item"] = rec
	case links.ShareLink:
		ln, err := s.links.Get(client, sh.Target)
		if err != nil {
			s.writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		base["link"] = ln
	case links.ShareTable:
		recs, count, err := s.atp.QueryTable(client+"/"+sh.Target, atpclient.TableQuery{Limit: 500})
		if err != nil {
			s.writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		base["table"] = sh.Target
		base["records"] = recs
		base["count"] = count
	}
	s.writeJSON(w, http.StatusOK, base)
}

// handleSharePage renders a human-readable share view.
func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sh, client, err := s.resolveShare(id)
	if err != nil {
		http.Error(w, "share not found", http.StatusNotFound)
		return
	}
	if !sh.Valid(r.URL.Query().Get("t")) {
		http.Error(w, "invalid or expired share", http.StatusForbidden)
		return
	}
	switch sh.Kind {
	case links.ShareItem:
		rec, err := s.atp.GetRecord(links.ItemsTable(client), sh.Target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		item := struct {
			Title, Link, Summary, Content, Author, Published, Source string
		}{
			Title: rec["title"], Link: rec["link"], Summary: rec["summary"],
			Content: rec["content"], Author: rec["author"], Published: rec["published"],
			Source: rec["source_name"],
		}
		s.renderPage(w, r, "share", map[string]any{
			"Title": "Shared item", "Kind": "item", "Client": client,
			"Item": item, "ShareTitle": sh.Title, "Expires": sh.Expires,
		})
	case links.ShareLink:
		ln, err := s.links.Get(client, sh.Target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		from, _ := s.atp.GetRecord(links.ItemsTable(client), ln.FromID)
		to := map[string]string{}
		if ln.ToKind == links.ToURL {
			to["title"], to["link"] = ln.ToURL, ln.ToURL
		} else {
			to, _ = s.atp.GetRecord(links.ItemsTable(client), ln.ToID)
		}
		s.renderPage(w, r, "share", map[string]any{
			"Title": "Shared link", "Kind": "link", "Client": client,
			"From": from, "To": to, "Relation": ln.Relation, "Label": ln.Label,
			"ShareTitle": sh.Title, "Expires": sh.Expires,
		})
	case links.ShareTable:
		jsonURL := s.baseURL + "/s/api/x/" + id + "?t=" + url.QueryEscape(sh.Token)
		_, count, err := s.atp.QueryTable(client+"/"+sh.Target, atpclient.TableQuery{Limit: 1})
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		s.renderPage(w, r, "share", map[string]any{
			"Title": "Shared database", "Kind": "table", "Client": client,
			"Table": sh.Target, "JSONURL": template.JSStr(jsonURL), "ShareTitle": sh.Title,
			"Expires": sh.Expires, "Count": count,
		})
	}
}
