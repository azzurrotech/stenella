// rss.go — stenella's collection layer.
//
// It compiles several public RSS/Atom sources into ONE feed and serves that
// feed from a single shareable URL:
//
//	GET /stenella/feeds/all.xml        the combined feed (RSS 2.0)
//	GET /stenella/api/aggregate        the same items as JSON, for the platform
//
// The endpoint is shareable by design: it needs no credentials, sets no
// cookies and carries no per-user state (atp's access model: no added security
// for hosted resources). What makes it safe to hand to anybody is the
// sanitiser: the shared feed contains NO LINKS and NO PRIVATE ELEMENTS.
//
//	removed links      <link>, <guid> URLs, <enclosure url>, <comments>,
//	                   <atom:link rel=self>, Atom <id> (a URL), <docs>,
//	                   <cloud>, and any http(s)/ftp/mailto URL, "www." or bare
//	                   domain appearing inside the text of a title/summary.
//	removed private    <author>, <dc:creator>, <managingEditor>, <webMaster>,
//	                   e-mail addresses, social @handles, <copyright>,
//	                   source-supplied HTML and scripts, tracking identifiers.
//
// Provenance survives as plain text only: each item carries the source's NAME
// in <category>. Nothing in the output can be followed, and nothing in it
// identifies a person or reveals the source's own feed URL.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// sources
// ---------------------------------------------------------------------------

// rssSource is one configured origin. The URL is used server-side only and is
// never echoed into the shared feed.
type rssSource struct {
	Name string
	URL  string
}

// defaultSources is the source list the README advertises: RadioItalia (RAI),
// ANSA, CBC, the Communications Security Establishment and the Government of
// Canada. Every URL below was verified live (see .tbc/evidence/STE05.log).
//
// CSE and the Government of Canada publish their newsrooms as Atom through the
// API that backs canada.ca's "Subscribe to emails and RSS feeds" page
// (https://www.canada.ca/en/news/subscribe-emails.html): dept=communicationssecurity
// is CSE, and no dept filter is the national news feed.
func defaultSources() []rssSource {
	return []rssSource{
		{
			Name: "RadioItalia (RAI)",
			URL:  "https://www.radioitalia.it/rss.xml",
		},
		{
			Name: "ANSA",
			URL:  "https://www.ansa.it/sito/ansait_rss.xml",
		},
		{
			Name: "CBC",
			URL:  "https://www.cbc.ca/webfeed/rss/rss-topstories",
		},
		{
			Name: "Communications Security Establishment",
			URL: "https://api.io.canada.ca/io-server/gc/news/en/v2" +
				"?dept=communicationssecurity&sort=publishedDate&orderBy=desc" +
				"&publishedDate%3E=2021-07-23&pick=50&format=atom" +
				"&atomtitle=Communications%20Security%20Establishment",
		},
		{
			Name: "Government of Canada",
			URL: "https://api.io.canada.ca/io-server/gc/news/en/v2" +
				"?sort=publishedDate&orderBy=desc&publishedDate%3E=2021-10-25" +
				"&pick=100&format=atom&atomtitle=National%20News",
		},
	}
}

