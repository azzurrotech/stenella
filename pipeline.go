// pipeline.go — the collection and processing half of the intelligence cycle.
//
//  1. COLLECT     fetch every enabled source of a collection concurrently
//  2. NORMALISE   one item shape regardless of source format
//  3. DEDUPLICATE collapse a story reported by several sources into one item
//  4. CLASSIFY    assign the acl class, categories and configured labels
//  5. EXTRACT     pull entities and coordinates into the graph
//  6. LINK        propose candidate links for the analyst to judge
//
// Fetching runs concurrently; processing does not. Two wires carrying the same
// story resolve to the same record, so the store pass has to be serial or one
// attribution would be lost — hence the deliberate two-phase shape below.
//
// This is processing, not conclusions: nothing here becomes an *insight*. Only
// the rule engine (insights.go) asserts anything, and it cites its items.
package main

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxBodyLen caps the stored prose body of an item.
const maxBodyLen = 4000

// RunReport is the result of one collection pass.
type RunReport struct {
	Collection string         `json:"collection"`
	Sources    int            `json:"sources"`
	Imported   int            `json:"imported,omitempty"`
	Fetched    int            `json:"fetched"`
	New        int            `json:"new"`
	Collapsed  int            `json:"collapsed"`
	Entities   int            `json:"entities"`
	Links      int            `json:"links"`
	Insights   int            `json:"insights"`
	PerSource  map[string]int `json:"perSource,omitempty"`
	Errors     []string       `json:"errors,omitempty"`
	Started    time.Time      `json:"started"`
	Finished   time.Time      `json:"finished"`
}

// fetchOutcome is one source's fetch result, parked before the sequential pass.
type fetchOutcome struct {
	src  *Source
	raws []rawItem
	err  error
}

// refresh collects and processes a collection. "all" is every configured
// source; any other value is the sources tasking that collection.
func (a *App) refresh(ctx context.Context, collection string) (RunReport, error) {
	rep := RunReport{Collection: collection, Started: nowUTC(), PerSource: map[string]int{}}

	// only one collection run at a time: they share the records they collapse into
	a.runMu.Lock()
	defer a.runMu.Unlock()

	sources, err := a.store.EnabledSources()
	if err != nil {
		return rep, err
	}
	var wanted []*Source
	imported := 0
	for _, s := range sources {
		if !collectionMatches(collection, s.Collection) {
			continue
		}
		// A dataset is material the analyst imports. There is nothing to fetch,
		// so it is counted and left alone — not fetched, not failed.
		if s.Kind == sourceKindDataset {
			imported++
			_ = a.store.SetSourceStatus(s.ID, "imported", 0)
			continue
		}
		wanted = append(wanted, s)
	}
	rep.Sources = len(wanted) + imported
	rep.Imported = imported
	if len(wanted) == 0 {
		rep.Finished = nowUTC()
		return rep, nil
	}

	// --- phase 1: collect, concurrently -----------------------------------
	outcomes := make(chan fetchOutcome, len(wanted))
	var wg sync.WaitGroup
	for _, s := range wanted {
		wg.Add(1)
		go func(s *Source) {
			defer wg.Done()
			raws, err := a.fetcher.FetchRaw(ctx, rssSource{Name: s.Name, URL: s.URL})
			outcomes <- fetchOutcome{src: s, raws: raws, err: err}
		}(s)
	}
	wg.Wait()
	close(outcomes)

	// --- phase 2: process, sequentially -----------------------------------
	var fetched []fetchOutcome
	for out := range outcomes {
		if out.err != nil {
			// redact then sanitise: a fetch failure must not smuggle a link
			msg := redact(out.src.URL, out.err.Error())
			status := "error: " + sanitizeText(msg, 160)
			_ = a.store.SetSourceStatus(out.src.ID, status, 0)
			rep.Errors = append(rep.Errors, out.src.Name+": "+sanitizeText(msg, 200))
			continue
		}
		fetched = append(fetched, out)
	}
	// deterministic order for reproducible reports
	sort.SliceStable(fetched, func(i, j int) bool { return fetched[i].src.Name < fetched[j].src.Name })

	for _, out := range fetched {
		newCount, collapseCount, entities := a.processSource(out.src, out.raws, nowUTC())
		rep.New += newCount
		rep.Collapsed += collapseCount
		rep.Entities += entities
		rep.Fetched += len(out.raws)
		rep.PerSource[out.src.Name] = len(out.raws)
		_ = a.store.SetSourceStatus(out.src.ID, "ok", len(out.raws))
	}

	// --- phase 3: derive --------------------------------------------------
	rep.Links = a.proposeLinks(collection)
	if n, err := a.runRules(collection); err != nil {
		rep.Errors = append(rep.Errors, "rules: "+sanitizeText(err.Error(), 160))
	} else {
		rep.Insights = n
	}

	rep.Finished = nowUTC()
	return rep, nil
}

