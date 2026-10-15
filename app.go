// app.go — stenella as an atp application.
//
// Stenella owns no transport, no router and no security model: atp is the
// server. This file constructs the platform on top of an *atp.Server — the pod
// records, the collection engines, the routes — and is the only place that
// knows about atp. Everything else in the package is the OSINT platform itself.
package main

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"azzurrotech/atp"
)

// App is stenella: atp plus the OSINT platform.
type App struct {
	srv    *atp.Server
	store  *Store
	client *http.Client

	// fetcher is used by the collection pipeline; it shares the HTTP/1.1
	// transport that the source list needs.
	fetcher *aggregator

	aggMu sync.Mutex
	aggs  map[string]*aggregator

	// runMu serialises collection runs so two refreshes cannot interleave
	// their read-modify-write passes over the store.
	runMu sync.Mutex
}

// NewApp builds the platform on an atp server.
func NewApp(srv *atp.Server) (*App, error) {
	if srv == nil {
		return nil, fmt.Errorf("stenella: nil atp server")
	}
	store, err := NewStore(srv.Pod())
	if err != nil {
		return nil, err
	}
	return &App{
		srv:     srv,
		store:   store,
		client:  defaultFeedClient(),
		fetcher: newAggregator(nil, nil, time.Minute),
		aggs:    map[string]*aggregator{},
	}, nil
}

// Server exposes the atp server the platform is attached to.
func (a *App) Server() *atp.Server { return a.srv }

// Store exposes the record layer.
func (a *App) Store() *Store { return a.store }

// Register seeds the default configuration and mounts every stenella function
// on atp's registry.
func (a *App) Register() error {
	if err := a.seed(); err != nil {
		return err
	}
	for _, rt := range a.routes() {
		if err := a.srv.Handle(rt.method, rt.pattern, rt.fn); err != nil {
			return fmt.Errorf("stenella: route %s %s: %w", rt.method, rt.pattern, err)
		}
	}
	return nil
}

type route struct {
	method  string
	pattern string
	fn      http.HandlerFunc
}

// routes is the complete function surface stenella registers on atp. Every one
// is a serverless function in atp's sense: bound to a verb and an endpoint,
// listed at /api/functions, and replaceable without touching the server.
//
// atp's registry allows exactly one trailing {name} segment, so sub-actions on
// a resource are expressed as a POST to the resource carrying an "action"
// field rather than as deeper paths.
func (a *App) routes() []route {
	return []route{
		// --- collection: the shareable combined feed ----------------------
		{"GET", "/stenella/feeds/{name}", a.serveFeed},
		{"GET", "/stenella/api/aggregate", a.serveAggregate},

		// --- sources --------------------------------------------------------
		{"GET", "/stenella/api/sources", a.listSources},
		{"POST", "/stenella/api/sources", a.createSource},
		{"GET", "/stenella/api/sources/{id}", a.getSource},
		{"POST", "/stenella/api/sources/{id}", a.sourceAction},
		{"DELETE", "/stenella/api/sources/{id}", a.deleteSource},

		// --- collections ----------------------------------------------------
		{"GET", "/stenella/api/collections", a.listCollections},
		{"POST", "/stenella/api/collections/{id}", a.collectionAction},

		// --- items ----------------------------------------------------------
		{"GET", "/stenella/api/items", a.listItems},
		{"POST", "/stenella/api/items", a.createItem},
		{"GET", "/stenella/api/items/{id}", a.getItem},
		{"DELETE", "/stenella/api/items/{id}", a.deleteItem},

		// --- entities -------------------------------------------------------
		{"GET", "/stenella/api/entities", a.listEntities},
		{"GET", "/stenella/api/entities/{id}", a.getEntity},

		// --- links ----------------------------------------------------------
		{"GET", "/stenella/api/links", a.listLinks},
		{"POST", "/stenella/api/links", a.createLink},
		{"POST", "/stenella/api/links/{id}", a.linkAction},
		{"DELETE", "/stenella/api/links/{id}", a.deleteLink},

		// --- comments -------------------------------------------------------
		{"GET", "/stenella/api/comments", a.listComments},
		{"POST", "/stenella/api/comments", a.createComment},
		{"DELETE", "/stenella/api/comments/{id}", a.deleteComment},

		// --- notes ----------------------------------------------------------
		{"GET", "/stenella/api/notes", a.listNotes},
		{"POST", "/stenella/api/notes", a.createNote},
		{"GET", "/stenella/api/notes/{id}", a.getNote},
		{"DELETE", "/stenella/api/notes/{id}", a.deleteNote},

		// --- groups ---------------------------------------------------------
		{"GET", "/stenella/api/groups", a.listGroups},
		{"POST", "/stenella/api/groups", a.createGroup},
		{"DELETE", "/stenella/api/groups/{id}", a.deleteGroup},

		// --- rules ----------------------------------------------------------
		{"GET", "/stenella/api/rules", a.listRules},
		{"POST", "/stenella/api/rules", a.createRule},
		{"GET", "/stenella/api/rules/{id}", a.getRule},
		{"POST", "/stenella/api/rules/{id}", a.ruleAction},
		{"DELETE", "/stenella/api/rules/{id}", a.deleteRule},

		// --- insights -------------------------------------------------------
		{"GET", "/stenella/api/insights", a.listInsights},
		{"POST", "/stenella/api/insights/{id}", a.insightAction},
		{"DELETE", "/stenella/api/insights/{id}", a.deleteInsight},

		// --- pipeline -------------------------------------------------------
		{"POST", "/stenella/api/run", a.runAll},

		// --- search, statistics, dissemination ------------------------------
		{"GET", "/stenella/api/search", a.search},
		{"GET", "/stenella/api/stats", a.stats},
		{"GET", "/stenella/api/export.json", a.exportJSON},
		{"GET", "/stenella/api/export.csv", a.exportCSV},
		{"GET", "/stenella/api/opml", a.opmlExport},
		{"POST", "/stenella/api/opml", a.opmlImport},
		{"POST", "/stenella/api/share", a.createShare},

		// --- the atp console ------------------------------------------------
		{"GET", "/stenella/api/console", a.consoleAll},
		{"GET", "/stenella/api/console/{surface}", a.consoleSurface},

		// --- health ---------------------------------------------------------
		{"GET", "/stenella/api/health", a.health},
	}
}

