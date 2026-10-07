package feed

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

// KindSite is a generic web page scraped heuristically. It is a source kind in
// its own right rather than a fallback inside the RSS parser, so a client can see
// that a source is being scraped and can turn it off without breaking the feed.
const KindSite = "site"

// scraperMaxBytes bounds how much of a page the extractor reads. Real articles
// are well under this; a page that is larger is usually a listing page or an
// accidental binary download, and reading all of it wastes memory without making
// the result better.
const scraperMaxBytes = 4 << 20

// scraperSummaryLimit bounds the extracted body. A scraped page is a fallback
// source, not the client's own writing, so a truncated body is better than a
// multi-megabyte record per item.
const scraperSummaryLimit = 4000

// ScrapeError describes why a page could not be turned into an item. It is
// returned rather than an empty item so the source's status can say "no headline
// found" instead of silently contributing nothing (plan risk R5).
type ScrapeError struct {
	URL    string
	Reason string
}

func (e *ScrapeError) Error() string {
	return fmt.Sprintf("scraping %s: %s", e.URL, e.Reason)
}

// Unwrap reports the sentinel so a source status can say "the page had nothing
// usable" without matching on the message text.
func (e *ScrapeError) Unwrap() error { return ErrNoScrapableContent }

// item builds the feed item from an extracted page. It is the single place the
// scraper's heuristics are combined, so a test can exercise the whole extraction
// without standing up an HTTP server. The second argument is the response
// content type, kept for callers that sniff with it; the extraction itself is
// content-type independent because the heuristics already read the markup.
func (d *htmlDoc) item(rawURL, _ string) (Item, error) {
	doc := d
	title := firstNonEmpty(
		doc.meta("og:title"),
		doc.meta("twitter:title"),
		doc.title,
		doc.heading,
	)
	if title == "" {
		return Item{}, &ScrapeError{URL: rawURL, Reason: "no headline in the page (tried og:title, twitter:title, <title>, <h1>)"}
	}

	// The link is the page's canonical URL when it declares one, because that is
	// the address a reader should follow; the requested URL is the fallback.
	link := firstNonEmpty(doc.meta("og:url"), doc.link("canonical"), rawURL)

	// Summary comes from the description metas when the page sets one, because
	// that is a description *of* the page rather than the page itself. Content is
	// the article body. Falling back the other way would put a one-sentence meta
	// description where a reader expects the article.
	summary := firstNonEmpty(
		doc.meta("og:description"),
		doc.meta("twitter:description"),
		doc.meta("description"),
	)
	article := firstNonEmpty(doc.text("article"), doc.text("main"))
	if summary == "" {
		summary = summarize(article)
	}
	content := article

	author := firstNonEmpty(
		doc.meta("author"),
		doc.meta("article:author"),
		doc.meta("twitter:creator"),
	)

	published := parseAnyTimeMayFail(
		doc.meta("article:published_time"),
		doc.meta("og:published_time"),
		doc.timeDT,
		doc.timeText,
	)
	// A page with no date at all is not an error: it becomes a fresh item, which
	// is the right behaviour for a page someone subscribed to as a "what changed
	// today" signal.
	if published.IsZero() {
		published = time.Now().UTC()
	}

	categories := doc.metaKeywords()
	if author != "" {
		categories = appendUnique(categories, author)
	}

	return Item{
		SourceID:   "",
		SourceName: "",
		Title:      title,
		Link:       link,
		GUID:       link,
		Author:     author,
		Summary:    summary,
		Content:    content,
		Categories: categories,
		Published:  published.UTC(),
		Fetched:    time.Now().UTC(),
	}, nil
}