// refreshOne collects a single source by id, then derives links and insights
// for its collection. It is what the per-source \"fetch now\" control calls.
func (a *App) refreshOne(ctx context.Context, id string) (RunReport, error) {
	rep := RunReport{Collection: "all", Started: nowUTC(), PerSource: map[string]int{}}

	src, err := a.store.Source(id)
	if err != nil {
		return rep, err
	}
	rep.Collection = collectionOrDefault(src.Collection)
	rep.Sources = 1

	// a dataset has no wire to read
	if src.Kind == sourceKindDataset {
		rep.Imported = 1
		_ = a.store.SetSourceStatus(src.ID, "imported", 0)
		rep.Finished = nowUTC()
		return rep, nil
	}

	a.runMu.Lock()
	defer a.runMu.Unlock()

	raws, err := a.fetcher.FetchRaw(ctx, rssSource{Name: src.Name, URL: src.URL})
	if err != nil {
		msg := redact(src.URL, err.Error())
		_ = a.store.SetSourceStatus(src.ID, "error: "+sanitizeText(msg, 160), 0)
		rep.Errors = append(rep.Errors, src.Name+": "+sanitizeText(msg, 200))
		rep.Finished = nowUTC()
		return rep, nil
	}

	rep.Fetched = len(raws)
	rep.PerSource[src.Name] = len(raws)
	newCount, collapseCount, entities := a.processSource(src, raws, nowUTC())
	rep.New, rep.Collapsed, rep.Entities = newCount, collapseCount, entities
	_ = a.store.SetSourceStatus(src.ID, "ok", len(raws))

	rep.Links = a.proposeLinks(rep.Collection)
	if n, err := a.runRules(rep.Collection); err == nil {
		rep.Insights = n
	}
	rep.Finished = nowUTC()
	return rep, nil
}

// processSource normalises, de-duplicates, classifies and extracts for one
// source's fetch. It must only be called from the sequential phase.
func (a *App) processSource(src *Source, raws []rawItem, fetchedAt time.Time) (newCount, collapsedCount, entityCount int) {
	for _, raw := range raws {
		it, ok := normalise(src, raw, fetchedAt)
		if !ok {
			continue
		}

		existing := a.findDuplicate(it)
		if existing != nil {
			if a.mergeInto(existing, it) {
				collapsedCount++
				if err := a.store.PutItem(existing); err == nil {
					n := a.extractInto(existing, raw)
					entityCount += n
				}
			}
			continue
		}

		n := a.extractInto(it, raw)
		if err := a.store.PutItem(it); err != nil {
			continue
		}
		entityCount += n
		newCount++
	}
	return newCount, collapsedCount, entityCount
}

