// search.go — server-side search, and the dissemination layer: exports, OPML
// and shares.
//
// The README splits search in two: server-side search over record *metadata*
// (this file) and client-side search over rendered *content* (web/app.js). The
// server side knows fields; the browser knows what the analyst is reading.
//
// Dissemination carries one rule with it: everything stenella hands out about
// an *item* is already sanitised, because an item never stores a link or an
// author in the first place. OPML is the one exception, and deliberately so —
// it is a subscription-list format for sources (the operator's own
// configuration, which has to be a URL to mean anything), and it is a tool for
// the operator, not the shareable feed.
package main

import (
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"azzurrotech/shepherd/token"
)

// ---------------------------------------------------------------------------
// server-side search
// ---------------------------------------------------------------------------

// itemQuery is a parsed metadata query.
type itemQuery struct {
	Words      []string // free text over title, summary and source
	Tags       []string // category / label
	Sources    []string
	Collection string
	Entities   []string
	ACL        string
	From, To   time.Time

	// GUIDHashes and LinkHashes let an operator search by the identity the
	// README names — guid, then canonical link — without the platform ever
	// storing either value: the query is hashed and matched against the hash.
	GUIDHashes []string
	LinkHashes []string

	// Unavailable names recognised operators this information model cannot
	// answer. They are reported to the caller rather than silently ignored.
	Unavailable []string
}

// parseQuery reads the compact query language the search bar and the saved
// groups share:
//
//	term...                       free text
//	tag:critical                  a category or label
//	source:CBC                    a source name
//	collection:news               a collection
//	entity:ottawa                 an entity id or name
//	acl:public                    the classification class
//	from:2026-01-01 to:2026-03-01 publication window
//	guid:<value>                  an item's stable guid (matched as a hash)
//	link:<url>                    an item's canonical link (matched as a hash)
//
// A value containing a space is quoted — tag:"supply chain" — and a quoted
// phrase in the free text is matched as a phrase. Unknown prefixes are treated
// as plain text so a search never silently fails. `author:` and `host:` are
// recognised but unanswerable — the model records neither — and are reported
// through queryNotes.
func parseQuery(s string) itemQuery {
	var q itemQuery
	for _, tok := range queryTokens(s) {
		key, val, ok := strings.Cut(tok, ":")
		val = strings.Trim(val, `"'`)
		if !ok || val == "" || key == "" {
			q.Words = append(q.Words, tok)
			continue
		}
		switch strings.ToLower(key) {
		case "tag", "label":
			q.Tags = append(q.Tags, val)
		case "source", "feed":
			q.Sources = append(q.Sources, val)
		case "collection":
			q.Collection = val
		case "entity":
			q.Entities = append(q.Entities, val)
		case "acl":
			q.ACL = val
		case "from":
			if t, err := time.Parse("2006-01-02", val); err == nil {
				q.From = t.UTC()
			}
		case "to":
			if t, err := time.Parse("2006-01-02", val); err == nil {
				q.To = t.Add(24*time.Hour - time.Second).UTC()
			}
		case "guid":
			// the stored identity is the hash of the raw guid
			q.GUIDHashes = append(q.GUIDHashes, shortHash(strings.TrimSpace(val)))
		case "link", "url":
			// and the stored link identity is the hash of the canonical link,
			// so a tracking URL finds the same record as its clean form
			q.LinkHashes = append(q.LinkHashes, shortHash(canonicalLink(val)))
		case "author", "host":
			// recognised, but this information model stores neither: an author
			// is a private element and a link host is a link. Report, don't guess.
			q.Unavailable = append(q.Unavailable, strings.ToLower(key))
		default:
			q.Words = append(q.Words, tok)
		}
	}
	return q
}

// queryNotes explains any recognised operator the information model cannot
// answer, so an empty-looking result is never a mystery.
func queryNotes(q itemQuery) []string {
	var notes []string
	for _, u := range uniqueStrings(q.Unavailable) {
		switch u {
		case "author":
			notes = append(notes, "author search is unavailable: stenella records no author, by design (an author is a private element)")
		case "host":
			notes = append(notes, "link-host search is unavailable: stenella stores no link host; search by guid:<value> or link:<url> instead")
		default:
			notes = append(notes, "search operator "+u+": is not supported")
		}
	}
	return notes
}

