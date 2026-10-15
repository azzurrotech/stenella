// handlers.go — the platform's API handlers.
//
// Every route registered in app.go's routes() lands in one of these methods.
// The shape is deliberately uniform: read the request with the helpers in
// http.go, do one thing to the store, answer with JSON. The pipeline lives in
// pipeline.go, the rule engine in insights.go, search and dissemination in
// search.go, and the atp console in console.go.
package main

import (
	"net/http"
	"strconv"
	"strings"

	"azzurrotech/atp"
)

// ---------------------------------------------------------------------------
// items
// ---------------------------------------------------------------------------

// listItems answers the feed, the map and the information tree: the same items,
// filtered by collection, source, query and time, newest first.
func (a *App) listItems(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	collection := formValue(r, "collection")
	source := formValue(r, "source")
	query := formValue(r, "q")

	var (
		items []*Item
		err   error
		notes []string
	)
	if query != "" {
		var q itemQuery
		items, q, err = a.searchItems(collection, query, limit)
		notes = queryNotes(q)
	} else {
		items, err = a.store.ItemsIn(collection, source, limit)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// resolve the entity ids on each item so the views can render names
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, a.itemView(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"count": len(out),
		"notes": notes,
	})
}

// itemView renders one item for a view: the record plus the resolved names of
// its entities. Nothing here can reintroduce a link — there is none to resolve.
func (a *App) itemView(it *Item) map[string]any {
	entities := make([]map[string]string, 0, len(it.Entities))
	for _, id := range it.Entities {
		if e, err := a.store.Entity(id); err == nil {
			entities = append(entities, map[string]string{
				"id": e.ID, "kind": e.Kind, "name": e.Name,
			})
		}
	}
	lat, lon, hasGeo := parseGeo(it.Geo)
	return map[string]any{
		"id":           it.ID,
		"source":       it.Source,
		"attributions": it.Attributions,
		"collection":   it.Collection,
		"title":        it.Title,
		"summary":      it.Summary,
		"body":         it.Body,
		"categories":   it.Categories,
		"acl":          it.ACL,
		"published":    it.Published,
		"fetched":      it.Fetched,
		"entities":     entities,
		"geo":          it.Geo,
		"lat":          lat,
		"lon":          lon,
		"hasGeo":       hasGeo,
	}
}

func (a *App) createItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Item
		Author string `json:"author"`
	}
	if !decode(w, r, &in) {
		return
	}
	it := in.Item
	if strings.TrimSpace(it.Title) == "" {
		writeErr(w, http.StatusBadRequest, "title is required")
		return
	}
	// An imported record is the analyst's own material, so it is normalised the
	// same way a collected one is: sanitised, hashed, classified.
	it.Title = cleanInline(it.Title, maxTitle)
	it.Summary = cleanInline(it.Summary, maxSummary)
	it.Body = cleanInline(it.Body, maxBodyLen)
	if it.Collection == "" {
		it.Collection = "all"
	}
	if it.Source == "" {
		it.Source = "manual"
	}
	it.ACL = aclOrDefault(it.ACL)
	it.Fetched = nowUTC()
	if it.Published.IsZero() {
		it.Published = it.Fetched
	}
	if it.ContentHash == "" {
		it.ContentHash = shortHash(titleKey(it.Title))
	}
	if it.ID == "" {
		it.ID = "item-" + it.ContentHash
	}
	it.SourceID = "manual"
	it.Attributions = uniqueStrings(append(it.Attributions, it.Source))
	it.Categories = uniqueStrings(classify(it.Title, it.Summary, it.Categories))

	if err := a.store.PutItem(&it); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// extract entities from the analyst's own material too
	raw := rawItem{Title: it.Title, Body: it.Body, When: it.Published, Categories: it.Categories}
	if n := a.extractInto(&it, raw); n > 0 {
		_ = a.store.PutItem(&it)
	}
	writeJSON(w, http.StatusCreated, a.itemView(&it))
}

// getItem is the analyst's single-item page: the record plus everything the
// platform and the analyst have attached to it.
func (a *App) getItem(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	it, err := a.store.Item(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "item not found")
		return
	}
	links, _ := a.store.LinksFor(id)
	comments, _ := a.store.CommentsFor(id)
	notes, _ := a.store.NotesFor(id)
	insights, _ := a.store.Insights("all")

	var cited []*Insight
	for _, in := range insights {
		if containsFold(in.Items, id) {
			cited = append(cited, in)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"item":     a.itemView(it),
		"links":    links,
		"comments": comments,
		"notes":    notes,
		"insights": cited,
	})
}

