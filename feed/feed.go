// Package feed implements stenella's single-feed engine: many RSS/Atom/OPML
// sources per client are fetched, normalized and merged into one feed,
// newest-first, with lazy pagination. Parsing is done with encoding/xml — the
// standard library only. A feed cache lives on disk per client so the combined
// feed works offline and paginates instantly; the durable copy of every item
// is mirrored into the client's pod namespace through atp (see web package).
package feed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind of a data source.
const (
	KindRSS  = "rss"
	KindAtom = "atom"
	KindRDF  = "rdf"
	KindJSON = "json"
	KindOPML = "opml" // import-only
)

// Source describes one online data source a client subscribes to.
type Source struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Kind        string    `json:"kind"`
	Enabled     bool      `json:"enabled"`
	IntervalMin int       `json:"interval_min"`             // fetch cadence; 0 = default
	AuthSecret  string    `json:"auth_secret,omitempty"`    // name of a vault secret used as a bearer token
	Retention   int       `json:"retention_days,omitempty"` // 0 = default retention
	LastFetch   time.Time `json:"last_fetch"`
	LastStatus  string    `json:"last_status,omitempty"`
	LastCount   int       `json:"last_count"`
	ItemCount   int       `json:"item_count"`
	Added       time.Time `json:"added"`
}

