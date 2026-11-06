// server.go — stenella's own HTTP server.
//
// Stenella used to be an atp application: atp owned the transport, the router,
// the head manager, the header policy and the static hosting. Under full
// decoupling stenella is a service of its own (default :8085) and owns those
// local concerns, while pod, song, shepherd and atp are reached over HTTP.
//
// What stays local is deliberately small and honest:
//
//   - the function registry (method + pattern + handler),
//   - the document head and header policy stenella itself applies,
//   - the hosted app shell and the generated PWA plumbing,
//   - two read-through proxies for browser assets owned elsewhere (song's
//     viewkit runtime).
//
// Nothing here pretends to be atp: the atp console panel asks the atp service.
package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Version is stenella's own version.
const Version = "2.0.0"

// ---------------------------------------------------------------------------
// options and server
// ---------------------------------------------------------------------------

// Options configures a standalone stenella server.
type Options struct {
	Addr          string
	Root          string
	Secret        string // reserved: stenella keeps no vault of its own
	Title         string
	PodURL        string
	SongURL       string
	ShepherdURL   string
	ShepherdAdmin string
	ATPURL        string
	WebFS         fs.FS
}

// Server is the stenella HTTP surface.
type Server struct {
	addr     string
	root     string
	title    string
	appsDir  string
	encrypted bool

	mux    *http.ServeMux
	mu     sync.Mutex
	routes []Route

	pod      *PodClient
	song     *SongClient
	shepherd *ShepherdClient
	atp      *ATPClient

	apps    *AppRegistry
	head    *Head
	headers *Headers
}

// New builds a standalone stenella server. It does not require the other
// services to be running: the clients are lazy, and the console reports an
// unreachable service rather than refusing to start.
func New(o Options) (*Server, error) {
	if o.Root == "" {
		o.Root = "./data"
	}
	if o.Addr == "" {
		o.Addr = ":8085"
	}
	if o.Title == "" {
		o.Title = "stenella — OSINT platform"
	}
	if o.PodURL == "" {
		o.PodURL = "http://127.0.0.1:8081"
	}
	if o.SongURL == "" {
		o.SongURL = "http://127.0.0.1:8082"
	}
	if o.ShepherdURL == "" {
		o.ShepherdURL = "http://127.0.0.1:8083"
	}
	if o.ATPURL == "" {
		o.ATPURL = "http://127.0.0.1:8080"
	}
	if err := os.MkdirAll(o.Root, 0o755); err != nil {
		return nil, fmt.Errorf("stenella: root: %w", err)
	}
	appsDir := filepath.Join(o.Root, "apps")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return nil, fmt.Errorf("stenella: apps: %w", err)
	}
	s := &Server{
		addr:      o.Addr,
		root:      o.Root,
		title:     o.Title,
		appsDir:   appsDir,
		mux:       http.NewServeMux(),
		pod:       NewPodClient(o.PodURL),
		song:      NewSongClient(o.SongURL),
		shepherd:  NewShepherdClient(o.ShepherdURL, o.ShepherdAdmin),
		atp:       NewATPClient(o.ATPURL),
		apps:      newAppRegistry(),
		head:      newHead(o.Title),
		headers:   newHeaders(),
	}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/stenella/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	s.mux.Handle("/stenella/", http.HandlerFunc(s.serveApp))
	s.mux.Handle("/_song/", s.songProxy())
	return s, nil
}

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.addr }

// Root returns the data root.
func (s *Server) Root() string { return s.root }

// Encrypted reports whether stenella holds an encrypted vault. It does not:
// the vault belongs to atp, and stenella never holds the master secret.
func (s *Server) Encrypted() bool { return s.encrypted }

// Pod exposes the pod client.
func (s *Server) Pod() *PodClient { return s.pod }

// Song exposes the song client.
func (s *Server) Song() *SongClient { return s.song }