func (a *App) deleteItem(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	if err := a.store.DeleteItem(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---------------------------------------------------------------------------
// entities
// ---------------------------------------------------------------------------

func (a *App) listEntities(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	entities, err := a.store.Entities(limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": entities, "count": len(entities)})
}

func (a *App) getEntity(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	e, err := a.store.Entity(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "entity not found")
		return
	}
	items := make([]map[string]any, 0, len(e.Items))
	for _, iid := range e.Items {
		if it, err := a.store.Item(iid); err == nil {
			items = append(items, a.itemView(it))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entity": e, "items": items})
}

// ---------------------------------------------------------------------------
// links
// ---------------------------------------------------------------------------

func (a *App) listLinks(w http.ResponseWriter, r *http.Request) {
	if item := formValue(r, "item"); item != "" {
		links, err := a.store.LinksFor(item)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"links": links})
		return
	}
	links, err := a.store.Links()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": links, "count": len(links)})
}

func (a *App) createLink(w http.ResponseWriter, r *http.Request) {
	var l Link
	if !decode(w, r, &l) {
		return
	}
	l.From = strings.TrimSpace(l.From)
	l.To = strings.TrimSpace(l.To)
	if l.From == "" || l.To == "" {
		writeErr(w, http.StatusBadRequest, "from and to are required")
		return
	}
	if l.From == l.To {
		writeErr(w, http.StatusBadRequest, "an item cannot link to itself")
		return
	}
	if l.Relation == "" {
		l.Relation = "related"
	}
	// an analyst-created edge is always a human assertion
	l.Origin = "human"
	l.Note = cleanInline(l.Note, 300)
	if err := a.store.PutLink(&l); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

// linkAction accepts or rejects a candidate link. Accepting promotes the
// automatic edge to a human assertion; rejecting deletes it.
func (a *App) linkAction(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	var in struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	links, err := a.store.Links()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var target *Link
	for _, l := range links {
		if l.ID == id {
			target = l
			break
		}
	}
	if target == nil {
		writeErr(w, http.StatusNotFound, "link not found")
		return
	}
	switch in.Action {
	case "accept":
		target.Origin = "human"
		if err := a.store.PutLink(target); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, target)
	case "reject", "delete":
		if err := a.store.DeleteLink(id); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	default:
		writeErr(w, http.StatusBadRequest, "action must be accept or reject")
	}
}

func (a *App) deleteLink(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	if err := a.store.DeleteLink(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---------------------------------------------------------------------------
// comments
// ---------------------------------------------------------------------------

func (a *App) listComments(w http.ResponseWriter, r *http.Request) {
	if item := formValue(r, "item"); item != "" {
		c, err := a.store.CommentsFor(item)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"comments": c})
		return
	}
	c, err := a.store.Comments()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": c, "count": len(c)})
}

func (a *App) createComment(w http.ResponseWriter, r *http.Request) {
	var c Comment
	if !decode(w, r, &c) {
		return
	}
	if c.Item == "" || strings.TrimSpace(c.Body) == "" {
		writeErr(w, http.StatusBadRequest, "item and body are required")
		return
	}
	if c.Author == "" {
		c.Author = "analyst"
	}
	c.Body = cleanInline(c.Body, 2000)
	c.Author = cleanInline(c.Author, 80)
	if err := a.store.PutComment(&c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *App) deleteComment(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	if err := a.store.DeleteComment(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---------------------------------------------------------------------------
// notes
// ---------------------------------------------------------------------------

func (a *App) listNotes(w http.ResponseWriter, r *http.Request) {
	notes, err := a.store.Notes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes, "count": len(notes)})
}

func (a *App) createNote(w http.ResponseWriter, r *http.Request) {
	var n Note
	if !decode(w, r, &n) {
		return
	}
	if strings.TrimSpace(n.Title) == "" {
		writeErr(w, http.StatusBadRequest, "title is required")
		return
	}
	n.Title = cleanInline(n.Title, 160)
	n.Body = cleanInline(n.Body, 8000)
	if err := a.store.PutNote(&n); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (a *App) getNote(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	n, err := a.store.Note(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "note not found")
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (a *App) deleteNote(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	if err := a.store.DeleteNote(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---------------------------------------------------------------------------
// groups
// ---------------------------------------------------------------------------

func (a *App) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := a.store.Groups()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// a group is a saved query, so listing it returns what it currently matches
	type groupView struct {
		*Group
		Count int `json:"count"`
	}
	out := make([]groupView, 0, len(groups))
	for _, g := range groups {
		items, _ := a.runQuery("", g.Query, 0)
		out = append(out, groupView{Group: g, Count: len(items)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out, "count": len(out)})
}

func (a *App) createGroup(w http.ResponseWriter, r *http.Request) {
	var g Group
	if !decode(w, r, &g) {
		return
	}
	if strings.TrimSpace(g.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	g.Name = cleanInline(g.Name, 120)
	g.Query = strings.TrimSpace(g.Query)
	if err := a.store.PutGroup(&g); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (a *App) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	if err := a.store.DeleteGroup(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---------------------------------------------------------------------------
// rules
// ---------------------------------------------------------------------------

func (a *App) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := a.store.Rules()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "count": len(rules)})
}

func (a *App) createRule(w http.ResponseWriter, r *http.Request) {
	var rule Rule
	if !decode(w, r, &rule) {
		return
	}
	if strings.TrimSpace(rule.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	rule.Name = cleanInline(rule.Name, 120)
	rule.Text = cleanInline(rule.Text, 300)
	rule.Severity = severityOr(rule.Severity)
	if err := a.store.PutRule(&rule); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (a *App) getRule(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	rule, err := a.store.Rule(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "rule not found")
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

// ruleAction runs, enables or disables a rule. Running it evaluates the rule
// over the whole collection and writes its insight.
func (a *App) ruleAction(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	var in struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	rule, err := a.store.Rule(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "rule not found")
		return
	}
	switch in.Action {
	case "run":
		items, err := a.store.ItemsIn("", "", 0)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		matches := evaluate(rule, items, nowUTC())
		if len(matches) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"rule": rule.ID, "fired": false, "count": 0})
			return
		}
		if err := a.writeInsight(rule, matches); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rule": rule.ID, "fired": true, "count": len(matches)})
	case "enable":
		rule.Enabled = true
		_ = a.store.PutRule(rule)
		writeJSON(w, http.StatusOK, rule)
	case "disable":
		rule.Enabled = false
		_ = a.store.PutRule(rule)
		writeJSON(w, http.StatusOK, rule)
	default:
		writeErr(w, http.StatusBadRequest, "action must be run, enable or disable")
	}
}

func (a *App) deleteRule(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	if err := a.store.DeleteRule(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---------------------------------------------------------------------------
// insights
// ---------------------------------------------------------------------------

func (a *App) listInsights(w http.ResponseWriter, r *http.Request) {
	status := formValue(r, "status")
	if status == "" {
		status = "new"
	}
	insights, err := a.store.Insights(status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// resolve rule names so the inbox reads as sentences, not ids
	type insightView struct {
		*Insight
		RuleName string `json:"ruleName,omitempty"`
	}
	out := make([]insightView, 0, len(insights))
	for _, in := range insights {
		v := insightView{Insight: in}
		if rule, err := a.store.Rule(in.Rule); err == nil {
			v.RuleName = rule.Name
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"insights": out, "count": len(out)})
}

// insightAction is the two-command lifecycle from the README: an insight is
// dismissed with a reason, or promoted to a note. Reopen reverses either.
func (a *App) insightAction(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	var in struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch in.Action {
	case "dismiss":
		ins, err := a.dismissInsight(id, in.Reason)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ins)
	case "promote":
		ins, note, err := a.promoteInsight(id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"insight": ins, "note": note})
	case "reopen":
		ins, err := a.reopenInsight(id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ins)
	default:
		writeErr(w, http.StatusBadRequest, "action must be dismiss, promote or reopen")
	}
}

func (a *App) deleteInsight(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	if err := a.store.DeleteInsight(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---------------------------------------------------------------------------
// pipeline
// ---------------------------------------------------------------------------

// runAll is the platform's "collect now" function. It fetches and processes the
// named collection (\"all\" by default), then derives links and insights.
func (a *App) runAll(w http.ResponseWriter, r *http.Request) {
	collection := formValue(r, "collection")
	if v := formValue(r, "id"); v != "" {
		collection = v
	}
	if collection == "" {
		collection = "all"
	}
	rep, err := a.refresh(r.Context(), collection)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---------------------------------------------------------------------------
// search, statistics, health
// ---------------------------------------------------------------------------

func (a *App) search(w http.ResponseWriter, r *http.Request) {
	query := formValue(r, "q")
	collection := formValue(r, "collection")
	limit := queryInt(r, "limit", 100)
	items, q, err := a.searchItems(collection, query, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, a.itemView(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":      query,
		"collection": collectionOrDefault(collection),
		"count":      len(out),
		"hits":       out,
		"notes":      queryNotes(q),
	})
}

// stats is the dashboard's summary: the record counts, the collections, and the
// shape of what has been collected.
func (a *App) stats(w http.ResponseWriter, r *http.Request) {
	collections, _ := a.store.Collections()
	sources, _ := a.store.Sources()
	items, _ := a.store.ItemsIn("", "", 0)
	insights, _ := a.store.Insights("new")
	entities, _ := a.store.Entities(0)

	perSource := map[string]int{}
	perCollection := map[string]int{}
	perCategory := map[string]int{}
	var geoCount int
	for _, it := range items {
		perSource[it.Source]++
		perCollection[collectionOrDefault(it.Collection)]++
		for _, c := range it.Categories {
			perCategory[strings.ToLower(c)]++
		}
		if _, _, ok := parseGeo(it.Geo); ok {
			geoCount++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"counts":        a.store.Counts(),
		"items":         len(items),
		"entities":      len(entities),
		"openInsights":  len(insights),
		"sources":       len(sources),
		"collections":   collections,
		"perSource":     perSource,
		"perCollection": perCollection,
		"perCategory":   perCategory,
		"withGeo":       geoCount,
	})
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"platform":  "atp",
		"atp":       atp.Version,
		"app":       "stenella",
		"time":      nowUTC(),
		"encrypted": a.srv.Encrypted(),
		"counts":    a.store.Counts(),
	})
}

// parseCount is a small helper for limits arriving as text in a form.
func parseCount(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return def
	}
	return n
}