// Item is one normalized element of the combined feed.
type Item struct {
	ID         string    `json:"id"` // stable hash of source+guid
	SourceID   string    `json:"source_id"`
	SourceName string    `json:"source_name"`
	Title      string    `json:"title"`
	Link       string    `json:"link"`
	GUID       string    `json:"guid"`
	Author     string    `json:"author,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	Content    string    `json:"content,omitempty"`
	Categories []string  `json:"categories,omitempty"`
	Published  time.Time `json:"published"`
	Updated    time.Time `json:"updated,omitempty"`
	Fetched    time.Time `json:"fetched"`
}

// FetchedFeed is the raw parse result of one source.
type FetchedFeed struct {
	Source *Source
	Items  []Item
}

// Engine stores sources and caches per client and performs fetches. It is safe
// for concurrent use and runs background refreshers.
type Engine struct {
	root string
	http *http.Client

	mu         sync.RWMutex
	byCli      map[string]*clientState
	refreshing map[string]bool
}

type clientState struct {
	file  string
	srcs  map[string]*Source
	cache map[string]*cacheFile // by source id
	order []string              // source ids, insertion order
}

type cacheFile struct {
	Updated time.Time
	Items   []Item
}

// Options configure the engine.
type Options struct {
	// Root is the stenella data root (feeds live under <root>/feeds).
	Root string
	// HTTP client used for fetching (optional; a default is created).
	HTTP *http.Client
}

// New creates the engine and loads existing source configs.
func New(opts Options) (*Engine, error) {
	if opts.Root == "" {
		opts.Root = "./data"
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	e := &Engine{
		root:       opts.Root,
		http:       opts.HTTP,
		byCli:      map[string]*clientState{},
		refreshing: map[string]bool{},
	}
	if err := os.MkdirAll(filepath.Join(e.root, "feeds"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(e.root, "cache"), 0o755); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) state(client string) *clientState {
	if s, ok := e.byCli[client]; ok {
		return s
	}
	dir := filepath.Join(e.root, "feeds")
	_ = os.MkdirAll(dir, 0o755)
	s := &clientState{
		file:  filepath.Join(dir, safe(client)+".json"),
		srcs:  map[string]*Source{},
		cache: map[string]*cacheFile{},
	}
	e.loadState(s)
	e.byCli[client] = s
	return s
}

func (e *Engine) loadState(s *clientState) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	var srcs []*Source
	if err := json.Unmarshal(data, &srcs); err != nil {
		return
	}
	order := make([]string, 0, len(srcs))
	for _, src := range srcs {
		if src == nil || src.ID == "" {
			continue
		}
		s.srcs[src.ID] = src
		order = append(order, src.ID)
		if cf := e.loadCache(src.ID); cf != nil {
			s.cache[src.ID] = cf
		}
	}
	s.order = order
}

// Save persists a client's source config.
func (e *Engine) saveState(client string, s *clientState) {
	out := make([]*Source, 0, len(s.order))
	for _, id := range s.order {
		if src, ok := s.srcs[id]; ok {
			out = append(out, src)
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	_ = atomicWrite(s.file, data)
}

func (e *Engine) loadCache(sourceID string) *cacheFile {
	p := filepath.Join(e.root, "cache", safe(sourceID)+".json")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil
	}
	return &cf
}

func (e *Engine) saveCache(sourceID string, cf *cacheFile) {
	data, err := json.Marshal(cf)
	if err != nil {
		return
	}
	_ = atomicWrite(filepath.Join(e.root, "cache", safe(sourceID)+".json"), data)
}

// ---- source management -------------------------------------------------------

// Sources lists a client's sources (active first, then by added time).
func (e *Engine) Sources(client string) []*Source {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state(client)
	out := make([]*Source, 0, len(s.order))
	for _, id := range s.order {
		if src, ok := s.srcs[id]; ok {
			out = append(out, src)
		}
	}
	return out
}

// GetSource returns one source or nil.
func (e *Engine) GetSource(client, id string) *Source {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state(client)
	if src, ok := s.srcs[id]; ok {
		return src
	}
	return nil
}

// AddSource registers a new source (auto-detects kind when kind == "").
func (e *Engine) AddSource(client, name, rawURL, kind string, interval int, authSecret string, retention int) (*Source, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid url %q", rawURL)
	}
	if kind == "" {
		kind = GuessKind(u.Path)
	}
	if kind == KindOPML {
		return nil, errors.New("opml is for bulk import; add sources from an OPML file instead")
	}
	if interval <= 0 {
		interval = defaultIntervalMin
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state(client)
	id := shortHash(client + "|" + rawURL)
	src := &Source{
		ID:          id,
		Name:        strings.TrimSpace(name),
		URL:         rawURL,
		Kind:        kind,
		Enabled:     true,
		IntervalMin: interval,
		AuthSecret:  authSecret,
		Retention:   retention,
		Added:       time.Now().UTC(),
	}
	if src.Name == "" {
		src.Name = strings.TrimSuffix(u.Hostname(), "/")
	}
	s.srcs[id] = src
	s.order = append(s.order, id)
	e.saveState(client, s)
	return src, nil
}

// AddSources registers several sources at once (used by OPML import).
func (e *Engine) AddSources(client string, srcs []Source) []*Source {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state(client)
	var out []*Source
	for i := range srcs {
		src := &srcs[i]
		src.ID = shortHash(client + "|" + src.URL)
		src.Enabled = true
		if src.IntervalMin <= 0 {
			src.IntervalMin = defaultIntervalMin
		}
		if src.Added.IsZero() {
			src.Added = time.Now().UTC()
		}
		if _, exists := s.srcs[src.ID]; exists {
			continue
		}
		if src.Name == "" {
			u, err := url.Parse(src.URL)
			if err == nil {
				src.Name = strings.TrimSuffix(u.Hostname(), "/")
			}
		}
		s.srcs[src.ID] = src
		s.order = append(s.order, src.ID)
		out = append(out, src)
	}
	if len(out) > 0 {
		e.saveState(client, s)
	}
	return out
}

// UpdateSource applies partial updates to a source and reports the result.
func (e *Engine) UpdateSource(client, id string, patch map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state(client)
	src, ok := s.srcs[id]
	if !ok {
		return errors.New("source not found")
	}
	if v, ok := patch["name"].(string); ok {
		src.Name = v
	}
	if v, ok := patch["enabled"].(bool); ok {
		src.Enabled = v
	}
	if v, ok := patch["interval_min"].(float64); ok && v > 0 {
		src.IntervalMin = int(v)
	}
	if v, ok := patch["auth_secret"].(string); ok {
		src.AuthSecret = v
	}
	e.saveState(client, s)
	return nil
}

// DeleteSource removes a source and its cache.
func (e *Engine) DeleteSource(client, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state(client)
	if _, ok := s.srcs[id]; !ok {
		return errors.New("source not found")
	}
	delete(s.srcs, id)
	for i, v := range s.order {
		if v == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	delete(s.cache, id)
	_ = os.Remove(filepath.Join(e.root, "cache", safe(id)+".json"))
	e.saveState(client, s)
	return nil
}

// ---- fetching -----------------------------------------------------------------

// SecretResolver resolves a named vault secret to a string. The engine never
// stores secret values; the caller (which has atp access) provides them.
type SecretResolver func(client, name string) (string, error)

// Fetch retrieves and parses one source. When the source names an auth secret
// the resolver is used to build the Authorization header.
func (e *Engine) Fetch(client string, src *Source, resolve SecretResolver) (*FetchedFeed, error) {
	fetched, err := e.fetchSource(client, src, resolve)
	if err == nil {
		e.mu.Lock()
		s := e.state(client)
		if _, exists := s.srcs[src.ID]; exists {
			src.LastFetch = time.Now().UTC()
			src.LastStatus = "ok"
			src.LastCount = len(fetched.Items)
			src.ItemCount = len(fetched.Items)
			e.saveState(client, s)
		}
		e.mu.Unlock()
		e.replaceCache(client, src.ID, fetched.Items)
		return fetched, nil
	}
	e.mu.Lock()
	s := e.state(client)
	if s2, ok := s.srcs[src.ID]; ok {
		s2.LastStatus = "error: " + brief(err.Error())
		e.saveState(client, s)
	}
	e.mu.Unlock()
	return nil, err
}

func (e *Engine) fetchSource(client string, src *Source, resolve SecretResolver) (*FetchedFeed, error) {
	req, err := http.NewRequest(http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "stenella/1.0 (+https://azzurro.tech)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml, application/json;q=0.5, */*;q=0.1")
	if src.AuthSecret != "" && resolve != nil {
		if val, rerr := resolve(client, src.AuthSecret); rerr == nil && val != "" {
			req.Header.Set("Authorization", "Bearer "+val)
		}
	}
	res, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("source returned %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	switch src.Kind {
	case KindJSON:
		items, err := ParseJSONFeed(body)
		if err != nil {
			return nil, err
		}
		return &FetchedFeed{Source: src, Items: enrich(client, src, items)}, nil
	case KindAtom:
		items, err := ParseAtom(body)
		if err != nil {
			return nil, err
		}
		return &FetchedFeed{Source: src, Items: enrich(client, src, items)}, nil
	default:
		items, err := ParseRSS(body)
		if err != nil {
			return nil, err
		}
		return &FetchedFeed{Source: src, Items: enrich(client, src, items)}, nil
	}
}

// enrich stamps the source identity and the stable item id onto fresh parse
// results. The id (a hash of client+source+guid+link) is what makes mirrored
// pod records addressable and de-duplicated across refreshes.
func enrich(client string, src *Source, items []Item) []Item {
	for i := range items {
		if items[i].ID == "" {
			items[i].ID = itemID(client, src.ID, items[i].GUID, items[i].Link)
		}
		if items[i].SourceID == "" {
			items[i].SourceID = src.ID
		}
		if items[i].SourceName == "" {
			items[i].SourceName = src.Name
		}
	}
	return items
}

// FetchAll refreshes every enabled source of a client, skipping sources that
// were fetched within their interval unless force is set. Returns per-source
// results (ok/err compact for status display).
func (e *Engine) FetchAll(client string, resolver SecretResolver, force bool) []Result {
	e.mu.Lock()
	s := e.state(client)
	work := make([]*Source, 0, len(s.order))
	for _, id := range s.order {
		if src, ok := s.srcs[id]; ok && src.Enabled {
			work = append(work, src)
		}
	}
	e.mu.Unlock()

	var out []Result
	anyRefreshing := false
	for _, src := range work {
		if !force {
			e.mu.RLock()
			last := src.LastFetch
			e.mu.RUnlock()
			if time.Since(last) < time.Duration(src.IntervalMin)*time.Minute {
				out = append(out, Result{Source: src.Name, Status: "cached"})
				continue
			}
		}
		key := client + "/" + src.ID
		e.mu.Lock()
		if e.refreshing[key] {
			anyRefreshing = true
			e.mu.Unlock()
			out = append(out, Result{Source: src.Name, Status: "already refreshing"})
			continue
		}
		e.refreshing[key] = true
		e.mu.Unlock()

		res := Result{Source: src.Name}
		ff, err := e.Fetch(client, src, resolver)
		if err != nil {
			res.Status = "error: " + brief(err.Error())
		} else {
			res.Status = fmt.Sprintf("ok (%d items)", len(ff.Items))
		}
		out = append(out, res)

		e.mu.Lock()
		delete(e.refreshing, key)
		e.mu.Unlock()
	}
	_ = anyRefreshing
	return out
}

// Result is the outcome of refreshing one source.
type Result struct {
	Source string `json:"source"`
	Status string `json:"status"`
}

// Stale returns whether any enabled source of a client is due for a refresh.
func (e *Engine) Stale(client string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.byCli[client]
	if !ok {
		return false
	}
	for _, id := range s.order {
		if src, ok := s.srcs[id]; ok && src.Enabled {
			if time.Since(src.LastFetch) >= time.Duration(src.IntervalMin)*time.Minute {
				return true
			}
		}
	}
	return false
}

// replaceCache stores fetched items under a source and prunes stale entries.
func (e *Engine) replaceCache(client, sourceID string, items []Item) {
	now := time.Now().UTC()
	for i := range items {
		if items[i].Fetched.IsZero() {
			items[i].Fetched = now
		}
	}
	e.mu.Lock()
	s := e.state(client)
	src := s.srcs[sourceID]
	retention := defaultRetentionDays
	if src != nil && src.Retention > 0 {
		retention = src.Retention
	}
	cutoff := now.Add(-time.Duration(retention) * 24 * time.Hour)
	kept := items[:0]
	for _, it := range items {
		if it.Published.After(cutoff) || it.Updated.After(cutoff) {
			kept = append(kept, it)
		}
	}
	cf := &cacheFile{Updated: now, Items: kept}
	s.cache[sourceID] = cf
	e.saveCache(sourceID, cf)
	e.mu.Unlock()
}

// ---- combined feed ------------------------------------------------------------

// Query filters the combined feed (all sources of a client).
type Query struct {
	Page     int       // 1-based
	PageSize int       // <= 0 → defaultPageSize
	Q        string    // free-text across title/summary/categories
	Source   string    // restrict to one source
	Category string    // restrict to a category
	Since    time.Time // only items published after
}

// Page is a slice of the combined feed.
type Page struct {
	Items   []Item `json:"items"`
	Total   int    `json:"total"`
	Page    int    `json:"page"`
	HasMore bool   `json:"has_more"`
}

// Combined merges every source's cached items, newest-first.
func (e *Engine) Combined(client string, q Query) Page {
	e.mu.RLock()
	s, ok := e.byCli[client]
	if !ok {
		e.mu.RUnlock()
		return Page{Page: q.Page}
	}
	all := make([]Item, 0, 64)
	seen := map[string]bool{}
	for _, id := range s.order {
		src, ok := s.srcs[id]
		if !ok || !src.Enabled {
			continue
		}
		if q.Source != "" && q.Source != src.ID {
			continue
		}
		cf, ok := s.cache[id]
		if !ok {
			continue
		}
		for _, it := range cf.Items {
			if seen[it.ID] {
				continue
			}
			if !q.Since.IsZero() && it.Published.Before(q.Since) {
				continue
			}
			if q.Category != "" && !containsStr(it.Categories, q.Category) {
				continue
			}
			if q.Q != "" && !itemMatches(it, q.Q) {
				continue
			}
			seen[it.ID] = true
			all = append(all, it)
		}
	}
	e.mu.RUnlock()

	sort.SliceStable(all, func(i, j int) bool {
		a, b := itemTime(all[i]), itemTime(all[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return all[i].ID < all[j].ID
	})

	total := len(all)
	pageSize := q.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return Page{
		Items:   all[start:end],
		Total:   total,
		Page:    page,
		HasMore: end < total,
	}
}

// Categories returns the union of categories across a client's feed, sorted.
func (e *Engine) Categories(client string) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.byCli[client]
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, id := range s.order {
		if cf, ok := s.cache[id]; ok {
			for _, it := range cf.Items {
				for _, c := range it.Categories {
					if c != "" {
						set[c] = true
					}
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// ItemByID returns a single item from any enabled source (or nil).
func (e *Engine) ItemByID(client, id string) *Item {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.byCli[client]
	if !ok {
		return nil
	}
	for _, srcID := range s.order {
		cf, ok := s.cache[srcID]
		if !ok {
			continue
		}
		for i := range cf.Items {
			if cf.Items[i].ID == id {
				cp := cf.Items[i]
				return &cp
			}
		}
	}
	return nil
}

// ---- constants + helpers ------------------------------------------------------

const (
	defaultIntervalMin   = 15
	defaultPageSize      = 40
	defaultRetentionDays = 30
)

// PageSizeDefault is the default combined-feed page size used by handlers.
func PageSizeDefault() int { return defaultPageSize }

// GuessKind guesses the feed type from a path.
func GuessKind(p string) string {
	lower := strings.ToLower(p)
	switch {
	case strings.HasSuffix(lower, ".atom"), strings.HasSuffix(lower, ".atom.xml"):
		return KindAtom
	case strings.HasSuffix(lower, ".opml"):
		return KindOPML
	case strings.HasSuffix(lower, ".json"), strings.Contains(lower, "json"):
		return KindJSON
	default:
		return KindRSS
	}
}

func itemMatches(it Item, q string) bool {
	needle := strings.ToLower(q)
	hay := strings.ToLower(it.Title + "\n" + it.Summary + "\n" + it.Content + "\n" + it.Author + "\n" + strings.Join(it.Categories, " "))
	return strings.Contains(hay, needle)
}

func itemTime(it Item) time.Time {
	if !it.Published.IsZero() {
		return it.Published
	}
	if !it.Updated.IsZero() {
		return it.Updated
	}
	return it.Fetched
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func brief(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

// shortHash returns the first 16 hex chars of sha256(input).
func shortHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:16]
}

// itemID builds the stable identifier for an item.
func itemID(client, sourceID, guid, link string) string {
	key := client + "|" + sourceID + "|" + guid + "|" + link
	return shortHash(key)
}

// safe sanitizes a string for use as a filename segment.
func safe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "x"
	}
	return out
}

type xmlTime struct{ time.Time }

func (t *xmlTime) UnmarshalXMLAttr(attr xml.Attr) error {
	v := strings.TrimSpace(attr.Value)
	if v == "" {
		return nil
	}
	parsed, err := parseAnyTime(v)
	if err != nil {
		t.Time = time.Now().UTC() // tolerate junk dates
		return nil
	}
	t.Time = parsed
	return nil
}

func parseAnyTime(v string) (time.Time, error) {
	layouts := []string{
		time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822,
		"2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05", "2006-01-02",
		"Mon, 2 Jan 2006 15:04:05 -0700", "2006-01-02T15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, v); err == nil {
			return t, nil
		}
	}
	// RFC1123 sometimes with single-digit day without leading zero.
	for _, l := range []string{time.RFC1123Z, time.RFC1123} {
		if t, err := time.Parse(strings.ReplaceAll(l, "02", "2"), v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("unsupported time format")
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// Run refreshes stale sources in the background until ctx is done.
func (e *Engine) Run(ctx context.Context, resolver SecretResolver) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			clients := e.clientList()
			for _, c := range clients {
				if e.Stale(c) {
					e.FetchAll(c, resolver, false)
				}
			}
		}
	}
}

// Clients returns every client the engine knows about (it may lazily create
// state entries while scanning).
func (e *Engine) Clients() []string {
	return e.clientList()
}

func (e *Engine) clientList() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	set := map[string]bool{}
	for c := range e.byCli {
		set[c] = true
	}
	entries, err := os.ReadDir(filepath.Join(e.root, "feeds"))
	if err == nil {
		for _, ent := range entries {
			if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".json") {
				set[strings.TrimSuffix(ent.Name(), ".json")] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