// sourcesFromEnv reads STELLENA_RSS_SOURCES, a comma-separated list of
// "Name=URL" pairs. The first '=' separates the name from the URL, so query
// strings in the URL are preserved.
func sourcesFromEnv(v string) ([]rssSource, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	var out []rssSource
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, url, ok := strings.Cut(part, "=")
		name, url = strings.TrimSpace(name), strings.TrimSpace(url)
		if !ok || name == "" || url == "" || !strings.HasPrefix(url, "http") {
			continue
		}
		out = append(out, rssSource{Name: name, URL: url})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// rssSources returns the configured sources, honouring the environment.
func rssSources() []rssSource {
	if s, ok := sourcesFromEnv(os.Getenv("STELLENA_RSS_SOURCES")); ok {
		return s
	}
	return defaultSources()
}

// ---------------------------------------------------------------------------
// the shared item
// ---------------------------------------------------------------------------

// Article is one normalised, sanitised item. It deliberately has no URL field:
// the shared feed must not be able to carry a link.
//
// The three hash fields carry the README's de-duplication identity — guid,
// then canonical link, then a content hash — in one-way form, so a story can be
// recognised across sources without the link or guid ever being stored. They are
// marked json:"-" so they cannot reach the feed or the JSON API.
type Article struct {
	Source      string    `json:"source"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary,omitempty"`
	Published   time.Time `json:"published"`
	Categories  []string  `json:"categories,omitempty"`
	LinkHash    string    `json:"-"`
	GUIDHash    string    `json:"-"`
	ContentHash string    `json:"-"`
}

// ---------------------------------------------------------------------------
// sanitising
// ---------------------------------------------------------------------------

const (
	maxSummary = 480
	maxTitle   = 220
	// userAgent is deliberately a bare product/version token.
	//
	// CBC's WAF drops requests whose User-Agent carries anything richer: a
	// contact URL or a multi-word comment makes www.cbc.ca hang until the
	// response-header timeout, while "stenella/1.0" alone is served in ~70ms
	// (measured, 3/3 rounds each way). Contact details therefore live in the
	// README instead of the User-Agent.
	userAgent = "stenella/1.0"
)

var (
	scriptRe = regexp.MustCompile(`(?is)<script\b.*?</script\s*>`)
	styleRe  = regexp.MustCompile(`(?is)<style\b.*?</style\s*>`)
	comment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	tagRe    = regexp.MustCompile(`(?s)</?[a-zA-Z][^>]*>`)
	emailRe  = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	handleRe = regexp.MustCompile(`(?:^|\s)@[A-Za-z0-9_]{2,}`)
	schemeRe = regexp.MustCompile(`(?i)\b(?:https?|ftp|ftps|mailto|tel|news|irc|ssh|file):[^\s<>"'\)\]]*`)
	wwwRe    = regexp.MustCompile(`(?i)\bwww\.[^\s<>"'\)\]]*`)
	legacyRe = regexp.MustCompile(`(?i)\bhttps?://\S*`)
	domainRe = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9.\-]*\.(?:com|org|net|ca|it|gov|gc|io|edu|info|biz|uk|fr|de|eu|ch|au|jp|ru|tv|news|mil|int|dev|app)\b(?:/[^\s<>"'\)\]]*)?`)
	spaceRe  = regexp.MustCompile(`\s+`)
	noiseRe  = regexp.MustCompile(`\(\s*\)|\[\s*\]|\{\s*\}`)
)

// xmlSafe drops bytes and control characters that are not legal in XML 1.0.
func xmlSafe(s string) string {
	s = strings.ToValidUTF8(s, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case r < 0x20:
			return -1
		case r == 0xFFFE, r == 0xFFFF:
			return -1
		}
		return r
	}, s)
}

// sanitizeText turns arbitrary source markup into plain, link-free, private-
// free prose. It is idempotent: running it twice changes nothing.
func sanitizeText(s string, max int) string {
	if s == "" {
		return ""
	}
	s = xmlSafe(s)
	s = html.UnescapeString(s) // feeds hand us escaped markup (&lt;span&gt;…)
	s = scriptRe.ReplaceAllString(s, " ")
	s = styleRe.ReplaceAllString(s, " ")
	s = comment.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = emailRe.ReplaceAllString(s, " ")
	s = handleRe.ReplaceAllString(s, " ")
	s = schemeRe.ReplaceAllString(s, " ")
	s = wwwRe.ReplaceAllString(s, " ")
	s = legacyRe.ReplaceAllString(s, " ")
	s = domainRe.ReplaceAllString(s, " ")
	s = noiseRe.ReplaceAllString(s, "")
	s = spaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "-–—•|·:;,()[]{}<>«»\"' \t")
	s = spaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if max > 0 && utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max])) + "..."
	}
	return s
}

// containsLink reports whether s still carries anything a reader could follow.
// It is the invariant the tests assert and the live smoke re-checks.
func containsLink(s string) bool {
	return schemeRe.MatchString(s) || wwwRe.MatchString(s) ||
		domainRe.MatchString(s) || emailRe.MatchString(s) ||
		strings.Contains(strings.ToLower(s), "href=")
}

