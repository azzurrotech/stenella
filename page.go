// page.go — collection from HTML pages.
//
// The README lists five collection formats: RSS 2.0, RSS 1.0/RDF, Atom, JSON
// Feed and *pages*. The first four are documents that describe their items;
// a page is just a web page, so stenella has to read the page the way a
// subscriber would: find the articles (or the headline links) and treat each
// one as an item.
//
// This is deliberately a small, inspectable recogniser rather than an HTML
// parser: stdlib regexp over the markup, two strategies, and an honest failure
// when the page carries nothing collectable — a 404 page must report "no
// items", never an empty success.
//
// Everything a page yields still passes through the same normalise/dedupe/
// classify/extract path as a feed entry, so a story collected from a page and
// the same story read from a feed collapse onto one record.
package main

import (
	"errors"
	"html"
	"regexp"
	"strings"
)

// maxPageItems caps what one page may contribute to a collection pass, matching
// the per-source item cap the feed path applies.
const maxPageItems = 60

var (
	// script/style/noscript bodies are not page content, and a stray "</article>"
	// inside one must not end a block early.
	pageNoiseRe    = regexp.MustCompile(`(?is)<(script|style|noscript|svg|nav|footer)\b.*?</(script|style|noscript|svg|nav|footer)\s*>`)
	pageCommentRe  = regexp.MustCompile(`(?is)<!--.*?-->`)
	pageArticleRe  = regexp.MustCompile(`(?is)<article\b([^>]*)>(.*?)</article\s*>`)
	pageHeadingRe  = regexp.MustCompile(`(?is)<h[1-3]\b([^>]*)>(.*?)</h[1-3]\s*>`)
	pageAnchorRe   = regexp.MustCompile(`(?is)<a\b[^>]*>(.*?)</a\s*>`)
	pageTimeRe     = regexp.MustCompile(`(?i)<time\b[^>]*\bdatetime\s*=\s*"([^"]+)"`)
	pageIDRe       = regexp.MustCompile(`(?i)\bid\s*=\s*"([^"]+)"`)
	pageHrefRe     = regexp.MustCompile(`(?i)\bhref\s*=\s*"([^"]+)"`)
	pageMetaTimeRe = regexp.MustCompile(`(?i)<meta\b[^>]*(?:property|name)\s*=\s*"(?:article:published_time|datepublished|pubdate|date|last-modified)"[^>]*content\s*=\s*"([^"]+)"`)
	pageTagRe      = regexp.MustCompile(`(?s)<[^>]+>`)
	pageSpaceRe    = regexp.MustCompile(`\s+`)
)

// looksLikeHTML reports whether a document is a web page rather than a feed.
// It runs only after every feed format has already failed to parse, so a page
// is never collected in place of a feed that merely tripped up.
func looksLikeHTML(s string) bool {
	head := strings.ToLower(strings.TrimSpace(s))
	head = strings.TrimPrefix(head, "\xef\xbb\xbf") // UTF-8 BOM
	switch {
	case strings.HasPrefix(head, "<!doctype html"):
		return true
	case strings.Contains(head[:min(len(head), 4096)], "<html"):
		return true
	case strings.Contains(head[:min(len(head), 4096)], "<body"):
		return true
	}
	return false
}

// pageText turns a fragment of markup into the plain prose a view can render
// and a recogniser can read: tags out, entities decoded, whitespace collapsed.
func pageText(s string) string {
	s = pageTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = pageSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// pageItem is a draft record lifted from one block of a page.
func pageItem(title, body, when, guid, link string) rawItem {
	it := rawItem{
		Title: strings.TrimSpace(title),
		Body:  strings.TrimSpace(body),
		GUID:  strings.TrimSpace(guid),
		Link:  strings.TrimSpace(link),
	}
	if t, ok := parseDate(strings.TrimSpace(when)); ok {
		it.When = t
	}
	return it
}

// parsePage reads an HTML page into raw items using the article-block
// strategy, falling back to headline links, and fails when the page carries
// nothing collectable.
func parsePage(data []byte) ([]rawItem, error) {
	src := string(data)
	if !looksLikeHTML(src) {
		return nil, errors.New("not an HTML page")
	}

	// noise out first: a "<nav>" list is not the page's content, and scripts
	// must never contribute prose to the corpus.
	clean := pageCommentRe.ReplaceAllString(pageNoiseRe.ReplaceAllString(src, " "), " ")

	// a page-level publish time (article:published_time and friends) covers the
	// entries that carry no <time> of their own.
	pageWhen := ""
	if m := pageMetaTimeRe.FindStringSubmatch(clean); m != nil && m[1] != "" {
		pageWhen = m[1]
	}

	items := parseArticleBlocks(clean, pageWhen)
	if len(items) == 0 {
		items = parseHeadlineBlocks(clean, pageWhen)
	}
	if len(items) == 0 {
		return nil, errors.New("page carried no collectable items")
	}
	return items, nil
}

// parseArticleBlocks reads <article> elements: the shape news and blog pages
// use for "here is one story".
func parseArticleBlocks(clean, pageWhen string) []rawItem {
	var out []rawItem
	for _, m := range pageArticleRe.FindAllStringSubmatch(clean, maxPageItems) {
		attrs, inner := m[1], m[2]

		title := firstHeadingText(inner)
		if title == "" {
			title = firstAnchorText(inner)
		}
		if title == "" {
			continue
		}

		// the heading's own text is not part of the body
		body := inner
		if hm := pageHeadingRe.FindStringSubmatch(inner); hm != nil {
			body = strings.Replace(inner, hm[0], " ", 1)
		}
		body = pageText(body)

		when := pageWhen
		if tm := pageTimeRe.FindStringSubmatch(inner); tm != nil {
			when = tm[1]
		}

		guid := ""
		if gm := pageIDRe.FindStringSubmatch(attrs); gm != nil {
			guid = gm[1]
		}
		link := firstHref(inner)
		if link == "" {
			link = firstHref(attrs)
		}
		if guid == "" {
			guid = link
		}

		out = append(out, pageItem(title, body, when, guid, link))
	}
	return out
}

// parseHeadlineBlocks reads heading links — the shape an index or listing page
// uses when the page has no article markup at all. The prose that follows a
// heading, up to the next heading, becomes the item's body.
func parseHeadlineBlocks(clean, pageWhen string) []rawItem {
	matches := pageHeadingRe.FindAllStringSubmatchIndex(clean, maxPageItems)
	var out []rawItem
	for i, m := range matches {
		attrs, heading := clean[m[2]:m[3]], clean[m[4]:m[5]]
		_ = attrs

		// a listing heading is a headline only when it links somewhere
		link := firstHref(heading)
		if link == "" {
			link = firstHref(attrs)
		}
		if link == "" {
			continue
		}
		title := pageText(heading)
		if title == "" {
			continue
		}

		// body: everything up to the next heading (or the page end)
		end := len(clean)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		body := pageText(clean[m[1]:end])

		out = append(out, pageItem(title, body, pageWhen, link, link))
	}
	return out
}

func firstHeadingText(fragment string) string {
	if m := pageHeadingRe.FindStringSubmatch(fragment); m != nil {
		return pageText(m[2])
	}
	return ""
}

func firstAnchorText(fragment string) string {
	if m := pageAnchorRe.FindStringSubmatch(fragment); m != nil {
		return pageText(m[1])
	}
	return ""
}

func firstHref(fragment string) string {
	if m := pageHrefRe.FindStringSubmatch(fragment); m != nil {
		return m[1]
	}
	return ""
}