// matches applies a parsed query to one item.
func (q itemQuery) matches(it *Item) bool {
	if q.Collection != "" && !collectionMatches(q.Collection, it.Collection) {
		return false
	}
	if q.ACL != "" && !strings.EqualFold(q.ACL, it.ACL) {
		return false
	}
	if len(q.Tags) > 0 {
		if _, ok := intersect(q.Tags, it.Categories); !ok {
			return false
		}
	}
	if len(q.Sources) > 0 {
		if !anyOfFold(q.Sources, uniqueStrings(append(it.Attributions, it.Source))) {
			return false
		}
	}
	if len(q.Entities) > 0 {
		if !entityMatches(q.Entities, it) {
			return false
		}
	}
	if len(q.GUIDHashes) > 0 && !containsFold(q.GUIDHashes, it.GUIDHash) {
		return false
	}
	if len(q.LinkHashes) > 0 && !containsFold(q.LinkHashes, it.LinkHash) {
		return false
	}
	if !q.From.IsZero() && !it.Published.IsZero() && it.Published.Before(q.From) {
		return false
	}
	if !q.To.IsZero() && !it.Published.IsZero() && it.Published.After(q.To) {
		return false
	}
	for _, w := range q.Words {
		w = strings.ToLower(w)
		if !strings.Contains(strings.ToLower(it.Title), w) &&
			!strings.Contains(strings.ToLower(it.Summary), w) &&
			!strings.Contains(strings.ToLower(it.Source), w) {
			return false
		}
	}
	return true
}

// runQuery applies a query string to a collection.
func (a *App) runQuery(collection, query string, limit int) ([]*Item, error) {
	items, _, err := a.searchItems(collection, query, limit)
	return items, err
}