// Shepherd exposes the shepherd client.
func (s *Server) Shepherd() *ShepherdClient { return s.shepherd }

// ATP exposes the atp client.
func (s *Server) ATP() *ATPClient { return s.atp }

// Apps exposes the local hosted-application registry.
func (s *Server) Apps() *AppRegistry { return s.apps }

// Head exposes the local head manager.
func (s *Server) Head() *Head { return s.head }

// Headers exposes the local header policy.
func (s *Server) Headers() *Headers { return s.headers }

// Close releases nothing today; it exists so callers can defer it uniformly.
func (s *Server) Close() error { return nil }

// ListenAndServe starts the HTTP listener.
func (s *Server) ListenAndServe() error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}

// ServeHTTP applies the local header policy and dispatches.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.headers.applyRequest(r)
	rec := &headerRecorder{ResponseWriter: w, rules: s.headers, path: r.URL.Path}
	s.mux.ServeHTTP(rec, r)
}

// ---------------------------------------------------------------------------
// routed functions
// ---------------------------------------------------------------------------

// Route is one registered function, as the console and /api/functions report it.
type Route struct {
	Method  string `json:"method"`
	Pattern string `json:"pattern"`
	Group   string `json:"group,omitempty"`
	Enabled bool   `json:"enabled"`
}

// Handle registers an http.HandlerFunc for a method and pattern. Patterns use
// Go's ServeMux syntax ({name} wildcards), which is also what atp used.
func (s *Server) Handle(method, pattern string, fn http.HandlerFunc) error {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return fmt.Errorf("stenella: route %s: empty method", pattern)
	}
	if pattern == "" || !strings.HasPrefix(pattern, "/") {
		return fmt.Errorf("stenella: route %q: pattern must start with /", pattern)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rt := range s.routes {
		if rt.Method == method && rt.Pattern == pattern {
			return fmt.Errorf("stenella: duplicate route %s %s", method, pattern)
		}
	}
	s.mux.Handle(method+" "+pattern, fn)
	s.routes = append(s.routes, Route{Method: method, Pattern: pattern, Enabled: true})
	return nil
}

// Routes lists the registered functions in registration order.
func (s *Server) Routes() []Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Route, len(s.routes))
	copy(out, s.routes)
	return out
}

// PathParam reads a {name} wildcard from the request.
func PathParam(r *http.Request, name string) string { return r.PathValue(name) }

// ---------------------------------------------------------------------------
// hosted application registry
// ---------------------------------------------------------------------------

// Icon is one manifest icon.
type Icon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes,omitempty"`
	Type    string `json:"type,omitempty"`
	Purpose string `json:"purpose,omitempty"`
}

// AppMeta is the metadata stenella uses for its own hosted shell.
type AppMeta struct {
	Name            string   `json:"name"`
	Title           string   `json:"title,omitempty"`
	ShortName       string   `json:"short_name,omitempty"`
	Description     string   `json:"description,omitempty"`
	ThemeColor      string   `json:"theme_color,omitempty"`
	BackgroundColor string   `json:"background_color,omitempty"`
	Display         string   `json:"display,omitempty"`
	StartURL        string   `json:"start_url,omitempty"`
	Scope           string   `json:"scope,omitempty"`
	Icons           []Icon   `json:"icons,omitempty"`
	Search          string   `json:"search,omitempty"`
	Suggestions     string   `json:"suggestions,omitempty"`
	SearchTitle     string   `json:"search_title,omitempty"`
}

// AppRegistry holds the applications this server hosts.
type AppRegistry struct {
	mu     sync.RWMutex
	byName map[string]AppMeta
	order  []string
}

func newAppRegistry() *AppRegistry {
	return &AppRegistry{byName: map[string]AppMeta{}}
}

