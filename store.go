// store.go — stenella's record layer, built on pod.
//
// The nine record types of model.go each live in their own pod table under
// stenella/, so the platform's state is a directory of readable XML files
// (pod's promise) rather than an opaque database. This file holds the typed
// accessors; the records themselves are the model.
//
// pod's value type is map[string]string, so each typed accessor is a thin
// conversion around a generic put/get/list helper. Tables are provisioned from
// the record structs themselves, which means the declared schema can never
// drift from the model.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"azzurrotech/pod"
)

// Pod table paths. pod allows slashes in a table name, so every stenella table
// nests under a single stenella/ prefix — the platform's records never sit
// loose beside another application's.
const (
	tableSources  = "stenella/sources"
	tableItems    = "stenella/items"
	tableEntities = "stenella/entities"
	tableLinks    = "stenella/links"
	tableComments = "stenella/comments"
	tableNotes    = "stenella/notes"
	tableGroups   = "stenella/groups"
	tableRules    = "stenella/rules"
	tableInsights = "stenella/insights"
)

// Store is the record layer. mu guards the read-modify-write sequences that
// keep derived state consistent (entity counts, cross-source de-duplication).
type Store struct {
	pod *pod.Store
	mu  sync.Mutex
}

// NewStore wraps a pod store and makes sure every table exists.
func NewStore(p *pod.Store) (*Store, error) {
	if p == nil {
		return nil, errors.New("stenella: nil pod store")
	}
	s := &Store{pod: p}
	if err := s.ensureTables(); err != nil {
		return nil, err
	}
	return s, nil
}

// Pod exposes the underlying store (the atp console lists it).
func (s *Store) Pod() *pod.Store { return s.pod }

// Tables lists the platform's tables in the information model's order.
func (s *Store) Tables() []string {
	return []string{
		tableSources, tableItems, tableEntities, tableLinks,
		tableComments, tableNotes, tableGroups, tableRules, tableInsights,
	}
}