// summarize reduces a body to a single-line summary of at most
// scraperSummaryLimit characters, collapsing whitespace and cutting on a word
// boundary so the result never ends mid-word.
func summarize(body string) string {
	body = strings.TrimSpace(html.UnescapeString(stripTags(body)))
	if body == "" {
		return ""
	}
	body = strings.Join(strings.Fields(body), " ")
	if len(body) <= scraperSummaryLimit {
		return body
	}
	cut := body[:scraperSummaryLimit]
	if i := strings.LastIndexAny(cut, " "); i > scraperSummaryLimit/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// ---- a minimal HTML walk -----------------------------------------------------
//
// Go's standard library has no HTML parser, and the plan forbids third-party
// ones. What is needed here is narrow: pull a few well-known tags and attributes
// out of a page, tolerate anything malformed, and never fail on a document it
// does not understand. That is a token walk, not a tree build — and because the
// output is re-escaped or stripped before it is stored, there is no injection
// surface in what we keep.

var (
	reComment    = regexp.MustCompile(`(?s)<!--.*?-->`)
	reTag        = regexp.MustCompile(`(?s)<[^>]*>`)
	reWhitespace = regexp.MustCompile(`\s+`)

	reTitleTag = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reH1Tag    = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	reTimeTag  = regexp.MustCompile(`(?is)<time\b([^>]*)>(.*?)</time>`)
	reLinkTag  = regexp.MustCompile(`(?is)<link\b([^>]*)>`)

	reMetaTag  = regexp.MustCompile(`(?is)<meta\b([^>]*?)/?>`)
	reAttrName = regexp.MustCompile(`(?is)([a-z0-9:_-]+)\s*=\s*("([^"]*)"|'([^']*)'|([^\s"'>]+))`)
)

// voidElements are the elements whose content is markup or script rather than
// prose. Their bodies are removed before any text is extracted, so a page cannot
// smuggle "function () { … }" into an article body.
//
// The pattern is built per tag rather than as one alternation with a backreference:
// RE2 has no backreferences, and compiling seven small patterns at init is cheaper
// than carrying a regexp engine that does.
var voidElements = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, 7)
	// <head> is deliberately absent: the meta tags in it carry the headline,
	// author, description and dates that the whole extractor depends on.
	for _, tag := range []string{"script", "style", "noscript", "svg", "template", "iframe"} {
		out = append(out, regexp.MustCompile(`(?is)<`+tag+`\b[^>]*>.*?</`+tag+`\s*>`))
	}
	return out
}()

type attrMap map[string]string

func parseAttrs(s string) attrMap {
	out := attrMap{}
	for _, m := range reAttrName.FindAllStringSubmatch(s, -1) {
		val := m[3]
		if val == "" && m[4] != "" {
			val = m[4]
		}
		if val == "" {
			val = m[5]
		}
		out[strings.ToLower(m[1])] = html.UnescapeString(val)
	}
	return out
}

// htmlDoc is the extracted view of a page.
type htmlDoc struct {
	title    string
	heading  string
	metas    attrMap
	links    attrMap
	timeDT   string
	timeText string
	blocks   map[string]string // tag -> concatenated text
}

func parsePage(page string) *htmlDoc {
	page = reComment.ReplaceAllString(page, " ")
	for _, re := range voidElements {
		page = re.ReplaceAllString(page, " ")
	}

	d := &htmlDoc{metas: attrMap{}, links: attrMap{}, blocks: map[string]string{}}
	if m := reTitleTag.FindStringSubmatch(page); m != nil {
		d.title = cleanText(m[1])
	}
	if m := reH1Tag.FindStringSubmatch(page); m != nil {
		d.heading = cleanText(m[1])
	}
	if m := reTimeTag.FindStringSubmatch(page); m != nil {
		d.timeDT = parseAttrs(m[1])["datetime"]
		d.timeText = cleanText(m[2])
	}
	for _, m := range reMetaTag.FindAllStringSubmatch(page, -1) {
		a := parseAttrs(m[1])
		name := strings.ToLower(firstNonEmpty(a["property"], a["name"], a["http-equiv"]))
		if name == "" {
			continue
		}
		// First wins: a page that sets og:title twice wants the first, and a page
		// that sets both og:description and description wants each in its own key.
		if _, seen := d.metas[name]; !seen {
			d.metas[name] = a["content"]
		}
	}
	for _, m := range reLinkTag.FindAllStringSubmatch(page, -1) {
		a := parseAttrs(m[1])
		if rel := strings.ToLower(a["rel"]); rel != "" {
			if _, seen := d.links[rel]; !seen {
				d.links[rel] = a["href"]
			}
		}
	}
	for _, tag := range []string{"article", "main"} {
		if v := elementText(page, tag); v != "" {
			d.blocks[tag] = v
		}
	}
	return d
}

