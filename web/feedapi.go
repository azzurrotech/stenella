package web

import (
	"encoding/xml"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
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

// handleCombinedRSS serves the client's combined feed as RSS 2.0 (public).
func (s *Server) handleCombinedRSS(w http.ResponseWriter, r *http.Request) {
	client := r.PathValue("client")
	pg := s.feeds.Combined(client, feed.Query{Page: 1, PageSize: 100})
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
			Description: "Single feed of every source " + client + " subscribes to.",
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
	pg := s.feeds.Combined(client, feed.Query{Page: 1, PageSize: 100})
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(xml.Header))
	entries := make([]atomEntryOut, 0, len(pg.Items))
	for _, it := range pg.Items {
		entries = append(entries, atomEntryOut{
			Title: it.Title, ID: "urn:stenella:" + it.ID,
			Link:    atomLinkOut{Href: it.Link, Rel: "alternate"},
			Updated: it.Updated.Format(time.RFC3339),
			Summary: atomTextOut{Type: "html", Text: it.Summary},
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
	page := intParam(r, "page", 1)
	if page < 1 {
		page = 1
	}
	pg := s.feeds.Combined(client, feed.Query{Page: page, PageSize: 30})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"client": client, "page": pg.Page, "page_size": 30,
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
	q := r.URL.Query().Get("q")
	pg := s.feeds.Combined(client, feed.Query{Page: 1, PageSize: 30, Q: q})
	if pg.Total == 0 {
		s.renderPage(w, r, "feed", feedPageData{
			Title: "No feed yet", Client: client, Items: []feed.Item{},
			RSSURL:  "/s/feed/" + url.PathEscape(client) + "/combined.xml",
			AtomURL: "/s/feed/" + url.PathEscape(client) + "/combined.atom",
			Q:       q,
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
		Q:       q,
	})
}

// order: handleClientFeedPage must come AFTER handleCombinedRSS/Atom in the mux,
// which it does (combined.xml/.atom are exact-suffix patterns).

// ---- share page + JSON ----------------------------------------------------------

func (s *Server) resolveShare(id string) (*links.Share, string, error) {
	client, ok := s.shares.ClientFor(id)
	if !ok {
		return nil, "", errNotFound
	}
	sh, err := s.shares.Get(client, id)
	if err != nil {
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