// Register adds or replaces an application's metadata.
func (a *AppRegistry) Register(app AppMeta) error {
	if !isToken(app.Name) {
		return fmt.Errorf("stenella: invalid application name %q", app.Name)
	}
	app = withDefaults(app)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.byName[app.Name]; !ok {
		a.order = append(a.order, app.Name)
	}
	a.byName[app.Name] = app
	return nil
}

// Get returns an application's metadata.
func (a *AppRegistry) Get(name string) (AppMeta, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	app, ok := a.byName[name]
	return app, ok
}

// List returns applications in registration order.
func (a *AppRegistry) List() []AppMeta {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]AppMeta, 0, len(a.order))
	for _, n := range a.order {
		out = append(out, a.byName[n])
	}
	return out
}

// Names returns the registered application names.
func (a *AppRegistry) Names() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.order...)
}

func withDefaults(app AppMeta) AppMeta {
	if app.Title == "" {
		app.Title = app.Name
	}
	if app.ShortName == "" {
		app.ShortName = app.Title
	}
	if app.Display == "" {
		app.Display = "standalone"
	}
	if app.ThemeColor == "" {
		app.ThemeColor = "#0a84ff"
	}
	if app.BackgroundColor == "" {
		app.BackgroundColor = "#ffffff"
	}
	if app.StartURL == "" {
		app.StartURL = "/" + app.Name + "/"
	}
	if app.Scope == "" {
		app.Scope = "/" + app.Name + "/"
	}
	if app.Search == "" {
		app.Search = "/" + app.Name + "/?q={searchTerms}"
	}
	if app.SearchTitle == "" {
		app.SearchTitle = shortName(app.Title)
	}
	if len(app.Icons) == 0 {
		app.Icons = []Icon{{Src: "/" + app.Name + "/icon.svg", Sizes: "any", Type: "image/svg+xml", Purpose: "any"}}
	}
	return app
}

func shortName(s string) string {
	if len(s) <= 16 {
		return s
	}
	return strings.TrimSpace(s[:16])
}

func isToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// head management
// ---------------------------------------------------------------------------

// Head collects the fragments injected before </head>.
type Head struct {
	mu       sync.RWMutex
	global   []string
	perApp   map[string][]string
	title    string
	titleFor map[string]string
}

func newHead(title string) *Head {
	return &Head{title: title, perApp: map[string][]string{}, titleFor: map[string]string{}}
}

// Add registers a global head fragment.
func (h *Head) Add(fragment string) { h.AddFor("", fragment) }

// AddFor registers a head fragment for one application.
func (h *Head) AddFor(app, fragment string) {
	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if app == "" {
		h.global = appendUnique(h.global, fragment)
		return
	}
	h.perApp[app] = appendUnique(h.perApp[app], fragment)
}

// Title sets the default document title.
func (h *Head) Title(t string) { h.TitleFor("", t) }

// TitleFor sets the title for one application.
func (h *Head) TitleFor(app, t string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if app == "" {
		h.title = t
		return
	}
	h.titleFor[app] = t
}

// TitleValue returns the effective title for an app.
func (h *Head) TitleValue(app string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if app != "" {
		if t, ok := h.titleFor[app]; ok {
			return t
		}
	}
	return h.title
}

// Fragments returns the ordered fragments for an app.
func (h *Head) Fragments(app string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.global)+len(h.perApp[app]))
	out = appendUnique(out, h.global...)
	if app != "" {
		out = appendUnique(out, h.perApp[app]...)
	}
	return out
}

// GlobalFragments returns just the global fragments.
func (h *Head) GlobalFragments() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.global...)
}

// FragmentsFor returns just the per-application fragments.
func (h *Head) FragmentsFor(app string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.perApp[app]...)
}

// Inject inserts the managed fragments before </head>.
func (h *Head) Inject(doc []byte, app string, extra ...string) []byte {
	frags := h.Fragments(app)
	frags = appendUnique(frags, extra...)
	if len(frags) == 0 {
		return doc
	}
	marker := []byte("</head>")
	i := indexFold(doc, marker)
	if i < 0 {
		return doc
	}
	var b strings.Builder
	b.Write(doc[:i])
	b.WriteString(strings.Join(frags, "\n"))
	b.WriteString("\n")
	b.Write(doc[i:])
	return []byte(b.String())
}