// ---------------------------------------------------------------------------
// parsing: RSS 2.0, RSS 1.0 (RDF) and Atom
// ---------------------------------------------------------------------------

type rssItem struct {
	Title       string   `xml:"title"`
	Description string   `xml:"description"`
	Content     string   `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
	PubDate     string   `xml:"pubDate"`
	Date        string   `xml:"http://purl.org/dc/elements/1.1/ date"`
	GUID        string   `xml:"guid"`
	Link        string   `xml:"link"`
	About       string   `xml:"about,attr"` // RSS 1.0 / RDF identity
	Categories  []string `xml:"category"`
}

type rssDoc struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rdfDoc struct {
	XMLName xml.Name  `xml:"RDF"`
	Items   []rssItem `xml:"item"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomEntry struct {
	Title      string         `xml:"title"`
	Summary    string         `xml:"summary"`
	Content    string         `xml:"content"`
	Updated    string         `xml:"updated"`
	Published  string         `xml:"published"`
	ID         string         `xml:"id"`
	Links      []atomLink     `xml:"link"`
	Categories []atomCategory `xml:"category"`
}

type atomDoc struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type rawItem struct {
	Title      string
	Body       string
	When       time.Time
	GUID       string   // rss <guid>, rdf rdf:about / dc:identifier, atom <id>
	Link       string   // rss/rdf <link>, atom rel="alternate"
	Categories []string // feed categories / atom terms
}

// atomAlternate picks the entry's alternate link, falling back to the first
// link of any relation when a feed omits rel.
func atomAlternate(links []atomLink) string {
	for _, l := range links {
		if strings.EqualFold(strings.TrimSpace(l.Rel), "alternate") {
			return l.Href
		}
	}
	for _, l := range links {
		if strings.TrimSpace(l.Href) != "" {
			return l.Href
		}
	}
	return ""
}

func atomTerms(cats []atomCategory) []string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		if t := strings.TrimSpace(c.Term); t != "" {
			out = append(out, t)
		}
	}
	return out
}

