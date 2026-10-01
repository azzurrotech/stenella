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
	"path"
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
	AclClass    string    `json:"acl_class,omitempty"`      // "public", "private", "protected" for SHEPHERD classification
	Retention   int       `json:"retention_days,omitempty"` // 0 = default retention
	LastFetch   time.Time `json:"last_fetch"`
	LastStatus  string    `json:"last_status,omitempty"`
	LastCount   int       `json:"last_count"`
	ItemCount   int       `json:"item_count"`
	Added       time.Time `json:"added"`
}

// Item is one normalized element of the combined feed.
//
// Title, Summary and Content are the item's *plaintext* fields. They are never
// serialized: what reaches the cache file and the pod record are the matching
// ciphertext fields, and the plaintext fields are populated on read by the
// cipher. Keeping them as separate fields is what lets ACL gating, retention
// and envelope search run over metadata while the bodies stay opaque — see
// plan §4.4. A caller writing an Item should fill the plaintext fields and let
// the cache layer seal them.
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

	// Body bytes as stored. TitleEnc/SummaryEnc/ContentEnc are vici-compatible
	// AES-256-GCM envelopes (see the crypt package). These are what is on disk;
	// the plaintext fields above are populated from them on read and cleared on
	// write. ContentBytes records the plaintext length so an envelope can be
	// sized in a UI without being opened.
	TitleEnc     string `json:"title_enc,omitempty"`
	SummaryEnc   string `json:"summary_enc,omitempty"`
	ContentEnc   string `json:"content_enc,omitempty"`
	ContentBytes int    `json:"content_bytes,omitempty"`

	// ACLClass is the item's effective access class, stamped at ingest from the
	// owning source. It travels with the item rather than being resolved from
	// the source on read, so reclassifying a source never rewrites history and a
	// cached public item cannot be served after its source is tightened.
	ACLClass string `json:"acl_class,omitempty"`

	// Pinned marks an item the client explicitly kept, which exempts it from the
	// retention sweep. The authoritative pin record lives in pod; this is the
	// denormalized copy the engine reads when deciding what to prune.
	Pinned bool `json:"pinned,omitempty"`

	// CommentCount is the number of comment records referencing this item. A
	// commented-on item is also exempt from retention.
	CommentCount int `json:"comment_count,omitempty"`

	// Source carries the subset of the owning source that a reader needs in
	// order to group, label and gate the item: which feed it came from, that
	// feed's address, and the ACL class SHEPHERD classifies it under. Items are
	// cached per source and can outlive an in-memory source lookup, so the
	// classification travels with the item rather than being resolved on read.
	Source *ItemSource `json:"source,omitempty"`
}

// Envelope is the wire form of an item for a caller that holds the content key:
// envelope metadata in the clear, bodies as ciphertext, and no plaintext body at
// all.
//
// The portal API serves this rather than an Item. Two reasons, and they pull in
// the same direction:
//
//   - Symmetry. The browser encrypts what it writes (comments, and anything else
//     a client authors) and receives what it reads in the same envelope format,
//     so there is exactly one thing to implement on each side.
//   - The boundary stays visible. An Item serialised directly would carry a
//     plaintext body the moment a handler forgot which view it was using. An
//     Envelope has no field to forget.
//
// The public feed endpoints do not use this: a public reader holds no key, and
// public-class items are public by definition. They are server-rendered from
// Items, restricted to the public class at the query.
type Envelope struct {
	ID         string    `json:"id"`
	SourceID   string    `json:"source_id,omitempty"`
	SourceName string    `json:"source_name,omitempty"`
	Link       string    `json:"link,omitempty"`
	GUID       string    `json:"guid,omitempty"`
	Author     string    `json:"author,omitempty"`
	Categories []string  `json:"categories,omitempty"`
	Published  time.Time `json:"published"`
	Updated    time.Time `json:"updated,omitempty"`
	Fetched    time.Time `json:"fetched"`

	TitleEnc   string `json:"title_enc,omitempty"`
	SummaryEnc string `json:"summary_enc,omitempty"`
	ContentEnc string `json:"content_enc,omitempty"`
	// ContentBytes is the plaintext body length, so a UI can size or
	// word-count a body it cannot read.
	ContentBytes int `json:"content_bytes,omitempty"`

	ACLClass     string      `json:"acl_class,omitempty"`
	Pinned       bool        `json:"pinned,omitempty"`
	CommentCount int         `json:"comment_count,omitempty"`
	Source       *ItemSource `json:"source,omitempty"`
}