func appendUnique(dst []string, items ...string) []string {
	for _, s := range items {
		found := false
		for _, d := range dst {
			if d == s {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, s)
		}
	}
	return dst
}

func indexFold(haystack []byte, needle []byte) int {
	return strings.Index(strings.ToLower(string(haystack)), string(needle))
}

// ---------------------------------------------------------------------------
// header policy
// ---------------------------------------------------------------------------

// HeaderRule is one configured rule, as the console reports it.
type HeaderRule struct {
	Direction string `json:"direction"`
	Pattern   string `json:"pattern,omitempty"`
	Name      string `json:"name,omitempty"`
	Value     string `json:"value,omitempty"`
	Action    string `json:"action,omitempty"`
}

// Headers is a small request/response header policy. It mirrors the part of
// atp's API stenella used: Request(...).Strip(), Response(...).Set(...) and
// Internal(...).FromPath(...).
type Headers struct {
	mu    sync.RWMutex
	rules []HeaderRule
}

func newHeaders() *Headers {
	h := &Headers{}
	h.Request("X-Stenella-*").Strip()
	return h
}

// Request starts a request-header rule for a header-name glob.
func (h *Headers) Request(pattern string) *requestRule {
	return &requestRule{h: h, pattern: pattern}
}

// Response starts a response-header rule for a path glob.
func (h *Headers) Response(pattern string) *responseRule {
	return &responseRule{h: h, pattern: pattern}
}

// Internal starts an internal-header rule.
func (h *Headers) Internal(name string) *internalRule {
	return &internalRule{h: h, name: name}
}

type requestRule struct {
	h       *Headers
	pattern string
	strip   bool
	set     map[string]string
}

// Strip removes matching inbound headers.
func (r *requestRule) Strip() *Headers {
	r.strip = true
	r.h.add(HeaderRule{Direction: "request", Pattern: r.pattern, Action: "strip"})
	return r.h
}

// Set sets a header before handlers see it.
func (r *requestRule) Set(name, value string) *Headers {
	if r.set == nil {
		r.set = map[string]string{}
	}
	r.set[name] = value
	r.h.add(HeaderRule{Direction: "request", Pattern: r.pattern, Name: name, Value: value, Action: "set"})
	return r.h
}

type responseRule struct {
	h       *Headers
	pattern string
}

// Set sets a response header.
func (r *responseRule) Set(name, value string) *Headers {
	r.h.add(HeaderRule{Direction: "response", Pattern: r.pattern, Name: name, Value: value, Action: "set"})
	return r.h
}

// Unset removes a response header.
func (r *responseRule) Unset(name string) *Headers {
	r.h.add(HeaderRule{Direction: "response", Pattern: r.pattern, Name: name, Action: "unset"})
	return r.h
}

type internalRule struct {
	h    *Headers
	name string
}

// FromPath records that the header is internal and derived from a path segment.
func (r *internalRule) FromPath(n int) *Headers {
	r.h.add(HeaderRule{Direction: "internal", Name: r.name, Action: fmt.Sprintf("from-path:%d", n)})
	return r.h
}

// Value records a static internal header.
func (r *internalRule) Value(v string) *Headers {
	r.h.add(HeaderRule{Direction: "internal", Name: r.name, Value: v, Action: "set"})
	return r.h
}

func (h *Headers) add(rule HeaderRule) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rules = append(h.rules, rule)
}

// Rules lists every configured rule.
func (h *Headers) Rules() []HeaderRule {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]HeaderRule, len(h.rules))
	copy(out, h.rules)
	return out
}