var dateLayouts = []string{
	time.RFC3339,
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04 MST",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseFeed accepts RSS 2.0, RSS 1.0/RDF, Atom, JSON Feed and HTML pages, and
// returns the raw items. Parsing is baseline Go: encoding/xml for the XML
// formats, encoding/json for JSON Feed and stdlib regexp for a page, with no
// third-party parser. The page reader is only tried once every feed format has
// already been rejected.
func parseFeed(data []byte) ([]rawItem, error) {
	trimmed := bytes.TrimSpace(data)

	// JSON Feed is the only accepted format whose first byte is '{'.
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var jf jsonFeed
		if err := json.Unmarshal(trimmed, &jf); err == nil && jf.Items != nil {
			out := make([]rawItem, 0, len(jf.Items))
			for _, it := range jf.Items {
				when, _ := parseDate(it.DatePublished)
				if when.IsZero() {
					when, _ = parseDate(it.DateModified)
				}
				body := it.ContentText
				if strings.TrimSpace(body) == "" {
					body = it.Summary
				}
				if strings.TrimSpace(body) == "" {
					body = it.ContentHTML
				}
				out = append(out, rawItem{
					Title:      it.Title,
					Body:       body,
					When:       when,
					GUID:       it.ID,
					Link:       it.URL,
					Categories: it.Tags,
				})
			}
			return out, nil
		}
	}

	var rss rssDoc
	if err := xml.Unmarshal(data, &rss); err == nil && rss.XMLName.Local == "rss" {
		out := make([]rawItem, 0, len(rss.Channel.Items))
		for _, it := range rss.Channel.Items {
			when, _ := parseDate(it.PubDate)
			if when.IsZero() {
				when, _ = parseDate(it.Date)
			}
			body := it.Description
			if strings.TrimSpace(body) == "" {
				body = it.Content
			}
			out = append(out, rawItem{
				Title:      it.Title,
				Body:       body,
				When:       when,
				GUID:       it.GUID,
				Link:       it.Link,
				Categories: it.Categories,
			})
		}
		return out, nil
	}

	var rdf rdfDoc
	if err := xml.Unmarshal(data, &rdf); err == nil && strings.EqualFold(rdf.XMLName.Local, "RDF") {
		out := make([]rawItem, 0, len(rdf.Items))
		for _, it := range rdf.Items {
			when, _ := parseDate(it.Date)
			if when.IsZero() {
				when, _ = parseDate(it.PubDate)
			}
			body := it.Description
			if strings.TrimSpace(body) == "" {
				body = it.Content
			}
			link := it.Link
			if strings.TrimSpace(link) == "" {
				link = it.About
			}
			out = append(out, rawItem{
				Title:      it.Title,
				Body:       body,
				When:       when,
				GUID:       it.About,
				Link:       link,
				Categories: it.Categories,
			})
		}
		return out, nil
	}

	var atom atomDoc
	if err := xml.Unmarshal(data, &atom); err == nil && atom.XMLName.Local == "feed" {
		out := make([]rawItem, 0, len(atom.Entries))
		for _, e := range atom.Entries {
			when, _ := parseDate(e.Updated)
			if when.IsZero() {
				when, _ = parseDate(e.Published)
			}
			body := e.Summary
			if strings.TrimSpace(body) == "" {
				body = e.Content
			}
			out = append(out, rawItem{
				Title:      e.Title,
				Body:       body,
				When:       when,
				GUID:       e.ID,
				Link:       atomAlternate(e.Links),
				Categories: atomTerms(e.Categories),
			})
		}
		return out, nil
	}

	// The fifth collection format: an HTML page. Every feed shape has failed to
	// parse by this point, so nothing here can steal a document from a parser
	// that understood it — a page is only reached when the bytes are not a feed.
	if items, err := parsePage(data); err == nil && len(items) > 0 {
		return items, nil
	}

	return nil, errors.New("unrecognised feed format (not RSS 2.0, RSS 1.0/RDF, Atom, JSON Feed or an HTML page)")
}

// jsonFeed is the subset of JSON Feed v1.x stenella consumes.
type jsonFeed struct {
	Version string         `json:"version"`
	Title   string         `json:"title"`
	Items   []jsonFeedItem `json:"items"`
}

type jsonFeedItem struct {
	ID            string   `json:"id"`
	URL           string   `json:"url"`
	Title         string   `json:"title"`
	ContentText   string   `json:"content_text"`
	ContentHTML   string   `json:"content_html"`
	Summary       string   `json:"summary"`
	DatePublished string   `json:"date_published"`
	DateModified  string   `json:"date_modified"`
	Tags          []string `json:"tags"`
}

// ---------------------------------------------------------------------------
// the aggregator
// ---------------------------------------------------------------------------

// aggregator fetches every source, sanitises the items, merges them into one
// list and caches the result for ttl.
type aggregator struct {
	sources   []rssSource
	client    *http.Client
	ttl       time.Duration
	maxBody   int64
	perSource int
	attempts  int

	// sig identifies the source set this aggregator was built for, so the app
	// can rebuild it when the configured sources change.
	sig string

	mu    sync.RWMutex
	cache []Article
	at    time.Time
	errs  []string
}

// defaultFeedClient is used when the caller does not supply one.
//
// It speaks HTTP/1.1 on purpose: CBC's CDN resets HTTP/2 streams with
// "INTERNAL_ERROR; received from peer" (observed live), while HTTP/1.1 serves
// every source in the default list correctly.
func defaultFeedClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ForceAttemptHTTP2:     false,
			TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
			DialContext:           (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   8 * time.Second,
			ResponseHeaderTimeout: 12 * time.Second,
			MaxIdleConnsPerHost:   4,
		},
	}
}