// normalise reduces one raw feed entry to the platform's item shape, as a plain
// link-free, private-free record. Its identity fields are hashes, so the
// de-duplication identity survives while the link itself does not.
func normalise(src *Source, raw rawItem, fetchedAt time.Time) (*Item, bool) {
	title := cleanInline(raw.Title, maxTitle)
	if title == "" {
		return nil, false
	}

	// An entry the wire never dated is placed on the timeline at the moment it
	// was collected, not at "now" as the item is read — the report's fetch time
	// is the honest one, and two runs of the same undated source must agree.
	published := raw.When
	if published.IsZero() {
		published = fetchedAt
	}

	it := &Item{
		SourceID:     src.ID,
		Source:       src.Name,
		Collection:   collectionOrDefault(src.Collection),
		Title:        title,
		Summary:      cleanInline(raw.Body, maxSummary),
		Body:         cleanInline(raw.Body, maxBodyLen),
		ACL:          aclOrDefault(src.ACL),
		ContentHash:  shortHash(titleKey(title)),
		Attributions: []string{src.Name},
		Published:    saneTime(published),
		Fetched:      fetchedAt,
	}
	// the stable identity the README names: guid, then canonical link
	if g := strings.TrimSpace(raw.GUID); g != "" {
		it.GUIDHash = shortHash(g)
	}
	if l := strings.TrimSpace(raw.Link); l != "" {
		it.LinkHash = shortHash(canonicalLink(l))
	}
	it.ID = "item-" + it.ContentHash

	for _, c := range raw.Categories {
		if c = cleanInline(c, 60); c != "" {
			it.Categories = append(it.Categories, c)
		}
	}
	it.Categories = uniqueStrings(classify(title, it.Summary, it.Categories))
	return it, true
}

// findDuplicate looks the item up by the README's identity chain: guid, then
// canonical link, then content (title) hash.
func (a *App) findDuplicate(it *Item) *Item {
	if it.GUIDHash != "" {
		if ex, ok := a.store.ItemByHash("guidHash", it.GUIDHash); ok {
			return ex
		}
	}
	if it.LinkHash != "" {
		if ex, ok := a.store.ItemByHash("linkHash", it.LinkHash); ok {
			return ex
		}
	}
	if ex, ok := a.store.ItemByHash("contentHash", it.ContentHash); ok {
		return ex
	}
	return nil
}

// mergeInto folds a later sighting into the item already stored, keeping every
// attribution — the README's "one item with five source attributions".
//
// A later sighting that matched by guid is the same record re-read (the wire
// may have carried a new headline), so it updates the prose. A match by link or
// title is a different wire describing the same story, so it only adds
// provenance and keeps the first wording.
func (a *App) mergeInto(existing, seen *Item) bool {
	before := len(existing.Attributions)
	existing.Attributions = uniqueStrings(append(existing.Attributions, seen.Attributions...))
	existing.Categories = uniqueStrings(append(existing.Categories, seen.Categories...))
	if seen.Published.After(existing.Published) {
		existing.Published = seen.Published
	}
	existing.Fetched = seen.Fetched
	if existing.Summary == "" {
		existing.Summary = seen.Summary
	}
	if existing.Body == "" {
		existing.Body = seen.Body
	}
	// The README names two different events, and they are treated differently:
	//
	//   - the same guid on both wires is the *same record re-read*. The wire may
	//     carry a corrected headline, so the prose follows the newest reading.
	//   - a record that matched by link or title has no guid yet, and a sighting
	//     that has one identifies it. The guid is adopted with its prose, because
	//     it is the first time the record has been pinned to a stable identity.
	//
	// A match by link or title alone never overwrites the first wording: that
	// sighting is a different wire describing the story, not a re-read.
	if existing.GUIDHash != "" && existing.GUIDHash == seen.GUIDHash {
		if seen.Title != "" {
			existing.Title = seen.Title
		}
		if seen.Summary != "" {
			existing.Summary = seen.Summary
		}
		if seen.Body != "" {
			existing.Body = seen.Body
		}
	} else if existing.GUIDHash == "" && seen.GUIDHash != "" {
		existing.GUIDHash = seen.GUIDHash
		existing.Title, existing.Summary, existing.Body = seen.Title, seen.Summary, seen.Body
	}
	if existing.LinkHash == "" && seen.LinkHash != "" {
		existing.LinkHash = seen.LinkHash
	}
	if existing.Geo == "" && seen.Geo != "" {
		existing.Geo = seen.Geo
	}
	if existing.SourceID == "" {
		existing.SourceID = seen.SourceID
	}
	return len(existing.Attributions) > before
}

// ---------------------------------------------------------------------------
// classification
// ---------------------------------------------------------------------------