func (h *Headers) applyRequest(r *http.Request) {
	h.mu.RLock()
	rules := append([]HeaderRule(nil), h.rules...)
	h.mu.RUnlock()
	for _, rule := range rules {
		switch {
		case rule.Direction == "request" && rule.Action == "strip":
			for name := range r.Header {
				if matchHeaderGlob(rule.Pattern, name) {
					r.Header.Del(name)
				}
			}
		case rule.Direction == "request" && rule.Action == "set" && rule.Name != "":
			r.Header.Set(rule.Name, rule.Value)
		}
	}
}

func (h *Headers) applyResponse(hdr http.Header, p string) {
	h.mu.RLock()
	rules := append([]HeaderRule(nil), h.rules...)
	h.mu.RUnlock()
	for _, rule := range rules {
		if rule.Direction != "response" || !globMatch(rule.Pattern, p) {
			continue
		}
		switch rule.Action {
		case "set":
			hdr.Set(rule.Name, rule.Value)
		case "unset":
			hdr.Del(rule.Name)
		}
	}
}

// headerRecorder applies response rules before the headers are sent.
type headerRecorder struct {
	http.ResponseWriter
	rules    *Headers
	path     string
	applied  bool
	status   int
}

func (w *headerRecorder) WriteHeader(status int) {
	if !w.applied {
		w.rules.applyResponse(w.Header(), w.path)
		w.applied = true
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *headerRecorder) Write(p []byte) (int, error) {
	if !w.applied {
		w.rules.applyResponse(w.Header(), w.path)
		w.applied = true
	}
	return w.ResponseWriter.Write(p)
}

func matchHeaderGlob(pattern, name string) bool {
	return globMatch(strings.ToLower(pattern), strings.ToLower(name))
}

// globMatch matches a path/header glob with * (one segment) and ** (any).
func globMatch(pattern, s string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

// ---------------------------------------------------------------------------
// hosted app shell
// ---------------------------------------------------------------------------

func (s *Server) serveApp(w http.ResponseWriter, r *http.Request) {
	app := "stenella"
	tail := strings.TrimPrefix(r.URL.Path, "/stenella/")
	switch tail {
	case "manifest.webmanifest":
		s.serveManifest(w, app)
		return
	case "sw.js":
		s.serveServiceWorker(w, app)
		return
	case "opensearch.xml":
		s.serveOpenSearch(w, app)
		return
	}
	if tail == "" || tail == "index.html" {
		index := filepath.Join(s.appsDir, app, "index.html")
		doc, err := os.ReadFile(index)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		meta, _ := s.apps.Get(app)
		doc = s.head.Inject(doc, app, s.capabilityFragments(app, meta)...)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(doc)
		return
	}
	http.StripPrefix("/stenella/", http.FileServer(http.Dir(filepath.Join(s.appsDir, app)))).ServeHTTP(w, r)
}

func (s *Server) serveManifest(w http.ResponseWriter, app string) {
	meta, _ := s.apps.Get(app)
	w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(manifestOf(meta))
}

func (s *Server) serveServiceWorker(w http.ResponseWriter, app string) {
	meta, _ := s.apps.Get(app)
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, serviceWorker(meta, s.appShellFiles(app)))
}

func (s *Server) serveOpenSearch(w http.ResponseWriter, app string) {
	meta, _ := s.apps.Get(app)
	w.Header().Set("Content-Type", "application/opensearchdescription+xml; charset=utf-8")
	_, _ = io.WriteString(w, openSearch(meta))
}

// capabilityFragments returns the head fragments generated for an app.
func (s *Server) capabilityFragments(app string, meta AppMeta) []string {
	if app == "" {
		return nil
	}
	base := "/" + app
	frags := []string{
		`<link rel="manifest" href="` + base + `/manifest.webmanifest">`,
		`<link rel="search" type="application/opensearchdescription+xml" title="` +
			xmlEscape(meta.SearchTitle) + `" href="` + base + `/opensearch.xml">`,
		`<meta name="theme-color" content="` + meta.ThemeColor + `">`,
	}
	return frags
}

func (s *Server) appShellFiles(app string) []string {
	var out []string
	root := filepath.Join(s.appsDir, app)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		switch rel {
		case "sw.js", "manifest.webmanifest", "opensearch.xml":
			return nil
		}
		out = append(out, "/"+app+"/"+rel)
		return nil
	})
	sort.Strings(out)
	return out
}