func newAggregator(sources []rssSource, client *http.Client, ttl time.Duration) *aggregator {
	if client == nil {
		client = defaultFeedClient()
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &aggregator{
		sources:   sources,
		client:    client,
		ttl:       ttl,
		maxBody:   4 << 20,
		perSource: 60,
		attempts:  2,
	}
}

// Sources lists the configured source names (never their URLs).
func (a *aggregator) Sources() []string {
	names := make([]string, 0, len(a.sources))
	for _, s := range a.sources {
		names = append(names, s.Name)
	}
	return names
}

// Errors returns the fetch errors from the last collection.
func (a *aggregator) Errors() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.errs...)
}

// Articles returns the merged, sanitised, newest-first item list. It serves the
// cache while it is fresh; a source that fails is skipped, and if every source
// fails the previous (stale) list is served rather than nothing.
func (a *aggregator) Articles(ctx context.Context) []Article {
	a.mu.RLock()
	fresh := !a.at.IsZero() && time.Since(a.at) < a.ttl
	cached := a.cache
	a.mu.RUnlock()
	if fresh {
		return cached
	}

	items, errs := a.collect(ctx)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.errs = errs
	if len(items) > 0 {
		a.cache = items
		a.at = time.Now()
		return items
	}
	return a.cache
}

type fetchResult struct {
	src   rssSource
	items []Article
	err   error
}

// redact removes a source's URL and host from an error message, so a fetch
// failure can never carry a link back out through the API.
func redact(rawURL, msg string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return strings.ReplaceAll(msg, rawURL, "the source")
	}
	if rawURL != "" {
		msg = strings.ReplaceAll(msg, rawURL, "the source")
	}
	if parsed.Host != "" {
		msg = strings.ReplaceAll(msg, parsed.Host, "the source")
	}
	if hn := parsed.Hostname(); hn != "" {
		msg = strings.ReplaceAll(msg, hn, "the source")
	}
	return msg
}

func (a *aggregator) collect(ctx context.Context) ([]Article, []string) {
	ch := make(chan fetchResult, len(a.sources))
	var wg sync.WaitGroup
	for _, s := range a.sources {
		wg.Add(1)
		go func(s rssSource) {
			defer wg.Done()
			items, err := a.fetch(ctx, s)
			ch <- fetchResult{src: s, items: items, err: err}
		}(s)
	}
	wg.Wait()
	close(ch)

	var all []Article
	var errs []string
	for r := range ch {
		if r.err != nil {
			// Redact the URL/host, then sanitise: the message must not smuggle
			// a source link into an API response.
			msg := redact(r.src.URL, r.err.Error())
			errs = append(errs, r.src.Name+": "+sanitizeText(msg, 200))
			continue
		}
		all = append(all, r.items...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Published.After(all[j].Published) })
	return dedupe(all), errs
}

