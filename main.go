// Command stenella runs the stenella OSINT platform **on top of atp**: atp is
// the server, stenella contributes the hosted dashboard, its Go functions and
// its secrets. This is the integration the atp README describes in
// "Using atp from another Go project".
package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"azzurrotech/atp"
)

//go:embed web/index.html
var indexHTML []byte

func main() {
	var (
		addr   = flag.String("addr", env("ATP_ADDR", ":8080"), "listen address (host:port)")
		root   = flag.String("root", env("ATP_ROOT", "./data"), "root for the hosted app, the pod store and the vault")
		secret = flag.String("secret", env("ATP_SECRET", ""), "master secret, at least 32 bytes")
	)
	flag.Parse()

	// --- the platform is atp ----------------------------------------------
	srv, err := atp.New(atp.Options{
		Root:   *root,
		Addr:   *addr,
		Secret: *secret,
		Title:  "stenella — OSINT platform",
	})
	if err != nil {
		log.Fatalf("stenella: %v", err)
	}
	defer srv.Close()

	// --- the hosted application -------------------------------------------
	if err := srv.Apps().Register(atp.App{
		Name:        "stenella",
		Title:       "stenella",
		ShortName:   "stenella",
		Description: "Open-source intelligence platform",
		ThemeColor:  "#0a84ff",
		Search:      "/stenella/?q={searchTerms}",
	}); err != nil {
		log.Fatalf("stenella: app: %v", err)
	}
	indexPath := filepath.Join(srv.Root(), "apps", "stenella", "index.html")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
			log.Fatalf("stenella: %v", err)
		}
		if err := os.WriteFile(indexPath, indexHTML, 0o644); err != nil {
			log.Fatalf("stenella: %v", err)
		}
	}

	// --- central head management ------------------------------------------
	srv.Head().Title("stenella — OSINT platform")
	srv.Head().Add(`<meta name="description" content="stenella: aggregated open-source intelligence">`)
	srv.Head().AddFor("stenella", `<meta name="color-scheme" content="light dark">`)

	// --- header policy -----------------------------------------------------
	srv.Headers().
		Request("X-Stenella-*").Strip().
		Response("/stenella/*.css").Set("Cache-Control", "public, max-age=3600").
		Response("/stenella/*.js").Set("Cache-Control", "public, max-age=3600").
		Internal("X-Stenella-View").FromPath(1)

	// --- secrets (API keys for external sources) ---------------------------
	for _, name := range []string{"newsapi", "twitter", "shodan"} {
		if v := os.Getenv("STELLENA_" + strings.ToUpper(name) + "_KEY"); v != "" {
			if err := srv.Secrets().Set("stenella", name, v, "configured via environment"); err != nil {
				log.Printf("stenella: secret %s: %v", name, err)
			}
		}
	}

	// --- stenella's Go functions on top of atp ------------------------------
	state := newState()
	must(srv.Handle("GET", "/stenella/api/feeds", state.listFeeds))
	must(srv.Handle("POST", "/stenella/api/feeds", state.addFeed))
	must(srv.Handle("POST", "/stenella/api/links", state.addLink))
	must(srv.Handle("GET", "/stenella/api/links", state.listLinks))
	must(srv.Handle("GET", "/stenella/api/insights", state.insights))
	must(srv.Handle("GET", "/stenella/api/stats", state.stats))

	// --- collection: the shareable combined feed ---------------------------
	// One URL, many sources, no links and no private elements. Configured from
	// STELLENA_RSS_SOURCES ("Name=URL,Name=URL") or the built-in source list.
	agg := newAggregator(rssSources(), nil, 5*time.Minute)
	must(srv.Handle("GET", "/stenella/feeds/all.xml", agg.serveRSS))
	must(srv.Handle("GET", "/stenella/api/aggregate", agg.serveJSON))
	log.Printf("stenella: combined feed on /stenella/feeds/all.xml from %d sources", len(agg.Sources()))

	must(srv.Handle("GET", "/api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"platform": "atp", "app": "stenella", "ok": true})
	}))

	log.Printf("stenella on atp %s listening on %s (root %s)", atp.Version, srv.Addr(), srv.Root())
	log.Printf("  platform: http://%s/     app: http://%s/stenella/", hostPort(srv.Addr()), hostPort(srv.Addr()))
	if err := http.ListenAndServe(srv.Addr(), srv); err != nil {
		log.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// the OSINT core: aggregated feed items, links between them, rule-based
// insights. This is a self-contained demonstration of what stenella registers
// as serverless functions on top of atp.
// ---------------------------------------------------------------------------

// Item is one aggregated feed entry.
type Item struct {
	ID      string    `json:"id"`
	Feed    string    `json:"feed"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Summary string    `json:"summary,omitempty"`
	Tags    []string  `json:"tags,omitempty"`
	Seen    time.Time `json:"seen"`
}

// Link is a relationship between two items or entities.
type Link struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // reference | duplicate | related | contradiction
	Note string `json:"note,omitempty"`
}

// Rule is one insight rule: it fires when items match a tag.
type Rule struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
	Text string   `json:"text"`
}

type state struct {
	mu    sync.RWMutex
	items []Item
	links []Link
	rules []Rule
}

func newState() *state {
	now := time.Now().UTC()
	return &state{
		items: []Item{
			{ID: "i1", Feed: "cert", Title: "Certificate authority logs a wildcard issuance", URL: "https://example.org/ca", Tags: []string{"critical", "crypto"}, Seen: now},
			{ID: "i2", Feed: "news", Title: "Vendor confirms supply-chain compromise", URL: "https://example.org/vendor", Tags: []string{"critical", "supply-chain"}, Seen: now.Add(-time.Hour)},
			{ID: "i3", Feed: "forum", Title: "Discussion of the same package hash", URL: "https://example.org/forum", Tags: []string{"supply-chain"}, Seen: now.Add(-2 * time.Hour)},
			{ID: "i4", Feed: "news", Title: "New toolkit release notes", URL: "https://example.org/toolkit", Tags: []string{"tooling"}, Seen: now.Add(-6 * time.Hour)},
		},
		links: []Link{
			{From: "i2", To: "i3", Kind: "related", Note: "same package hash"},
		},
		rules: []Rule{
			{ID: "r-critical", Tags: []string{"critical"}, Text: "Two or more items tagged critical within 24h"},
			{ID: "r-supply", Tags: []string{"supply-chain"}, Text: "Supply-chain activity across two sources"},
		},
	}
}

func (s *state) listFeeds(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"items": s.items})
}

func (s *state) addFeed(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Feed    string   `json:"feed"`
		Title   string   `json:"title"`
		URL     string   `json:"url"`
		Summary string   `json:"summary"`
		Tags    []string `json:"tags"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Feed == "" || in.Title == "" {
		writeErr(w, http.StatusBadRequest, "feed and title are required")
		return
	}
	item := Item{
		ID:      "i" + randomID(),
		Feed:    in.Feed,
		Title:   in.Title,
		URL:     in.URL,
		Summary: in.Summary,
		Tags:    in.Tags,
		Seen:    time.Now().UTC(),
	}
	s.mu.Lock()
	s.items = append(s.items, item)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, item)
}