// categoryTerms is the platform's label vocabulary: matched terms contribute
// these canonical labels to an item's categories. It is a lexicon, not a model
// — explicit and inspectable, and it feeds both the views and the rule engine.
var categoryTerms = map[string][]string{
	"cyber":        {"cyber", "cybersecurity", "malware", "ransomware", "phishing", "breach", "hacking", "zero-day", "cve", "exploit", "vulnerability"},
	"supply-chain": {"supply chain", "supply-chain", "supplier", "supplier", "vendor", "logistics", "procurement"},
	"security":     {"security", "defence", "defense", "military", "armed forces", "nato", "threat", "terror"},
	"energy":       {"energy", "oil", "gas", "pipeline", "nuclear", "renewable", "grid"},
	"economy":      {"economy", "inflation", "gdp", "budget", "market", "trade", "tariff", "interest rate"},
	"climate":      {"climate", "emission", "weather", "wildfire", "flood", "hurricane", "storm"},
	"health":       {"health", "vaccine", "pandemic", "hospital", "disease", "outbreak"},
	"election":     {"election", "ballot", "poll", "campaign", "parliament", "vote", "senate"},
	"government":   {"minister", "department", "agency", "government", "policy", "regulation", "federal"},
	"privacy":      {"privacy", "surveillance", "data protection", "tracking", "collection"},
}