// ---------------------------------------------------------------------------
// seeding
// ---------------------------------------------------------------------------

// seed installs the configured sources and the starter rule set the first time
// the platform starts, so a fresh server has something to collect and something
// to reason with. It never overwrites operator changes.
func (a *App) seed() error {
	existing, err := a.store.Sources()
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		for _, def := range defaultSourceConfigs() {
			src := def
			if err := a.store.PutSource(&src); err != nil {
				return err
			}
		}
	}
	rules, err := a.store.Rules()
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		for _, r := range defaultRules() {
			rule := r
			if err := a.store.PutRule(&rule); err != nil {
				return err
			}
		}
	}
	return nil
}

// defaultSourceConfigs turns the README's five origin feeds into configured
// sources with a collection, a kind and a reliability grade. The URLs are the
// verified ones from the collection layer; the grades are the platform's own
// assessment of the publisher, which the operator can change.
func defaultSourceConfigs() []Source {
	grades := map[string]string{
		"RadioItalia (RAI)":                     "C",
		"ANSA":                                  "B",
		"CBC":                                   "B",
		"Communications Security Establishment": "A",
		"Government of Canada":                  "A",
	}
	collections := map[string]string{
		"RadioItalia (RAI)":                     "news",
		"ANSA":                                  "news",
		"CBC":                                   "news",
		"Communications Security Establishment": "government",
		"Government of Canada":                  "government",
	}
	var out []Source
	for _, s := range rssSources() {
		collection := collections[s.Name]
		if collection == "" {
			collection = "all"
		}
		grade := grades[s.Name]
		if grade == "" {
			grade = "C"
		}
		out = append(out, Source{
			Name:        s.Name,
			URL:         s.URL,
			Kind:        "feed",
			Collection:  collection,
			Reliability: grade,
			ACL:         "public",
			Enabled:     true,
		})
	}
	return out
}

// defaultRules is the starter rule set: rules that are true of almost any
// collection, so a fresh install produces insights from its first run.
func defaultRules() []Rule {
	return []Rule{
		{
			Name:       "multi-source corroboration",
			Text:       "The same or closely related reporting appears in two or more independent sources.",
			Severity:   "medium",
			Enabled:    true,
			MinItems:   2,
			MinSources: 2,
		},
		{
			Name:     "supply-chain activity",
			Text:     "Reporting on supply chains, vendors or dependencies: watch for a compromise narrative.",
			Severity: "high",
			Enabled:  true,
			Tags:     []string{"supply-chain", "supply chain", "vendor"},
		},
		{
			Name:     "cyber and vulnerability reporting",
			Text:     "Reporting on vulnerabilities, exploits or cyber incidents deserves analyst triage.",
			Severity: "high",
			Enabled:  true,
			Tags:     []string{"cyber", "vulnerability", "security", "malware", "ransomware"},
		},
	}
}