func (s *state) listLinks(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"links": s.links})
}

func (s *state) addLink(w http.ResponseWriter, r *http.Request) {
	var l Link
	if !decode(w, r, &l) {
		return
	}
	if l.From == "" || l.To == "" {
		writeErr(w, http.StatusBadRequest, "from and to are required")
		return
	}
	if l.Kind == "" {
		l.Kind = "related"
	}
	s.mu.Lock()
	s.links = append(s.links, l)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, l)
}

// insights evaluates every rule against the aggregated items.
func (s *state) insights(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []map[string]any
	for _, rule := range s.rules {
		var matched []string
		for _, it := range s.items {
			if hasAny(it.Tags, rule.Tags) {
				matched = append(matched, it.ID)
			}
		}
		if len(matched) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"rule":    rule.ID,
			"text":    rule.Text,
			"items":   matched,
			"count":   len(matched),
			"verdict": verdict(rule, len(matched)),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"insights": out})
}

func verdict(rule Rule, n int) string {
	if n >= 2 {
		return "attention"
	}
	return "monitor"
}

func (s *state) stats(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	feeds := map[string]int{}
	tags := map[string]int{}
	for _, it := range s.items {
		feeds[it.Feed]++
		for _, t := range it.Tags {
			tags[t]++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":   len(s.items),
		"links":   len(s.links),
		"rules":   len(s.rules),
		"feeds":   feeds,
		"tags":    tags,
		"updated": time.Now().UTC(),
	})
}

func hasAny(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func must(err error) {
	if err != nil {
		log.Fatalf("stenella: %v", err)
	}
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// hostPort renders a listen address as a host:port for logs.
func hostPort(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}