// ensureTables creates each table and widens it if the record type grew. pod's
// schema is a hint rather than a gate, so this is safe to run on every start.
func (s *Store) ensureTables() error {
	specs := []struct {
		name   string
		sample any
	}{
		{tableSources, Source{}},
		{tableItems, Item{}},
		{tableEntities, Entity{}},
		{tableLinks, Link{}},
		{tableComments, Comment{}},
		{tableNotes, Note{}},
		{tableGroups, Group{}},
		{tableRules, Rule{}},
		{tableInsights, Insight{}},
	}
	for _, sp := range specs {
		names, err := columnNames(sp.sample)
		if err != nil {
			return err
		}
		cols := make([]pod.Column, 0, len(names))
		for _, n := range names {
			cols = append(cols, pod.Column{Name: n, Type: "text"})
		}
		if err := s.pod.UpdateSchema(sp.name, cols); err != nil {
			return fmt.Errorf("stenella: schema %s: %w", sp.name, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// generic record plumbing
// ---------------------------------------------------------------------------

func putRecord[T any](s *Store, table, id string, rec *T) error {
	fields, err := fieldsOf(rec)
	if err != nil {
		return err
	}
	if _, _, err := s.pod.Upsert(table, id, fields); err != nil {
		return fmt.Errorf("stenella: upsert %s/%s: %w", table, id, err)
	}
	return nil
}

func getRecord[T any](s *Store, table, id string) (*T, error) {
	rec, err := s.pod.Get(table, id)
	if err != nil {
		return nil, err
	}
	var out T
	if err := applyFields(rec.Values(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// listRecords decodes every matching record. A record written by an older
// schema that no longer decodes is skipped rather than failing the query.
func listRecords[T any](s *Store, table string, cond pod.Cond, opts *pod.QueryOptions) ([]*T, error) {
	recs, err := s.pod.Query(table, cond, opts)
	if err != nil {
		return nil, err
	}
	out := make([]*T, 0, len(recs))
	for _, r := range recs {
		var v T
		if err := applyFields(r.Values(), &v); err != nil {
			continue
		}
		out = append(out, &v)
	}
	return out, nil
}

func listAll[T any](s *Store, table string) ([]*T, error) {
	return listRecords[T](s, table, nil, nil)
}

// eq, like, and, or build pod conditions.
func eq(field, value string) pod.Cond {
	return pod.PredCond{P: pod.Predicate{Field: field, Op: pod.OpEq, Value: value, HasValue: true}}
}

func like(field, pattern string) pod.Cond {
	return pod.PredCond{P: pod.Predicate{Field: field, Op: pod.OpLike, Value: pattern, HasValue: true}}
}

func and(a, b pod.Cond) pod.Cond { return pod.AndCond{L: a, R: b} }
func or(a, b pod.Cond) pod.Cond  { return pod.OrCond{L: a, R: b} }

// findOne returns the first record whose field equals value.
func findOne[T any](s *Store, table, field, value string) (*T, bool) {
	if value == "" {
		return nil, false
	}
	recs, err := listRecords[T](s, table, eq(field, value), &pod.QueryOptions{Limit: 1})
	if err != nil || len(recs) == 0 {
		return nil, false
	}
	return recs[0], true
}

// ---------------------------------------------------------------------------
// sources
// ---------------------------------------------------------------------------

func (s *Store) PutSource(src *Source) error {
	if src.ID == "" {
		src.ID = "src-" + shortHash(src.Name+"|"+src.URL)
	}
	if src.Added.IsZero() {
		src.Added = nowUTC()
	}
	return putRecord(s, tableSources, src.ID, src)
}

func (s *Store) Source(id string) (*Source, error) {
	return getRecord[Source](s, tableSources, id)
}

func (s *Store) Sources() ([]*Source, error) {
	out, err := listAll[Source](s, tableSources)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Collection != out[j].Collection {
			return out[i].Collection < out[j].Collection
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// EnabledSources lists the sources a collection run should fetch.
func (s *Store) EnabledSources() ([]*Source, error) {
	all, err := s.Sources()
	if err != nil {
		return nil, err
	}
	out := make([]*Source, 0, len(all))
	for _, src := range all {
		if src.Enabled {
			out = append(out, src)
		}
	}
	return out, nil
}

func (s *Store) DeleteSource(id string) error { return s.pod.Delete(tableSources, id) }

// SetSourceStatus records the outcome of a collection run against the source:
// the operator sees "ok" or a redacted failure next to every configured source,
// and how many items the last run produced. The message must already be
// sanitised — a fetch error is built by redact() + sanitizeText() so it cannot
// carry a link.
func (s *Store) SetSourceStatus(id, status string, count int) error {
	src, err := s.Source(id)
	if err != nil {
		return err
	}
	src.LastFetch = nowUTC()
	src.LastStatus = status
	if count > 0 {
		src.LastCount = count
	}
	return s.PutSource(src)
}

// Collections lists the distinct collection names, always including "all".
func (s *Store) Collections() ([]string, error) {
	all, err := s.Sources()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{"all": true}
	out := []string{"all"}
	for _, src := range all {
		c := src.Collection
		if c == "" {
			c = "all"
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out[1:])
	return out, nil
}

// ---------------------------------------------------------------------------
// items
// ---------------------------------------------------------------------------

func (s *Store) PutItem(it *Item) error {
	if it.ID == "" {
		it.ID = "item-" + shortHash(it.ContentHash+it.Title)
	}
	if it.Fetched.IsZero() {
		it.Fetched = nowUTC()
	}
	return putRecord(s, tableItems, it.ID, it)
}

func (s *Store) Item(id string) (*Item, error) {
	return getRecord[Item](s, tableItems, id)
}

// Items returns the collection newest-first, capped at limit (0 = no cap).
func (s *Store) Items(limit int) ([]*Item, error) {
	return s.ItemsIn("", "", limit)
}

// ItemsIn filters by collection, then source name (both optional).
func (s *Store) ItemsIn(collection, source string, limit int) ([]*Item, error) {
	var cond pod.Cond
	if collection != "" && collection != "all" {
		cond = eq("collection", collection)
	}
	if source != "" {
		c := eq("source", source)
		if cond == nil {
			cond = c
		} else {
			cond = and(cond, c)
		}
	}
	opts := &pod.QueryOptions{OrderBy: "published", Desc: true}
	if limit > 0 {
		opts.Limit = limit
	}
	return listRecords[Item](s, tableItems, cond, opts)
}

// ItemByHash finds a stored item by one of the three de-duplication identities.
func (s *Store) ItemByHash(field, hash string) (*Item, bool) {
	if hash == "" {
		return nil, false
	}
	return findOne[Item](s, tableItems, field, hash)
}

// ItemByTitleHash finds an item by its lowercased-title hash, the last-resort
// duplicate key when a source supplies neither guid nor link.
func (s *Store) ItemByTitleHash(h string) (*Item, bool) {
	return findOne[Item](s, tableItems, "contentHash", h)
}

func (s *Store) DeleteItem(id string) error { return s.pod.Delete(tableItems, id) }

// ---------------------------------------------------------------------------
// entities
// ---------------------------------------------------------------------------

// EntityID is derived from kind and name so the same real-world entity always
// has the same record, whichever item introduced it. Both parts are canonicalised
// — kind lowercased, name slugged — so a recogniser that reports "Place" and one
// that reports "place" resolve to one record rather than two.
func EntityID(kind, name string) string {
	return canonicalKind(kind) + ":" + shortHash(slug(name))
}

// canonicalKind maps any recogniser spelling onto the model's five kinds.
func canonicalKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "person", "org", "place", "handle", "keyword":
		return kind
	}
	return "keyword"
}

func (s *Store) PutEntity(e *Entity) error {
	return putRecord(s, tableEntities, e.ID, e)
}

func (s *Store) Entity(id string) (*Entity, error) {
	return getRecord[Entity](s, tableEntities, id)
}

func (s *Store) Entities(limit int) ([]*Entity, error) {
	opts := &pod.QueryOptions{OrderBy: "count", Desc: true}
	if limit > 0 {
		opts.Limit = limit
	}
	return listRecords[Entity](s, tableEntities, nil, opts)
}

// observeEntity folds one occurrence of an entity on an item into its record,
// creating the record on first sight. It is serialised so concurrent fetches
// cannot lose a count.
func (s *Store) observeEntity(kind, name, itemID string, when time.Time) (string, error) {
	name = cleanInline(name, 120)
	if name == "" {
		return "", nil
	}
	id := EntityID(kind, name)

	s.mu.Lock()
	defer s.mu.Unlock()

	e, err := s.Entity(id)
	if err != nil {
		if !errors.Is(err, pod.ErrNotFound) {
			return "", err
		}
		e = &Entity{ID: id, Kind: canonicalKind(kind), Name: name, FirstSeen: when}
	}
	seen := false
	for _, got := range e.Items {
		if got == itemID {
			seen = true
			break
		}
	}
	if !seen {
		e.Items = append(e.Items, itemID)
		e.Count = len(e.Items)
	}
	if e.FirstSeen.IsZero() || (!when.IsZero() && when.Before(e.FirstSeen)) {
		e.FirstSeen = when
	}
	if when.After(e.LastSeen) {
		e.LastSeen = when
	}
	return id, s.PutEntity(e)
}

// ---------------------------------------------------------------------------
// links
// ---------------------------------------------------------------------------

func (s *Store) PutLink(l *Link) error {
	if l.ID == "" {
		l.ID = "link-" + shortHash(l.From+"|"+l.To+"|"+l.Relation)
	}
	if l.Created.IsZero() {
		l.Created = nowUTC()
	}
	if l.Origin == "" {
		l.Origin = "human"
	}
	return putRecord(s, tableLinks, l.ID, l)
}

func (s *Store) Links() ([]*Link, error) {
	out, err := listAll[Link](s, tableLinks)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// LinksFor returns every edge touching an item, in either direction.
func (s *Store) LinksFor(itemID string) ([]*Link, error) {
	recs, err := listRecords[Link](s, tableLinks, or(eq("from", itemID), eq("to", itemID)), nil)
	if err != nil {
		return nil, err
	}
	return recs, nil
}

func (s *Store) DeleteLink(id string) error { return s.pod.Delete(tableLinks, id) }

// ---------------------------------------------------------------------------
// comments
// ---------------------------------------------------------------------------

func (s *Store) PutComment(c *Comment) error {
	if c.ID == "" {
		c.ID = "c-" + shortHash(c.Item+"|"+c.Author+"|"+c.Body+"|"+nowUTC().String())
	}
	if c.Created.IsZero() {
		c.Created = nowUTC()
	}
	return putRecord(s, tableComments, c.ID, c)
}

func (s *Store) Comments() ([]*Comment, error) {
	out, err := listAll[Comment](s, tableComments)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

func (s *Store) CommentsFor(itemID string) ([]*Comment, error) {
	return listRecords[Comment](s, tableComments, eq("item", itemID), nil)
}

func (s *Store) DeleteComment(id string) error { return s.pod.Delete(tableComments, id) }

// ---------------------------------------------------------------------------
// notes
// ---------------------------------------------------------------------------

func (s *Store) PutNote(n *Note) error {
	if n.ID == "" {
		n.ID = "n-" + shortHash(n.Title+"|"+nowUTC().String())
	}
	if n.Created.IsZero() {
		n.Created = nowUTC()
	}
	n.Updated = nowUTC()
	return putRecord(s, tableNotes, n.ID, n)
}

func (s *Store) Note(id string) (*Note, error) { return getRecord[Note](s, tableNotes, id) }

func (s *Store) Notes() ([]*Note, error) {
	out, err := listAll[Note](s, tableNotes)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

func (s *Store) NotesFor(subject string) ([]*Note, error) {
	return listRecords[Note](s, tableNotes, eq("subject", subject), nil)
}

func (s *Store) DeleteNote(id string) error { return s.pod.Delete(tableNotes, id) }

// ---------------------------------------------------------------------------
// groups
// ---------------------------------------------------------------------------

func (s *Store) PutGroup(g *Group) error {
	if g.ID == "" {
		g.ID = "g-" + shortHash(g.Name)
	}
	if g.Created.IsZero() {
		g.Created = nowUTC()
	}
	return putRecord(s, tableGroups, g.ID, g)
}

func (s *Store) Groups() ([]*Group, error) {
	return listAll[Group](s, tableGroups)
}

func (s *Store) DeleteGroup(id string) error { return s.pod.Delete(tableGroups, id) }

// ---------------------------------------------------------------------------
// rules
// ---------------------------------------------------------------------------

func (s *Store) PutRule(r *Rule) error {
	if r.ID == "" {
		r.ID = "r-" + shortHash(r.Name)
	}
	if r.Created.IsZero() {
		r.Created = nowUTC()
	}
	if r.Severity == "" {
		r.Severity = "medium"
	}
	return putRecord(s, tableRules, r.ID, r)
}

func (s *Store) Rule(id string) (*Rule, error) { return getRecord[Rule](s, tableRules, id) }

func (s *Store) Rules() ([]*Rule, error) {
	out, err := listAll[Rule](s, tableRules)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) DeleteRule(id string) error { return s.pod.Delete(tableRules, id) }

// ---------------------------------------------------------------------------
// insights
// ---------------------------------------------------------------------------

func (s *Store) PutInsight(i *Insight) error {
	if i.ID == "" {
		i.ID = "ins-" + shortHash(i.Rule+"|"+nowUTC().String())
	}
	if i.Status == "" {
		i.Status = "new"
	}
	if i.Created.IsZero() {
		i.Created = nowUTC()
	}
	i.Updated = nowUTC()
	return putRecord(s, tableInsights, i.ID, i)
}

func (s *Store) Insight(id string) (*Insight, error) {
	return getRecord[Insight](s, tableInsights, id)
}

func (s *Store) Insights(status string) ([]*Insight, error) {
	var cond pod.Cond
	if status != "" && status != "all" {
		cond = eq("status", status)
	}
	out, err := listRecords[Insight](s, tableInsights, cond, nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// InsightByRule returns the existing insight for a rule, so re-running a rule
// updates its insight instead of piling up duplicates.
func (s *Store) InsightByRule(ruleID string) (*Insight, bool) {
	return findOne[Insight](s, tableInsights, "rule", ruleID)
}

func (s *Store) DeleteInsight(id string) error { return s.pod.Delete(tableInsights, id) }

// ---------------------------------------------------------------------------
// summary
// ---------------------------------------------------------------------------

// Counts reports the size of every table, for the dashboard and the console.
func (s *Store) Counts() map[string]int {
	out := map[string]int{}
	for _, t := range s.Tables() {
		ids, err := s.pod.IDs(t)
		if err != nil {
			out[t] = 0
			continue
		}
		out[t] = len(ids)
	}
	return out
}