// searchItems applies a query and also returns the parsed form, so a caller can
// explain any operator the model cannot answer.
func (a *App) searchItems(collection, query string, limit int) ([]*Item, itemQuery, error) {
	items, err := a.store.ItemsIn(collection, "", 0)
	if err != nil {
		return nil, itemQuery{}, err
	}
	q := parseQuery(query)
	a.resolveQueryEntities(&q)
	out := make([]*Item, 0, len(items))
	for _, it := range items {
		if q.matches(it) {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, q, nil
}

// queryTokens splits a query on whitespace while honouring double quotes, so
// tag:"supply chain" is one token and a quoted phrase in the free text stays
// together. It is the only piece of the query language that is not a plain
// strings.Fields, and it exists because the platform's own vocabulary contains
// multi-word labels.
func queryTokens(s string) []string {
	var (
		out     []string
		b       strings.Builder
		inQuote bool
	)
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t' || r == '\n' || r == '\r') && !inQuote:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

// resolveQueryEntities turns the entity names a human types into the entity ids
// the items actually carry, so `entity:ottawa` works for an analyst while
// `entity:place:9f2c…` still works for a script. A term that matches no record
// is kept as-is: it will simply match nothing, which is the honest answer, and
// the operator can still search by a full id.
func (a *App) resolveQueryEntities(q *itemQuery) {
	if len(q.Entities) == 0 {
		return
	}
	entities, err := a.store.Entities(0)
	if err != nil {
		return
	}
	var ids []string
	for _, want := range q.Entities {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		matched := false
		for _, e := range entities {
			if e.ID == want ||
				strings.EqualFold(e.Name, want) ||
				strings.EqualFold(e.Kind+":"+e.Name, want) {
				ids = append(ids, e.ID)
				matched = true
			}
		}
		if !matched {
			ids = append(ids, want)
		}
	}
	q.Entities = uniqueStrings(ids)
}

// ---------------------------------------------------------------------------
// exports
// ---------------------------------------------------------------------------

// ExportItem is exactly what leaves the platform: the sanitised projection,
// with no link and no author, resolved entity names instead of ids, and the
// analyst's own aggregation.
type ExportItem struct {
	Title        string    `json:"title"`
	Summary      string    `json:"summary,omitempty"`
	Source       string    `json:"source"`
	Attributions []string  `json:"attributions,omitempty"`
	Collection   string    `json:"collection,omitempty"`
	Categories   []string  `json:"categories,omitempty"`
	ACL          string    `json:"acl,omitempty"`
	Published    time.Time `json:"published,omitempty"`
	Geo          string    `json:"geo,omitempty"`
	Entities     []string  `json:"entities,omitempty"`
	Comments     int       `json:"comments,omitempty"`
}

// exportItem resolves ids to names and assembles the export projection.
func (a *App) exportItem(it *Item) ExportItem {
	e := ExportItem{
		Title:        it.Title,
		Summary:      it.Summary,
		Source:       it.Source,
		Attributions: uniqueStrings(it.Attributions),
		Collection:   it.Collection,
		Categories:   it.Categories,
		ACL:          it.ACL,
		Published:    it.Published,
		Geo:          it.Geo,
	}
	for _, id := range it.Entities {
		if en, err := a.store.Entity(id); err == nil {
			e.Entities = append(e.Entities, strings.ToLower(en.Kind)+":"+en.Name)
		}
	}
	if comments, err := a.store.CommentsFor(it.ID); err == nil {
		e.Comments = len(comments)
	}
	return e
}

func (a *App) exportJSON(w http.ResponseWriter, r *http.Request) {
	collection := formValue(r, "collection")
	query := formValue(r, "q")
	items, err := a.runQuery(collection, query, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]ExportItem, 0, len(items))
	for _, it := range items {
		out = append(out, a.exportItem(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"feed":       "stenella",
		"collection": collectionOrDefault(collection),
		"count":      len(out),
		"generated":  nowUTC(),
		"items":      out,
	})
}

// csvHeader is the fixed export shape, so a downstream tool can rely on it.
var csvHeader = []string{
	"title", "summary", "source", "attributions", "collection",
	"categories", "acl", "published", "lat", "lon", "entities", "comments",
}

func (a *App) exportCSV(w http.ResponseWriter, r *http.Request) {
	collection := formValue(r, "collection")
	query := formValue(r, "q")
	items, err := a.runQuery(collection, query, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A UTF-8 BOM so a spreadsheet opens non-ASCII text correctly.
	if _, err := w.Write([]byte("\xef\xbb\xbf")); err != nil {
		return
	}
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write(csvHeader)

	for _, it := range items {
		e := a.exportItem(it)
		lat, lon, _ := parseGeo(e.Geo)
		var latS, lonS string
		if e.Geo != "" {
			latS, lonS = formatCoord(lat), formatCoord(lon)
		}
		_ = cw.Write([]string{
			e.Title,
			e.Summary,
			e.Source,
			strings.Join(e.Attributions, "; "),
			e.Collection,
			strings.Join(e.Categories, "; "),
			e.ACL,
			formatTime(e.Published),
			latS,
			lonS,
			strings.Join(e.Entities, "; "),
			fmt.Sprintf("%d", e.Comments),
		})
	}
}

func formatCoord(f float64) string { return strings.TrimRight(fmt.Sprintf("%.5f", f), "0") }

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------------------
// OPML import and export
// ---------------------------------------------------------------------------

type opmlOutline struct {
	Text     string        `xml:"text,attr,omitempty"`
	Title    string        `xml:"title,attr,omitempty"`
	Type     string        `xml:"type,attr,omitempty"`
	XMLURL   string        `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string        `xml:"htmlUrl,attr,omitempty"`
	Outlines []opmlOutline `xml:"outline"`
}

type opmlBody struct {
	Outlines []opmlOutline `xml:"outline"`
}

type opmlDoc struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    opmlHead `xml:"head"`
	Body    opmlBody `xml:"body"`
}

type opmlHead struct {
	Title string `xml:"title"`
}

// opmlExport writes the configured sources as an OPML subscription list, grouped
// by collection. This is operator tooling: OPML is a format for feed URLs, so
// unlike the shareable feed this document necessarily contains them.
func (a *App) opmlExport(w http.ResponseWriter, r *http.Request) {
	sources, err := a.store.Sources()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	groups := map[string][]opmlOutline{}
	var order []string
	for _, s := range sources {
		c := collectionOrDefault(s.Collection)
		if _, ok := groups[c]; !ok {
			order = append(order, c)
		}
		groups[c] = append(groups[c], opmlOutline{
			Text: s.Name, Title: s.Name, Type: "rss", XMLURL: s.URL,
		})
	}
	sort.Strings(order)

	doc := opmlDoc{
		Version: "2.0",
		Head:    opmlHead{Title: "stenella sources"},
	}
	for _, c := range order {
		doc.Body.Outlines = append(doc.Body.Outlines, opmlOutline{
			Text: c, Title: c, Outlines: groups[c],
		})
	}

	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append([]byte(xml.Header), body...))
}

// trackingParams are query parameters that identify a campaign rather than a
// feed. They are dropped when comparing two source URLs: the same feed is the
// same subscription whether it was copied out of a newsletter or not.
var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "dclid": true, "msclkid": true, "yclid": true,
	"igshid": true, "twclid": true, "spm": true, "ref": true, "ref_src": true,
	"mc_cid": true, "mc_eid": true, "_hsenc": true, "_hsmi": true,
}

// feedIdentity is the identity two source URLs share when they subscribe to the
// same feed. Unlike canonicalLink — which exists to collapse the variants of an
// item's own link — it keeps meaningful query parameters, because a government
// API commonly serves several distinct feeds from one path with different
// parameters. Dropping the query there would silently merge two different
// subscriptions into one.
func feedIdentity(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if trackingParams[lk] || strings.HasPrefix(lk, "utm_") {
			q.Del(k)
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""
	u.RawQuery = q.Encode()
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

// opmlImport reads an OPML document and creates a source for every feed entry
// it names. Outlines without an xmlUrl are folders (collections), so an
// imported document keeps the grouping it arrived with.
func (a *App) opmlImport(w http.ResponseWriter, r *http.Request) {
	var doc opmlDoc
	if err := xml.NewDecoder(r.Body).Decode(&doc); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid OPML: "+sanitizeText(err.Error(), 200))
		return
	}
	defer r.Body.Close()

	type pending struct {
		collection string
		outline    opmlOutline
	}
	var queue []pending

	// A leaf outline that names no feed is an incomplete entry: it is counted,
	// not silently dropped, so the operator sees that their document did
	// nothing. A folder is an outline with children.
	skipped := 0
	for _, o := range doc.Body.Outlines {
		if o.XMLURL != "" {
			queue = append(queue, pending{"all", o})
			continue
		}
		if len(o.Outlines) == 0 {
			skipped++
			continue
		}
		for _, child := range o.Outlines {
			c := o.Title
			if c == "" {
				c = o.Text
			}
			queue = append(queue, pending{c, child})
		}
	}

	added := 0
	existing, err := a.store.Sources()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	known := map[string]bool{}
	for _, s := range existing {
		known[feedIdentity(s.URL)] = true
	}

	for _, p := range queue {
		if p.outline.XMLURL == "" {
			skipped++
			continue
		}
		key := feedIdentity(p.outline.XMLURL)
		if known[key] {
			skipped++
			continue
		}
		name := p.outline.Title
		if name == "" {
			name = p.outline.Text
		}
		src := Source{
			Name:        cleanInline(name, 80),
			URL:         strings.TrimSpace(p.outline.XMLURL),
			Kind:        "feed",
			Collection:  cleanSlug(p.collection),
			Reliability: "C",
			ACL:         "public",
			Enabled:     true,
		}
		if src.Name == "" || src.URL == "" {
			skipped++
			continue
		}
		if err := a.store.PutSource(&src); err != nil {
			skipped++
			continue
		}
		known[key] = true
		added++
	}
	a.flushAggrs()

	writeJSON(w, http.StatusCreated, map[string]any{
		"added":   added,
		"skipped": skipped,
		"total":   len(existing) + added,
	})
}

func cleanSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "all"
	}
	if v := slug(s); v != "" {
		return v
	}
	return "all"
}

// ---------------------------------------------------------------------------
// shares
// ---------------------------------------------------------------------------

// createShare issues an identity-free capability token for a single item,
// group or insight, so a colleague can be handed one specific thing with a
// reference that the operator can revoke later.
//
// The token is a capability *record*, not an access control: atp adds no
// security to hosted resources, so the shareable feed and the record endpoints
// are readable by anyone with the URL by design. What the token buys is a
// revocable, verifiable pointer — atp's own /api/token/verify confirms it and
// /api/revoke can cancel it.
func (a *App) createShare(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"` // item | group | insight | collection
		ID   string `json:"id"`
		TTL  string `json:"ttl"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	if in.Kind == "" {
		in.Kind = "item"
	}
	if in.ID == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if !oneOf(in.Kind, "item", "group", "insight", "collection", "all") {
		writeErr(w, http.StatusBadRequest, "kind must be item, group, insight or collection")
		return
	}

	ttl := 7 * 24 * time.Hour
	if in.TTL != "" {
		if d, err := time.ParseDuration(in.TTL); err == nil && d > 0 {
			ttl = d
		}
	}

	raw, claims, err := a.srv.Shepherd().Issue(token.IssueOptions{
		Subject: "stenella-share",
		Scopes:  []string{"stenella", "share"},
		TTL:     ttl,
		Meta: map[string]string{
			"kind":   in.Kind,
			"target": in.ID,
		},
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "issue share: "+sanitizeText(err.Error(), 200))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":    raw,
		"kind":     in.Kind,
		"id":       in.ID,
		"issuer":   a.srv.Shepherd().Issuer(),
		"subject":  claims.Subject,
		"issuedAt": time.Unix(int64(claims.IssuedAt), 0).UTC(),
		"expires":  time.Unix(int64(claims.ExpiresAt), 0).UTC(),
		"verify":   "/api/token/verify",
		"revoke":   "/api/revoke",
		"note": "a capability record; atp adds no access control to hosted " +
			"resources, so the shareable feed stays readable by design",
	})
}

func oneOf(v string, opts ...string) bool {
	for _, o := range opts {
		if v == o {
			return true
		}
	}
	return false
}
