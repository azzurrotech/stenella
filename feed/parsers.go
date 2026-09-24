package feed

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

// ----
// RSS 2.0 / RDF
// ----

type rssDoc struct {
	// XMLName is left unconstrained so a single struct decodes both RSS 2.0
	// (<rss>) and RSS 1.0 / RDF (<rdf:RDF>) documents; the local name selects
	// the layout below.
	XMLName xml.Name
	Channel rssCh     `xml:"channel"`
	Items   []rssItem `xml:"item"` // RDF feeds put <item> at document level
}

type rssCh struct {
	Title string    `xml:"title"`
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	Description string   `xml:"description"`
	Guid        string   `xml:"guid"`
	About       string   `xml:"http://www.w3.org/1999/02/22-rdf-syntax-ns# about,attr"` // RDF item URI
	PubDate     string   `xml:"pubDate"`
	Date        string   `xml:"date"`
	Creator     string   `xml:"creator"`
	Author      string   `xml:"author"`
	Content     string   `xml:"encoded"`
	Categories  []string `xml:"category"`
	Updated     string   `xml:"updated"`
}

// ParseRSS parses RSS 2.0 and RSS 1.0 (RDF) feeds.
func ParseRSS(data []byte) ([]Item, error) {
	var doc rssDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	items := doc.Channel.Items
	// RDF (RSS 1.0) documents carry <item> at the document root; some broken
	// RSS 2.0 feeds do too.
	switch doc.XMLName.Local {
	case "RDF", "rdf":
		items = doc.Items
	default:
		if len(items) == 0 {
			items = doc.Items
		}
	}
	out := make([]Item, 0, len(items))
	chTitle := doc.Channel.Title
	for _, it := range items {
		pub := parseAnyTimeMayFail(it.PubDate, it.Date)
		upd := parseAnyTimeMayFail(it.Updated)
		guid := it.Guid
		if guid == "" {
			guid = it.About
		}
		if guid == "" {
			guid = it.Link
		}
		summary := strings.TrimSpace(it.Description)
		out = append(out, Item{
			Title:      strings.TrimSpace(it.Title),
			Link:       strings.TrimSpace(it.Link),
			GUID:       guid,
			Summary:    summary,
			Content:    strings.TrimSpace(it.Content),
			Author:     firstNonEmpty(it.Author, it.Creator),
			Categories: cleanCategories(it.Categories),
			Published:  pub,
			Updated:    upd,
			SourceName: chTitle,
		})
	}
	return out, nil
}

// ----
// Atom
// ----

type atomDoc struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title     string     `xml:"title"`
	ID        string     `xml:"id"`
	Links     []atomLink `xml:"link"`
	Summary   atomText   `xml:"summary"`
	Content   atomText   `xml:"content"`
	Author    atomAuthor `xml:"author"`
	Updated   xmlTime    `xml:"updated"`
	Published xmlTime    `xml:"published"`
	Category  []atomCat  `xml:"category"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomText struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

type atomAuthor struct {
	Name  string `xml:"name"`
	Email string `xml:"email"`
}

type atomCat struct {
	Term string `xml:"term,attr"`
}

// ParseAtom parses an Atom feed.
func ParseAtom(data []byte) ([]Item, error) {
	var doc atomDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		link := ""
		for _, l := range e.Links {
			if l.Rel == "alternate" || l.Rel == "" {
				link = l.Href
				break
			}
		}
		pub := e.Published.Time
		if pub.IsZero() {
			pub = e.Updated.Time
		}
		summary := atomTextValue(e.Summary)
		if summary == "" {
			summary = atomTextValue(e.Content)
			summary = stripHTML(summary)
			if len(summary) > 800 {
				summary = summary[:800] + "…"
			}
		}
		cats := make([]string, 0, len(e.Category))
		for _, c := range e.Category {
			if c.Term != "" {
				cats = append(cats, c.Term)
			}
		}
		author := strings.TrimSpace(e.Author.Name)
		if author == "" {
			author = strings.TrimSpace(e.Author.Email)
		}
		out = append(out, Item{
			Title:      strings.TrimSpace(e.Title),
			Link:       strings.TrimSpace(link),
			GUID:       e.ID,
			Summary:    summary,
			Content:    atomTextValue(e.Content),
			Author:     author,
			Categories: cats,
			Published:  pub,
			Updated:    e.Updated.Time,
			SourceName: doc.Title,
		})
	}
	return out, nil
}

func atomTextValue(t atomText) string {
	switch t.Type {
	case "html", "xhtml":
		return stripHTML(t.Text)
	default:
		return strings.TrimSpace(t.Text)
	}
}

// ----
// JSON feed / generic JSON item lists ("other online data sources")
// ----

type jsonDoc struct {
	Title   string          `json:"title"`
	Items   []jsonItem      `json:"items"`
	Entries []jsonEntry     `json:"entries"`
	Data    json.RawMessage `json:"data"`
}

type jsonItem struct {
	Title       string   `json:"title"`
	Name        string   `json:"name"`
	Link        string   `json:"link"`
	URL         string   `json:"url"`
	ID          string   `json:"id"`
	GUID        string   `json:"guid"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Author      string   `json:"author"`
	Published   string   `json:"published"`
	Date        string   `json:"date"`
	Created     string   `json:"created_at"`
	Updated     string   `json:"updated"`
	Tags        []string `json:"tags"`
	Categories  []string `json:"categories"`
}