// fetch pulls one source, retrying a transient failure before giving up.
func (a *aggregator) fetch(ctx context.Context, s rssSource) ([]Article, error) {
	attempts := a.attempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		items, err := a.fetchOnce(ctx, s)
		if err == nil {
			return items, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

// getFeedOnce performs one HTTP request and parses it into raw items. It is the
// single point where a source is actually contacted, so both the shared feed
// (which sanitises the result) and the collection pipeline (which needs the
// original text to extract entities and coordinates) share one implementation,
// one timeout policy and one body cap.
func (a *aggregator) getFeedOnce(ctx context.Context, s rssSource) ([]rawItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/json, application/xml, text/xml, */*")
	req.Header.Set("Accept-Language", "en-CA,en;q=0.9,it;q=0.8,fr;q=0.7")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, a.maxBody))
	if err != nil {
		return nil, err
	}
	return parseFeed(data)
}

// FetchRaw pulls one source and returns its raw items, retrying a transient
// failure. The items are unsanitised: the collection pipeline extracts
// entities, handles and coordinates from the original wording, and sanitises
// each field as it stores it.
func (a *aggregator) FetchRaw(ctx context.Context, s rssSource) ([]rawItem, error) {
	attempts := a.attempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		raw, err := a.getFeedOnce(ctx, s)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

// fetchOnce performs a single request/parse pass for one source, and sanitises
// the result into the shared Article shape.
func (a *aggregator) fetchOnce(ctx context.Context, s rssSource) ([]Article, error) {
	raw, err := a.getFeedOnce(ctx, s)
	if err != nil {
		return nil, err
	}

	items := make([]Article, 0, len(raw))
	for _, r := range raw {
		title := sanitizeText(r.Title, maxTitle)
		if title == "" {
			continue
		}
		it := Article{
			Source:      s.Name,
			Title:       title,
			Summary:     sanitizeText(r.Body, maxSummary),
			Published:   r.When,
			ContentHash: shortHash(titleKey(title)),
		}
		if g := strings.TrimSpace(r.GUID); g != "" {
			it.GUIDHash = shortHash(g)
		}
		if l := strings.TrimSpace(r.Link); l != "" {
			it.LinkHash = shortHash(canonicalLink(l))
		}
		for _, c := range r.Categories {
			if c = sanitizeText(c, 60); c != "" {
				it.Categories = append(it.Categories, c)
			}
		}
		it.Categories = uniqueStrings(it.Categories)
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Published.After(items[j].Published) })
	if a.perSource > 0 && len(items) > a.perSource {
		items = items[:a.perSource]
	}
	return items, nil
}

// dedupe removes repeated headlines, keeping the newest occurrence.
func dedupe(in []Article) []Article {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, it := range in {
		key := strings.ToLower(strings.TrimSpace(it.Title))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, it)
	}
	return out
}

// ---------------------------------------------------------------------------
// rendering the combined feed
// ---------------------------------------------------------------------------

type outRSS struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel outChannel `xml:"channel"`
}

type outChannel struct {
	Title         string    `xml:"title"`
	Description   string    `xml:"description"`
	LastBuildDate string    `xml:"lastBuildDate"`
	Categories    []string  `xml:"category,omitempty"`
	Items         []outItem `xml:"item"`
}

// outItem has no Link, Guid, Enclosure, Author, Comments or Source-URL field —
// the shape of the struct is the guarantee. Provenance is the <category>.
type outItem struct {
	Title       string `xml:"title"`
	Description string `xml:"description,omitempty"`
	PubDate     string `xml:"pubDate,omitempty"`
	Category    string `xml:"category,omitempty"`
}

// renderRSS marshals the combined feed. The channel carries no <link>, no
// <atom:link>, no <docs> and no <generator>: there is nothing to follow and
// nothing that fingerprints the host.
func renderRSS(arts []Article, sources []string, now time.Time) ([]byte, error) {
	feed := outRSS{
		Version: "2.0",
		Channel: outChannel{
			Title: "stenella — combined intelligence feed",
			Description: "One feed compiled from several public sources by stenella. " +
				"It contains no links and no private elements: each item is a title, " +
				"a plain-text summary, a publication date and the source name only.",
			LastBuildDate: now.UTC().Format(time.RFC1123Z),
			Categories:    sources,
		},
	}
	for _, ar := range arts {
		it := outItem{
			Title:       sanitizeText(ar.Title, maxTitle),
			Description: sanitizeText(ar.Summary, maxSummary),
			Category:    sanitizeText(ar.Source, 120),
		}
		if !ar.Published.IsZero() {
			it.PubDate = ar.Published.UTC().Format(time.RFC1123Z)
		}
		feed.Channel.Items = append(feed.Channel.Items, it)
	}
	body, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

// ---------------------------------------------------------------------------
// HTTP endpoints
// ---------------------------------------------------------------------------

// serveRSS serves the shareable combined feed.
func (a *aggregator) serveRSS(w http.ResponseWriter, r *http.Request) {
	body, err := renderRSS(a.Articles(r.Context()), a.Sources(), time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveJSON exposes the same sanitised items to the platform itself.
func (a *aggregator) serveJSON(w http.ResponseWriter, r *http.Request) {
	items := a.Articles(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"feed":    "stenella",
		"sources": a.Sources(),
		"count":   len(items),
		"errors":  a.Errors(),
		"items":   items,
		"updated": time.Now().UTC(),
	})
}