// classify adds canonical labels derived from the item's own text to the
// categories the source already supplied.
func classify(title, summary string, supplied []string) []string {
	out := append([]string{}, supplied...)
	text := strings.ToLower(title + " " + summary)
	for label, terms := range categoryTerms {
		for _, t := range terms {
			if strings.Contains(text, t) {
				out = append(out, label)
				break
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// extraction
// ---------------------------------------------------------------------------

var (
	handleExtractRe = regexp.MustCompile(`@[A-Za-z0-9_]{2,}`)
	hashExtractRe   = regexp.MustCompile(`#[\p{L}\p{N}_]{2,}`)
	coordRe         = regexp.MustCompile(`-?\d{1,3}\.\d+\s*,\s*-?\d{1,3}\.\d+`)

	// an organisation is a capitalised phrase ending in an institutional suffix
	orgRe = regexp.MustCompile(`\b([A-Z][\p{L}\p{N}&.'’-]*(?:\s+(?:of|the|and|[A-Z][\p{L}\p{N}&.'’-]*)){0,4}\s+(?:Inc|Corp|Corporation|Ltd|LLC|Limited|University|Institute|Foundation|Bank|Ministry|Department|Agency|Commission|Council|Group|Company|Consortium|Alliance))\b`)

	// a person is a title followed by a name: "Prime Minister Mark Carney"
	personRe = regexp.MustCompile(`\b((?:Prime\s+Minister|President|Chancellor|Premier|Governor|Mayor|Chief\s+Executive|CEO|Director|Secretary|General|Admiral|Ambassador|Minister|Senator)\s+[A-Z][\p{L}\p{N}'’-]+(?:\s+[A-Z][\p{L}\p{N}'’-]+)?)`)
)

// entityHit is one recogniser result before it is folded into a record.
type entityHit struct {
	Kind string
	Name string
}

// extractEntities recognises people, organisations, places, handles and
// keywords in an item's own wording. It runs on the raw text so that handles
// and hashtags are found before the sanitiser removes them, which is also why
// the store keeps only what was recognised and never the original prose.
//
// The recognisers are pattern-based and deliberately modest: what they miss
// still surfaces as a category, and nothing they catch is presented as a fact —
// it is a candidate for the analyst, exactly as the README describes extraction
// as processing rather than conclusions.
func extractEntities(raw rawItem) []entityHit {
	text := strings.TrimSpace(raw.Title + "\n" + raw.Body)
	if text == "" {
		return nil
	}

	// remove e-mail addresses first: a handle must not be half of an address
	withoutEmails := emailRe.ReplaceAllString(text, " ")

	var hits []entityHit
	add := func(kind, name string) {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > 80 {
			return
		}
		hits = append(hits, entityHit{Kind: kind, Name: name})
	}

	for _, m := range handleExtractRe.FindAllString(withoutEmails, 8) {
		add("handle", m)
	}
	for _, m := range hashExtractRe.FindAllString(text, 16) {
		add("keyword", strings.TrimPrefix(m, "#"))
	}
	for _, m := range orgRe.FindAllStringSubmatch(text, 8) {
		add("org", m[1])
	}
	for _, m := range personRe.FindAllStringSubmatch(text, 6) {
		add("person", m[1])
	}
	for _, p := range placeNames(text) {
		add("place", displayName(p))
	}

	// categories are keywords too: they are the platform's own vocabulary
	for _, c := range raw.Categories {
		add("keyword", c)
	}

	return dedupeHits(hits)
}

func dedupeHits(hits []entityHit) []entityHit {
	seen := map[string]bool{}
	out := make([]entityHit, 0, len(hits))
	for _, h := range hits {
		k := h.Kind + "|" + strings.ToLower(strings.TrimSpace(h.Name))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, h)
	}
	return out
}

// extractInto recognises entities in one item, folds each into its record, and
// fills the item's entity list and coordinates. It returns how many distinct
// entities it observed.
func (a *App) extractInto(it *Item, raw rawItem) int {
	hits := extractEntities(raw)
	if len(hits) == 0 {
		return 0
	}

	// coordinates come from the item's wording, wherever it points
	text := raw.Title + " " + raw.Body
	if it.Geo == "" {
		if lat, lon, ok := extractGeo(text); ok {
			it.Geo = formatGeo(lat, lon)
		}
	}

	ids := append([]string{}, it.Entities...)
	for _, h := range hits {
		id, err := a.store.observeEntity(h.Kind, h.Name, it.ID, saneTime(raw.When))
		if err != nil || id == "" {
			continue
		}
		if !containsFold(ids, id) {
			ids = append(ids, id)
		}
	}
	it.Entities = ids
	return len(hits)
}

// ---------------------------------------------------------------------------
// candidate links
// ---------------------------------------------------------------------------

// maxProposedLinks caps how many automatic edges a run may add, so an unbounded
// corpus cannot turn the graph into a hairball before an analyst sees it.
const maxProposedLinks = 200

// proposeLinks creates candidate edges between items that share an entity. The
// edges are marked origin "auto": they are proposals, and the analyst accepts
// one (origin becomes human) or rejects it (the edge is deleted).
func (a *App) proposeLinks(collection string) int {
	items, err := a.store.ItemsIn(collection, "", 0)
	if err != nil {
		return 0
	}
	existing, err := a.store.Links()
	if err != nil {
		return 0
	}
	seen := map[string]bool{}
	for _, l := range existing {
		seen[l.From+"|"+l.To] = true
		seen[l.To+"|"+l.From] = true
	}

	created := 0
	index := map[string][]*Item{}
	for _, it := range items {
		for _, e := range it.Entities {
			index[e] = append(index[e], it)
		}
	}
	for _, group := range index {
		if len(group) < 2 {
			continue
		}
		for i := 0; i < len(group) && created < maxProposedLinks; i++ {
			for j := i + 1; j < len(group) && created < maxProposedLinks; j++ {
				from, to := group[i], group[j]
				if from.ID == to.ID || seen[from.ID+"|"+to.ID] {
					continue
				}
				seen[from.ID+"|"+to.ID] = true
				l := &Link{
					From:     from.ID,
					To:       to.ID,
					Relation: "related",
					Origin:   "auto",
					Note:     "shares an entity",
				}
				if err := a.store.PutLink(l); err == nil {
					created++
				}
			}
		}
	}
	return created
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func collectionOrDefault(c string) string {
	if strings.TrimSpace(c) == "" {
		return "all"
	}
	return c
}

func aclOrDefault(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "public", "private", "protected":
		return v
	}
	return "public"
}

// saneTime replaces a missing publication time with the fetch time, so an item
// always has a place on the timeline.
func saneTime(t time.Time) time.Time {
	if t.IsZero() {
		return nowUTC()
	}
	return t.UTC()
}