// elementText returns the concatenated text of the first <tag>…</tag> block.
//
// A block with no closing tag is read to the end of the input rather than
// discarded. That is what a truncated fetch looks like: the opening tag and the
// prose arrived, the closing tag did not. Treating the missing close as "this
// page has no article" would throw away every word that did arrive, which is the
// opposite of what degrading gracefully should do.
func elementText(page, tag string) string {
	re := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>(.*?)</` + tag + `\s*>`)
	if m := re.FindStringSubmatch(page); m != nil {
		return cleanText(m[1])
	}
	open := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>`)
	loc := open.FindStringIndex(page)
	if loc == nil {
		return ""
	}
	return cleanText(page[loc[1]:])
}

func (d *htmlDoc) meta(key string) string { return d.metas[strings.ToLower(key)] }

func (d *htmlDoc) link(rel string) string { return d.links[strings.ToLower(rel)] }

func (d *htmlDoc) text(tag string) string { return d.blocks[tag] }

func (d *htmlDoc) metaKeywords() []string {
	var out []string
	for _, key := range []string{"keywords", "news_keywords", "article:tag"} {
		raw := d.meta(key)
		if raw == "" {
			continue
		}
		sep := ","
		if strings.Contains(raw, ";") {
			sep = ";"
		}
		for _, part := range strings.Split(raw, sep) {
			if p := strings.TrimSpace(part); p != "" {
				out = appendUnique(out, p)
			}
		}
	}
	return out
}

// cleanText strips tags and collapses whitespace. Everything it returns goes
// through the normal item pipeline, which stores it as a ciphertext envelope, so
// the value is never interpreted as markup again.
func cleanText(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(reWhitespace.ReplaceAllString(s, " "))
}

// stripTags is cleanText without the whitespace collapse, for summarizing.
func stripTags(s string) string {
	return reTag.ReplaceAllString(s, " ")
}

// looksLikeHTML reports whether a fetched body is a web page rather than a feed.
func looksLikeHTML(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "html") {
		return true
	}
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	low := strings.ToLower(string(head))
	return strings.Contains(low, "<!doctype html") ||
		strings.Contains(low, "<html") ||
		(strings.Contains(low, "<head") && strings.Contains(low, "<body"))
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if strings.EqualFold(existing, v) {
			return list
		}
	}
	return append(list, v)
}

// scrapeBytes is the fetch path's entry point: it turns a body that has already
// been read into an item, so the fetch can sniff once and then route without a
// second round trip.
func (e *Engine) scrapeBytes(rawURL, contentType string, body []byte) ([]Item, error) {
	// The fetch path reads up to its own (larger) ceiling so a feed document is
	// never truncated mid-element. A page has no such requirement: past this
	// point the extra bytes are inline trackers and base64 images, so the bound
	// is applied here, where the content is known to be HTML.
	if len(body) > scraperMaxBytes {
		body = body[:scraperMaxBytes]
	}
	it, err := parsePage(string(body)).item(rawURL, contentType)
	if err != nil {
		return nil, err
	}
	return []Item{it}, nil
}

// sniffJSON reports whether a body is a JSON document. It is used only when the
// source kind is unknown, where guessing wrong in either direction is cheap and
// reporting a parse error would be confusing.
func sniffJSON(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}

// ErrNoScrapableContent wraps the "nothing usable on this page" condition so a
// caller can test for it with errors.Is rather than matching a message.
var ErrNoScrapableContent = errors.New("no scrapable content")