// ---------------------------------------------------------------------------
// collection engines, one per collection
// ---------------------------------------------------------------------------

// aggFor returns the cached collection engine for a collection, rebuilding it
// when the configured source set has changed. The engine is the same tested
// aggregator the shareable feed has always used.
func (a *App) aggFor(collection string) (*aggregator, error) {
	sources, err := a.store.EnabledSources()
	if err != nil {
		return nil, err
	}
	var (
		list []rssSource
		sig  []string
	)
	for _, s := range sources {
		if !collectionMatches(collection, s.Collection) {
			continue
		}
		list = append(list, rssSource{Name: s.Name, URL: s.URL})
		sig = append(sig, s.ID+"="+s.URL)
	}
	sort.Strings(sig)
	signature := strings.Join(sig, "\n")

	a.aggMu.Lock()
	defer a.aggMu.Unlock()
	if ag, ok := a.aggs[collection]; ok && ag.sig == signature {
		return ag, nil
	}
	ag := newAggregator(list, a.client, 5*time.Minute)
	ag.sig = signature
	a.aggs[collection] = ag
	return ag, nil
}

// collectionMatches reports whether a source in sourceCollection belongs to the
// requested collection. "all" is the union of every configured source, which is
// what the README's example URL uses it for.
func collectionMatches(want, sourceCollection string) bool {
	if want == "" || want == "all" {
		return true
	}
	if sourceCollection == "" {
		return false
	}
	return strings.EqualFold(want, sourceCollection)
}

// serveFeed serves the per-collection combined feed:
//
//	GET /stenella/feeds/{collection}.xml
//	GET /stenella/feeds/{collection}.json
//
// "all" is the whole configured source set. The document is the same sanitised,
// link-free, private-free feed the README describes: the format is the only
// difference between the two.
func (a *App) serveFeed(w http.ResponseWriter, r *http.Request) {
	name := atp.PathParam(r, "name")
	collection, format := splitFeedName(name)
	if collection == "" || format == "" {
		writeErr(w, http.StatusNotFound, "expected /stenella/feeds/{collection}.xml or .json")
		return
	}
	if !a.knownCollection(collection) {
		writeErr(w, http.StatusNotFound, "unknown collection "+collection)
		return
	}
	ag, err := a.aggFor(collection)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch format {
	case "xml":
		ag.serveRSS(w, r)
	case "json":
		items := ag.Articles(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{
			"feed":       "stenella",
			"collection": collection,
			"sources":    ag.Sources(),
			"count":      len(items),
			"errors":     ag.Errors(),
			"items":      items,
			"updated":    nowUTC(),
		})
	}
}

// splitFeedName turns "news.xml" into ("news", "xml").
func splitFeedName(name string) (collection, format string) {
	switch {
	case strings.HasSuffix(name, ".xml"):
		collection, format = strings.TrimSuffix(name, ".xml"), "xml"
	case strings.HasSuffix(name, ".json"):
		collection, format = strings.TrimSuffix(name, ".json"), "json"
	}
	if collection == "" {
		return "", ""
	}
	return collection, format
}

func (a *App) knownCollection(name string) bool {
	if name == "all" {
		return true
	}
	collections, err := a.store.Collections()
	if err != nil {
		return false
	}
	for _, c := range collections {
		if c == name {
			return true
		}
	}
	return false
}

// serveAggregate keeps the original JSON twin of the shareable feed. It always
// describes the whole configured source set ("all").
func (a *App) serveAggregate(w http.ResponseWriter, r *http.Request) {
	ag, err := a.aggFor("all")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ag.serveJSON(w, r)
}

// ---------------------------------------------------------------------------
// shipping the front end
// ---------------------------------------------------------------------------

// materialiseWeb writes the embedded front end into the hosted application's
// directory, so song can serve it and atp can generate the client-app plumbing
// (manifest, service worker, OpenSearch document) around it. The binary owns
// these files: an upgrade always replaces them.
// materialiseWeb copies the embedded web assets into the application directory,
// which is where song serves the app from. The assets are embedded under the
// "web/" directory, so that subtree is selected rather than assumed: a
// single-file embed carries the same prefix.
func materialiseWeb(fsys fs.FS, dst string) error {
	root := fsys
	if sub, err := fs.Sub(fsys, "web"); err == nil {
		root = sub
	}
	return fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := fs.ReadFile(root, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
}