func manifestOf(a AppMeta) map[string]any {
	m := map[string]any{
		"name":             a.Title,
		"short_name":       a.ShortName,
		"start_url":        a.StartURL,
		"scope":            a.Scope,
		"display":          a.Display,
		"theme_color":      a.ThemeColor,
		"background_color": a.BackgroundColor,
		"icons":            a.Icons,
	}
	if a.Description != "" {
		m["description"] = a.Description
	}
	return m
}

type openSearchDescription struct {
	XMLName     xml.Name        `xml:"OpenSearchDescription"`
	XMLNS       string          `xml:"xmlns,attr"`
	ShortName   string          `xml:"ShortName"`
	Description string          `xml:"Description"`
	InputEnc    string          `xml:"InputEncoding"`
	Image       struct {
		Width  int    `xml:"width,attr"`
		Height int    `xml:"height,attr"`
		Type   string `xml:"type,attr"`
		URL    string `xml:",chardata"`
	} `xml:"Image"`
	Urls []openSearchURL `xml:"Url"`
}

type openSearchURL struct {
	Type     string `xml:"type,attr"`
	Template string `xml:"template,attr"`
	Rel      string `xml:"rel,attr,omitempty"`
}

func openSearch(a AppMeta) string {
	doc := openSearchDescription{
		XMLNS:       "http://a9.com/-/spec/opensearch/1.1/",
		ShortName:   a.SearchTitle,
		Description: a.Title + " search",
		InputEnc:    "UTF-8",
	}
	doc.Image.Width = 16
	doc.Image.Height = 16
	doc.Image.Type = "image/svg+xml"
	doc.Image.URL = "/" + a.Name + "/icon.svg"
	doc.Urls = append(doc.Urls, openSearchURL{Type: "text/html", Template: a.Search})
	doc.Urls = append(doc.Urls, openSearchURL{
		Type:     "application/opensearchdescription+xml",
		Template: "/" + a.Name + "/opensearch.xml",
		Rel:      "self",
	})
	out, _ := xml.MarshalIndent(doc, "", "  ")
	return xml.Header + string(out) + "\n"
}

func serviceWorker(a AppMeta, shell []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// generated by stenella for application %q\n", a.Name)
	fmt.Fprintf(&b, "const CACHE = 'stenella-%s-v1';\n", a.Name)
	b.WriteString("const SHELL = [\n")
	for _, f := range shell {
		fmt.Fprintf(&b, "  %q,\n", f)
	}
	b.WriteString("];\n")
	b.WriteString(`self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});
self.addEventListener('activate', (e) => {
  e.waitUntil(self.clients.claim());
});
self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname.includes('/api/') || url.pathname.startsWith('/pod/')) {
    e.respondWith(fetch(req).catch(() => caches.match(req)));
    return;
  }
  e.respondWith(caches.match(req).then((hit) => hit || fetch(req).then((res) => {
    const copy = res.clone();
    caches.open(CACHE).then((c) => c.put(req, copy));
    return res;
  }).catch(() => caches.match('/` + a.Name + `/'))));
});
`)
	return b.String()
}

func xmlEscape(s string) string { return html.EscapeString(s) }

// songProxy forwards browser asset requests (the shared viewkit runtime) to the
// song service. It is a read-through proxy, not an embed.
func (s *Server) songProxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := s.song.Base() + r.URL.Path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "song unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}