// Envelope projects an item onto its wire form. The plaintext fields are
// deliberately not copied: if an envelope is empty the body was never sealed,
// and the caller learns that rather than receiving a silent plaintext copy.
func (it Item) Envelope() Envelope {
	return Envelope{
		ID: it.ID, SourceID: it.SourceID, SourceName: it.SourceName,
		Link: it.Link, GUID: it.GUID, Author: it.Author, Categories: it.Categories,
		Published: it.Published, Updated: it.Updated, Fetched: it.Fetched,
		TitleEnc: it.TitleEnc, SummaryEnc: it.SummaryEnc, ContentEnc: it.ContentEnc,
		ContentBytes: it.ContentBytes, ACLClass: itemACL(it),
		Pinned: it.Pinned, CommentCount: it.CommentCount, Source: it.Source,
	}
}

// Envelopes projects a page of items, preserving order.
func Envelopes(items []Item) []Envelope {
	out := make([]Envelope, 0, len(items))
	for _, it := range items {
		out = append(out, it.Envelope())
	}
	return out
}

// ItemSource is the per-item projection of a feed Source. Only fields that are
// safe to expose to a reader are present.
type ItemSource struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url,omitempty"`
	Kind     string `json:"kind,omitempty"`
	AclClass string `json:"acl_class"`
}

// itemSource projects a source into the form items carry.
func itemSource(src *Source) *ItemSource {
	if src == nil {
		return nil
	}
	return &ItemSource{
		ID:       src.ID,
		Name:     src.Name,
		URL:      src.URL,
		Kind:     src.Kind,
		AclClass: NormalizeACLClass(src.AclClass),
	}
}

// FetchedFeed is the raw parse result of one source.
type FetchedFeed struct {
	Source *Source
	Items  []Item
}

// Cipher seals and opens item bodies for at-rest storage. It is an interface so
// the engine does not depend on a key-management implementation: the web layer
// supplies the crypt.KeyStore, and tests can supply a pass-through.
type Cipher interface {
	// Seal returns a vici-compatible ciphertext envelope for plaintext.
	Seal(client, plaintext string) (string, error)
	// Open reverses Seal. An unreadable payload is an error, not empty text.
	Open(client, payload string) (string, error)
}