type jsonEntry struct {
	Title     string   `json:"title"`
	Link      string   `json:"link"`
	URL       string   `json:"url"`
	ID        string   `json:"id"`
	Published string   `json:"published"`
	Created   string   `json:"created_at"`
	Summary   string   `json:"summary"`
	Author    string   `json:"author"`
	Tags      []string `json:"tags"`
}

// ParseJSONFeed parses either an RFC 8939 JSON Feed or a simple array/object
// of items.
func ParseJSONFeed(data []byte) ([]Item, error) {
	var doc jsonDoc
	if err := json.Unmarshal(data, &doc); err == nil && (len(doc.Items) > 0 || len(doc.Entries) > 0 || doc.Title != "") {
		raw := doc.Items
		if len(raw) == 0 {
			raw = make([]jsonItem, 0, len(doc.Entries))
			for _, e := range doc.Entries {
				raw = append(raw, jsonItem{
					Title: e.Title, Link: firstNonEmpty(e.Link, e.URL), ID: e.ID, GUID: e.ID,
					Published: firstNonEmpty(e.Published, e.Created), Summary: e.Summary,
					Author: e.Author, Categories: e.Tags,
				})
			}
		}
		out := make([]Item, 0, len(raw))
		for _, it := range raw {
			link := firstNonEmpty(it.Link, it.URL)
			guid := firstNonEmpty(it.GUID, it.ID, link)
			pub := parseAnyTimeMayFail(firstNonEmpty(it.Published, it.Date, it.Created, it.Updated))
			summary := strings.TrimSpace(firstNonEmpty(it.Summary, it.Description))
			out = append(out, Item{
				Title:      strings.TrimSpace(firstNonEmpty(it.Title, it.Name)),
				Link:       strings.TrimSpace(link),
				GUID:       guid,
				Summary:    summary,
				Content:    strings.TrimSpace(it.Content),
				Author:     strings.TrimSpace(it.Author),
				Categories: cleanCategories(append(append([]string{}, it.Tags...), it.Categories...)),
				Published:  pub,
				SourceName: doc.Title,
			})
		}
		return out, nil
	}

	// Try a bare array of objects.
	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err == nil {
		out := make([]Item, 0, len(arr))
		for _, obj := range arr {
			str := func(keys ...string) string {
				for _, k := range keys {
					if v, ok := obj[k].(string); ok {
						return v
					}
					if v, ok := obj[k].(float64); ok {
						return f2s(v)
					}
				}
				return ""
			}
			link := str("link", "url", "href")
			guid := str("guid", "id", "key")
			if guid == "" {
				guid = link
			}
			pub := parseAnyTimeMayFail(str("published", "date", "created_at", "published_at", "created", "timestamp"))
			out = append(out, Item{
				Title:     str("title", "name"),
				Link:      link,
				GUID:      guid,
				Summary:   str("summary", "description", "body"),
				Author:    str("author", "creator"),
				Published: pub,
			})
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, errors.New("unsupported json feed shape")
}

func f2s(v float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', -1, 64), "0"), ".")
}

// ----
// OPML (import only)
// ----

// OPMLOutline is one outline in an OPML document.
type OPMLOutline struct {
	Text     string        `xml:"text,attr"`
	Title    string        `xml:"title,attr"`
	Type     string        `xml:"type,attr"`
	XMLURL   string        `xml:"xmlUrl,attr"`
	HTMLURL  string        `xml:"htmlUrl,attr"`
	Outlines []OPMLOutline `xml:"outline"`
}

// ParseOPML extracts subscribable feed URLs (xmlUrl) from an OPML document.
func ParseOPML(data []byte) ([]Source, error) {
	var doc struct {
		Body struct {
			Outlines []OPMLOutline `xml:"outline"`
		} `xml:"body"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var out []Source
	var walk func(outs []OPMLOutline)
	walk = func(outs []OPMLOutline) {
		for _, o := range outs {
			u := strings.TrimSpace(o.XMLURL)
			if u != "" {
				name := strings.TrimSpace(firstNonEmpty(o.Title, o.Text))
				out = append(out, Source{Name: name, URL: u, Kind: GuessKind(u), IntervalMin: defaultIntervalMin})
			}
			walk(o.Outlines)
		}
	}
	walk(doc.Body.Outlines)
	if len(out) == 0 {
		return nil, errors.New("no outlines with xmlUrl found in OPML")
	}
	return out, nil
}

// ----
// helpers
// ----

func parseAnyTimeMayFail(values ...string) time.Time {
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if t, err := parseAnyTime(strings.TrimSpace(v)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func cleanCategories(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// stripHTML removes tags and decodes the common entities for summaries.
func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	out := b.String()
	repl := strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'",
		"&rsquo;", "'", "&ldquo;", `"`, "&rdquo;", `"`, "&hellip;", "…",
	)
	out = repl.Replace(out)
	return strings.Join(strings.Fields(out), " ")
}

// ReadAllLimited is a convenience for body limits.
func ReadAllLimited(r io.Reader, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, n))
}