// Engine stores sources and caches per client and performs fetches. It is safe
// for concurrent use and runs background refreshers.
type Engine struct {
	root string
	http *http.Client
	// now is the engine's clock. See Options.Now.
	now func() time.Time
	// cipher seals item bodies on write and opens them on read. When nil the
	// engine still works but stores plaintext, which is only appropriate for
	// tests — production always wires crypt.KeyStore.
	cipher Cipher

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

// itemFields are the three body fields, paired as (plaintext, ciphertext).
// Everything that has to seal, open or measure a body iterates this list rather
// than naming the three separately, so adding a field cannot leave one path
// unencrypted.
type itemField struct {
	plain  *string
	cipher *string
}

func (it *Item) bodyFields() []itemField {
	return []itemField{
		{&it.Title, &it.TitleEnc},
		{&it.Summary, &it.SummaryEnc},
		{&it.Content, &it.ContentEnc},
	}
}

// seal encrypts every plaintext body field in place and clears it, so a
// plaintext value cannot survive into a struct that is about to be written.
// ContentBytes is recorded first so the plaintext length stays knowable after
// the text is gone.
func (e *Engine) seal(client string, it *Item) error {
	if e.cipher == nil {
		return nil
	}
	it.ContentBytes = len(it.Content)
	var firstErr error
	for _, f := range it.bodyFields() {
		if *f.plain == "" {
			*f.cipher = ""
			continue
		}
		enc, err := e.cipher.Seal(client, *f.plain)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		*f.cipher = enc
		*f.plain = ""
	}
	return firstErr
}

// open decrypts every ciphertext body field in place. A payload that will not
// decrypt is left as an empty body rather than aborting the whole load: one bad
// record should not take a client's entire feed offline.
func (e *Engine) open(client string, it *Item) {
	if e.cipher == nil {
		return
	}
	for _, f := range it.bodyFields() {
		if *f.cipher == "" {
			continue
		}
		pt, err := e.cipher.Open(client, *f.cipher)
		if err != nil {
			*f.plain = ""
			continue
		}
		*f.plain = pt
	}
}

// Options configure the engine.
type Options struct {
	// Root is the stenella data root (feeds live under <root>/feeds).
	Root string
	// HTTP client used for fetching (optional; a default is created).
	HTTP *http.Client
	// Cipher seals item bodies at rest (optional; see Cipher).
	Cipher Cipher
	// Now is the clock (optional; defaults to time.Now). Retention is decided by
	// comparing item timestamps against "now", so without a seam there is no way
	// to test a sweep without waiting a retention window. Production never sets
	// it.
	Now func() time.Time
}

// New creates the engine and loads existing source configs.
func New(opts Options) (*Engine, error) {
	if opts.Root == "" {
		opts.Root = "./data"
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	e := &Engine{
		root:       opts.Root,
		http:       opts.HTTP,
		cipher:     opts.Cipher,
		now:        opts.Now,
		byCli:      map[string]*clientState{},
		refreshing: map[string]bool{},
	}
	if err := os.MkdirAll(filepath.Join(e.root, "feeds"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(e.root, "cache"), 0o755); err != nil {
		return nil, err
	}
	// Load every client already on disk up front. A read path that has not yet
	// touched a client still has to see its cached items — otherwise a restart
	// would serve an empty feed until something happened to write, and the
	// bodies have to be opened here, once, rather than per request.
	e.loadAll()
	return e, nil
}

// loadAll eagerly builds the state for every client with a feed file.
func (e *Engine) loadAll() {
	dir := filepath.Join(e.root, "feeds")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		client := strings.TrimSuffix(ent.Name(), ".json")
		if client == "" || client == "x" {
			continue
		}
		if _, ok := e.byCli[client]; ok {
			continue
		}
		s := &clientState{
			file:  filepath.Join(dir, ent.Name()),
			srcs:  map[string]*Source{},
			cache: map[string]*cacheFile{},
		}
		e.loadState(client, s)
		e.byCli[client] = s
	}
}

// nowUTC is the engine's clock, UTC-normalised. Every timestamp the engine
// writes goes through it, so a test can move the clock without moving the items
// it is comparing against.
func (e *Engine) nowUTC() time.Time { return e.now().UTC() }

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
	e.loadState(client, s)
	e.byCli[client] = s
	return s
}

func (e *Engine) loadState(client string, s *clientState) {
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
		// Sources written before acl_class existed decode with an empty string.
		// Normalise on read so every in-memory source is classified.
		src.AclClass = NormalizeACLClass(src.AclClass)
		s.srcs[src.ID] = src
		order = append(order, src.ID)
		if cf := e.loadCache(src.ID); cf != nil {
			e.openCache(client, cf, src.AclClass)
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

// openCache decrypts a freshly loaded cache file. Items written before item
// encryption existed have plaintext bodies and no envelope; those are left as
// they are and will be re-sealed on the next fetch.
func (e *Engine) openCache(client string, cf *cacheFile, aclClass string) {
	if cf == nil {
		return
	}
	for i := range cf.Items {
		it := &cf.Items[i]
		if it.ACLClass == "" {
			it.ACLClass = NormalizeACLClass(aclClass)
		}
		e.open(client, it)
	}
}

// saveCache seals every body in the file before writing it, then copies the
// envelopes back onto the in-memory items. The caller's items stay usable — they
// keep their plaintext *and* gain the envelope that is now on disk, which is the
// invariant Item.Envelope relies on: an in-memory item and its stored form
// always describe the same content, so a caller can hand a browser ciphertext
// whether the item was just fetched or loaded at startup.
func (e *Engine) saveCache(client, sourceID string, cf *cacheFile) {
	// Seal into a copy so the in-memory cache the engine keeps serving from is
	// not blanked as a side effect of persisting it.
	stored := &cacheFile{Updated: cf.Updated, Items: make([]Item, len(cf.Items))}
	copy(stored.Items, cf.Items)
	var sealErr error
	for i := range stored.Items {
		if err := e.seal(client, &stored.Items[i]); err != nil && sealErr == nil {
			sealErr = err
		}
	}
	// Mirror the envelopes back. The copy preserved order, so the two slices line
	// up index for index; sealing a copy is what let the live items keep their
	// plaintext.
	for i := range stored.Items {
		if i >= len(cf.Items) {
			break
		}
		cf.Items[i].ContentBytes = stored.Items[i].ContentBytes
		live := cf.Items[i].bodyFields()
		for k, f := range stored.Items[i].bodyFields() {
			*live[k].cipher = *f.cipher
		}
	}
	if sealErr != nil {
		// A body that will not seal must not be written in the clear. Drop the
		// affected items rather than persist plaintext.
		kept := stored.Items[:0]
		for _, it := range stored.Items {
			if it.Title != "" || it.Summary != "" || it.Content != "" {
				continue // seal failed: plaintext still present
			}
			kept = append(kept, it)
		}
		stored.Items = kept
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return
	}
	_ = atomicWrite(filepath.Join(e.root, "cache", safe(sourceID)+".json"), data)
}

// ---- source management -------------------------------------------------------

// Sources lists a client's sources (active first, then by added time).
// Sources returns a snapshot of a client's sources.
//
// The returned sources are copies, not the engine's own pointers. Callers read
// them after the lock is released, while a concurrent Fetch is free to update
// LastFetch/LastCount on the stored value — handing out the live pointers would
// make every such read a data race.
func (e *Engine) Sources(client string) []*Source {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state(client)
	out := make([]*Source, 0, len(s.order))
	for _, id := range s.order {
		if src, ok := s.srcs[id]; ok {
			cp := *src
			out = append(out, &cp)
		}
	}
	return out
}

// GetSource returns a snapshot of one source, or nil. Like Sources, the result
// is a copy the caller may hold without holding the engine's lock.
func (e *Engine) GetSource(client, id string) *Source {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state(client)
	if src, ok := s.srcs[id]; ok {
		cp := *src
		return &cp
	}
	return nil
}

// ACL classes a source can be filed under. These mirror the three access
// levels SHEPHERD's directory tree enforces; a source's class decides who sees
// the items it produces on the public feed.
const (
	ACLPublic    = "public"
	ACLPrivate   = "private"
	ACLProtected = "protected"
)

// ValidACLClass reports whether c is one of the three known classes. An empty
// string is not valid here: callers normalise it to ACLPublic first, so that
// every persisted source carries an explicit class rather than relying on the
// reader to guess what the zero value meant.
func ValidACLClass(c string) bool {
	switch c {
	case ACLPublic, ACLPrivate, ACLProtected:
		return true
	}
	return false
}

// NormalizeACLClass maps an unset or unrecognised class onto a safe default.
// Anything that is not explicitly private or protected is treated as public,
// which is the only class whose contents the unauthenticated endpoint serves.
func NormalizeACLClass(c string) string {
	if ValidACLClass(c) {
		return c
	}
	return ACLPublic
}

// AddSource registers a new source (auto-detects kind when kind == "").
func (e *Engine) AddSource(client, name, rawURL, kind string, interval int, authSecret string, retention int) (*Source, error) {
	return e.AddSourceClassified(client, name, rawURL, kind, interval, authSecret, retention, ACLPublic)
}

// AddSourceClassified is AddSource with an explicit ACL class. A blank or
// unknown class is normalised to public so a source is never left unclassified.
func (e *Engine) AddSourceClassified(client, name, rawURL, kind string, interval int, authSecret string, retention int, aclClass string) (*Source, error) {
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
		AclClass:    NormalizeACLClass(aclClass),
		Added:       e.nowUTC(),
	}
	if src.Name == "" {
		src.Name = strings.TrimSuffix(u.Hostname(), "/")
	}
	s.srcs[id] = src
	s.order = append(s.order, id)
	e.saveState(client, s)
	// Return a copy. Callers typically hand the result straight to a JSON
	// response while the eager first fetch is already running in the
	// background and writing status fields onto the stored source; returning
	// the live pointer would make that serialisation race with the write.
	out := *src
	return &out, nil
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
			src.Added = e.nowUTC()
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
		src.AclClass = NormalizeACLClass(src.AclClass)
		s.srcs[src.ID] = src
		s.order = append(s.order, src.ID)
		// Copy out for the same reason AddSource does: the stored pointer is
		// mutated by later fetches while the caller may still be reading it.
		cp := *src
		out = append(out, &cp)
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
	if v, ok := patch["acl_class"].(string); ok {
		cls := strings.ToLower(strings.TrimSpace(v))
		if !ValidACLClass(cls) {
			return errors.New("acl_class must be public, private or protected")
		}
		src.AclClass = cls
	}
	if v, ok := patch["retention_days"].(float64); ok && v >= 0 {
		src.Retention = int(v)
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
		// Update the stored source, not the caller's snapshot. Callers get a
		// copy from Sources/GetSource, so writing to src would silently drop
		// the status fields instead of persisting them.
		if stored, exists := s.srcs[src.ID]; exists {
			stored.LastFetch = e.nowUTC()
			stored.LastStatus = "ok"
			stored.LastCount = len(fetched.Items)
			stored.ItemCount = len(fetched.Items)
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
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/feed+json, application/xml, text/xml, application/json;q=0.8, text/html;q=0.5, */*;q=0.1")
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
	items, err := e.parseBody(src, res.Header.Get("Content-Type"), body)
	if err != nil {
		return nil, err
	}
	return &FetchedFeed{Source: src, Items: enrich(client, src, items)}, nil
}

// parseBody turns a fetched body into items according to the source's declared
// kind, falling back to sniffing when the kind alone cannot decide.
//
// The fallback matters because GuessKind works from a URL and servers do not
// always agree with URLs: a bare domain that turns out to be Atom, an extension
// that actually serves a page, a page with no headline at all. When the body is
// HTML the page is scraped rather than reported as malformed — but if the page
// yields nothing usable, the scrape error is what the source status reports.
func (e *Engine) parseBody(src *Source, contentType string, body []byte) ([]Item, error) {
	switch src.Kind {
	case KindJSON:
		return ParseJSONFeed(body)
	case KindAtom:
		return ParseAtom(body)
	case KindSite:
		// A source declared as a page may still be serving a document — a
		// publisher who later added a feed at the same URL. Prefer the feed
		// parsers in that case, because a whole feed is strictly more useful than
		// one scraped headline.
		if !looksLikeHTML(contentType, body) {
			if items, err := ParseAtom(body); err == nil {
				return items, nil
			}
			if items, err := ParseRSS(body); err == nil {
				return items, nil
			}
			if sniffJSON(body) {
				return ParseJSONFeed(body)
			}
			return nil, fmt.Errorf("site source %s returned a document that is neither a page nor a feed", src.URL)
		}
		return e.scrapeBytes(src.URL, contentType, body)
	case KindRSS:
		items, err := ParseRSS(body)
		if err == nil {
			return items, nil
		}
		if !looksLikeHTML(contentType, body) {
			return nil, err
		}
		// An HTML body for a source declared RSS: scrape it rather than lose the
		// source entirely. If the page has no headline the scrape error wins,
		// because "no headline in the page" is more useful than "XML syntax error".
		return e.scrapeBytes(src.URL, contentType, body)
	default:
		switch {
		case looksLikeHTML(contentType, body):
			return e.scrapeBytes(src.URL, contentType, body)
		case sniffJSON(body):
			return ParseJSONFeed(body)
		default:
			return ParseAtom(body)
		}
	}
}

// enrich stamps the source identity and the stable item id onto fresh parse
// results. The id (a hash of client+source+guid+link) is what makes mirrored
// pod records addressable and de-duplicated across refreshes.
func enrich(client string, src *Source, items []Item) []Item {
	proj := itemSource(src)
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
		// Stamp the classification on every item. An item that already carries a
		// source (re-fetch of an existing entry) keeps its own, so a class change
		// on the source does not retroactively rewrite history in the cache.
		if items[i].Source == nil {
			items[i].Source = proj
		}
		if items[i].ACLClass == "" {
			items[i].ACLClass = NormalizeACLClass(src.AclClass)
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
	now := e.nowUTC()
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
	// Record the plaintext body length on the in-memory item as well: the cache
	// writer seals a copy, and the UI sizes a body it cannot read from this
	// number.
	for i := range kept {
		kept[i].ContentBytes = len(kept[i].Content)
	}
	cf := &cacheFile{Updated: now, Items: kept}
	s.cache[sourceID] = cf
	e.saveCache(client, sourceID, cf)
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
	// AclClass restricts results to one SHEPHERD access class. Empty means no
	// restriction, which is what an authenticated caller wants. Public
	// endpoints must set it to ACLPublic explicitly rather than leaving it
	// empty — see AllowedACL.
	AclClass string
}

// itemACL reports the effective class of an item: the class stamped on the item
// itself, falling back to its source projection and then to public for records
// cached before classification existed.
func itemACL(it Item) string {
	if it.ACLClass != "" {
		return NormalizeACLClass(it.ACLClass)
	}
	if it.Source == nil {
		return ACLPublic
	}
	return NormalizeACLClass(it.Source.AclClass)
}

// AllowedACL reports whether an item's class may be served to a caller that is
// cleared for the given classes. An empty allowed set means "public only",
// which is the correct default for any unauthenticated read path.
func AllowedACL(it Item, allowed ...string) bool {
	if len(allowed) == 0 {
		return itemACL(it) == ACLPublic
	}
	cls := itemACL(it)
	for _, a := range allowed {
		if a == cls {
			return true
		}
	}
	return false
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
			// Gate on the item's own stamped class, not the source's current
			// one: an item cached as public must not be served to a public
			// reader after its source is reclassified.
			if q.AclClass != "" && itemACL(it) != NormalizeACLClass(q.AclClass) {
				continue
			}
			if !q.Since.IsZero() && itemTime(it).Before(q.Since) {
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

// Expired returns the ids of a source's cached items published before cutoff.
// It is the candidate set for a retention sweep: the caller decides which of
// them are exempt. Disabled sources still report, because an item that was
// fetched before the source was turned off is exactly the kind of thing
// retention exists to clean up.
func (e *Engine) Expired(client, sourceID string, cutoff time.Time) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.byCli[client]
	if !ok {
		return nil
	}
	cf, ok := s.cache[sourceID]
	if !ok {
		return nil
	}
	var out []string
	for _, it := range cf.Items {
		t := itemTime(it)
		if t.IsZero() || t.Before(cutoff) {
			out = append(out, it.ID)
		}
	}
	sort.Strings(out)
	return out
}

// Prune drops the given ids from a source's cached items and rewrites the cache
// file. Unknown ids are ignored so a caller can pass a superset.
func (e *Engine) Prune(client, sourceID string, ids []string) {
	if len(ids) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.byCli[client]
	if !ok {
		return
	}
	cf, ok := s.cache[sourceID]
	if !ok {
		return
	}
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	kept := make([]Item, 0, len(cf.Items))
	removed := false
	for _, it := range cf.Items {
		if drop[it.ID] {
			removed = true
			continue
		}
		kept = append(kept, it)
	}
	if !removed {
		return
	}
	cf.Items = kept
	e.saveCache(client, sourceID, cf)
}

// ItemTitle returns one item's plaintext title and link. It exists for callers
// that need to *display* a title (the link list, a share page) rather than store
// one: item bodies are encrypted at rest, so the only plaintext copy is the one
// this engine holds in memory.
func (e *Engine) ItemTitle(client, id string) (title, link string, ok bool) {
	it := e.ItemByID(client, id)
	if it == nil {
		return "", "", false
	}
	return it.Title, it.Link, true
}

// Annotate stamps the collaboration facts the retention sweep and the UI need
// onto a page of items: whether each is pinned and how many comments it has.
// It is a single pass over the supplied sets rather than a lookup per item.
func (e *Engine) Annotate(items []Item, pinned map[string]bool, commentCounts map[string]int) {
	for i := range items {
		if pinned[items[i].ID] {
			items[i].Pinned = true
		}
		if n, ok := commentCounts[items[i].ID]; ok {
			items[i].CommentCount = n
		}
	}
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
	raw := strings.ToLower(p)
	// A "json" anywhere in the URL wins outright: publishers put it in the query
	// string more often than in the extension ("/api?format=json").
	if strings.Contains(raw, "json") {
		return KindJSON
	}
	// The remaining extensions are checked on the path alone — a query string
	// does not hide the file type, and "feed.atom?id=7" is still Atom.
	lower := raw
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	switch {
	case strings.HasSuffix(lower, ".atom"), strings.HasSuffix(lower, ".atom.xml"):
		return KindAtom
	case strings.HasSuffix(lower, ".opml"):
		return KindOPML
	case strings.HasSuffix(lower, ".json"):
		return KindJSON
	case looksLikeFeedPath(lower):
		return KindRSS
	default:
		// A bare URL is a page until proven otherwise. The fetch path still tries
		// the XML parsers first, so a site that does serve Atom or RSS at a
		// extensionless URL is not lost — this only decides what the source is
		// *called* before the first fetch.
		return KindSite
	}
}

// looksLikeFeedPath reports whether a URL looks like it points at a feed document
// rather than at a page. It is only a naming heuristic — the actual parse is
// decided by the body — but the default matters: anything not recognisably a feed
// is called a site so a client sees what it subscribed to.
func looksLikeFeedPath(lower string) bool {
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	// A directory URL is a page, not a feed.
	if strings.HasSuffix(lower, "/") {
		return false
	}
	switch path.Ext(lower) {
	case ".rss", ".rdf", ".xml":
		return true
	}
	// "feed" and friends as the last path segment, because that is the near
	// universal convention and it beats guessing "blog/latest" is a page.
	switch seg := lower[strings.LastIndex(lower, "/")+1:]; seg {
	case "feed", "rss", "atom", "index.xml", "feeds":
		return true
	}
	return false
}

// itemMatches performs envelope-only matching. It deliberately does not read
// Title/Summary/Content: the bodies are encrypted at rest and the plan forbids
// server-side plaintext search, so a server query can only match what is in
// the clear — source, categories, author, link, guid and the timestamps. Full
// text search over decrypted bodies runs in the browser (plan §4.4).
func itemMatches(it Item, q string) bool {
	needle := strings.ToLower(strings.TrimSpace(q))
	if needle == "" {
		return true
	}
	fields := []string{
		it.SourceName, it.Author, it.Link, it.GUID, it.ID,
		strings.Join(it.Categories, " "),
	}
	if it.Source != nil {
		fields = append(fields, it.Source.Name, it.Source.URL)
	}
	hay := strings.ToLower(strings.Join(fields, "\n"))
	return strings.Contains(hay, needle)
}

// MetadataSearchable reports whether a query can be answered from the envelope
// alone. Handlers use it to tell a reader that a hit list came back short
// because the bodies are encrypted, instead of implying there were no matches.
func MetadataSearchable(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" {
		return true
	}
	// Anything the browser will decrypt is body text; anything that looks like
	// metadata still matches server-side.
	return !looksLikeProse(q)
}

func looksLikeProse(q string) bool {
	fields := strings.Fields(q)
	if len(fields) < 2 {
		return false
	}
	avg := 0
	for _, f := range fields {
		avg += len(f)
	}
	// Multi-word queries of ordinary word length are almost always prose rather
	// than a source name, a category or a guid.
	return avg/len(fields) >= 4
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
