//go:build ignore

// stenella.go
//
// A minimal Go server that:
//   • Aggregates a configurable list of RSS feeds.
//   • Serves a combined view at "/".
//   • Exposes JSON APIs for the feed items and for managing the feed list.
//   • Lets you register extra handlers via the `extraHandlers` slice.
//
// Run with: go run stenella.go
// Then open http://localhost:8080 in a browser.

package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// -------------------- RSS structures --------------------
type RSS struct {
	Channel Channel `xml:"channel"`
}
type Channel struct {
	Title string `xml:"title"`
	Items []Item `xml:"item"`
}
type Item struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

// -------------------- Unified feed item --------------------
type FeedItem struct {
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Description string    `json:"description"`
	Published   time.Time `json:"published"`
	Source      string    `json:"source"` // which feed it came from
}

// -------------------- In‑memory feed source list --------------------
var (
	feedSources = []string{
		"https://news.ycombinator.com/rss",
		"https://www.reddit.com/r/golang/.rss",
	}
	srcMu sync.RWMutex // protects feedSources
)

// -------------------- Helpers: fetch & parse a single feed --------------------
func fetchFeed(feedURL string) ([]FeedItem, error) {
	resp, err := http.Get(feedURL)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", feedURL, err)
	}
	defer resp.Body.Close()

	var rss RSS
	if err := xml.NewDecoder(resp.Body).Decode(&rss); err != nil {
		return nil, fmt.Errorf("decode XML %s: %w", feedURL, err)
	}

	items := make([]FeedItem, 0, len(rss.Channel.Items))
	base, _ := url.Parse(feedURL)

	for _, it := range rss.Channel.Items {
		pub, _ := parsePubDate(it.PubDate)

		// Resolve relative links against the feed URL
		link, err := url.Parse(it.Link)
		if err == nil && !link.IsAbs() {
			it.Link = base.ResolveReference(link).String()
		}

		items = append(items, FeedItem{
			Title:       strings.TrimSpace(it.Title),
			Link:        strings.TrimSpace(it.Link),
			Description: strings.TrimSpace(it.Description),
			Published:   pub,
			Source:      rss.Channel.Title,
		})
	}
	return items, nil
}

// Parse many common date formats used in RSS feeds.
func parsePubDate(v string) (time.Time, error) {
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		time.RFC3339,
	}
	var t time.Time
	var err error
	for _, l := range layouts {
		t, err = time.Parse(l, v)
		if err == nil {
			return t, nil
		}
	}
	// Fallback without timezone
	if t, err = time.Parse("Mon, 02 Jan 2006 15:04:05", v); err == nil {
		return t, nil
	}
	return time.Now(), fmt.Errorf("unparseable date %q", v)
}

// -------------------- Aggregate all feeds --------------------
func aggregateFeeds() ([]FeedItem, error) {
	srcMu.RLock()
	sources := make([]string, len(feedSources))
	copy(sources, feedSources)
	srcMu.RUnlock()

	all := []FeedItem{}
	for _, src := range sources {
		itms, err := fetchFeed(src)
		if err != nil {
			log.Printf("[WARN] could not fetch %s: %v", src, err)
			continue // skip failing feeds
		}
		all = append(all, itms...)
	}
	// Newest first
	sort.Slice(all, func(i, j int) bool {
		return all[i].Published.After(all[j].Published)
	})
	return all, nil
}

// -------------------- HTTP Handlers --------------------
func indexHandler(w http.ResponseWriter, r *http.Request) {
	if err := pageTmpl.Execute(w, nil); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// JSON API returning merged feed items
func apiFeedsHandler(w http.ResponseWriter, r *http.Request) {
	items, err := aggregateFeeds()
	if err != nil {
		http.Error(w, "failed to load feeds", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// Return the current list of RSS URLs
func apiSourcesHandler(w http.ResponseWriter, r *http.Request) {
	srcMu.RLock()
	list := make([]string, len(feedSources))
	copy(list, feedSources)
	srcMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// Add a new RSS URL (expects JSON body: {"url":"https://..."} )
func apiAddSourceHandler(w http.ResponseWriter, r *http.Request) {
	var payload struct{ URL string `json:"url"` }
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.URL) == "" {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}
	payload.URL = strings.TrimSpace(payload.URL)

	// Basic validation – must be a parsable absolute URL
	if u, err := url.Parse(payload.URL); err != nil || !u.IsAbs() {
		http.Error(w, "invalid URL", http.StatusBadRequest)
		return
	}

	srcMu.Lock()
	// Avoid duplicates
	for _, s := range feedSources {
		if s == payload.URL {
			srcMu.Unlock()
			http.Error(w, "source already exists", http.StatusConflict)
			return
		}
	}
	feedSources = append(feedSources, payload.URL)
	srcMu.Unlock()

	w.WriteHeader(http.StatusCreated)
}

// Remove an existing RSS URL (JSON body: {"url":"https://..."} )
func apiRemoveSourceHandler(w http.ResponseWriter, r *http.Request) {
	var payload struct{ URL string `json:"url"` }
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.URL) == "" {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}
	payload.URL = strings.TrimSpace(payload.URL)

	srcMu.Lock()
	newList := make([]string, 0, len(feedSources))
	found := false
	for _, s := range feedSources {
		if s == payload.URL {
			found = true
			continue
		}
		newList = append(newList, s)
	}
	if !found {
		srcMu.Unlock()
		http.Error(w, "source not found", http.StatusNotFound)
		return
	}
	feedSources = newList
	srcMu.Unlock()

	w.WriteHeader(http.StatusOK)
}

// -------------------- Plug‑in style extra handlers --------------------
type extraHandler struct {
	Pattern string
	Handler http.HandlerFunc
}

// Add any custom routes here (example commented out):
var extraHandlers = []extraHandler{
	// {Pattern: "/hello", Handler: helloHandler},
}

// Example extra handler (uncomment in extraHandlers to use)
// func helloHandler(w http.ResponseWriter, r *http.Request) {
// 	fmt.Fprintln(w, "👋 Hello from a custom endpoint!")
// }

// -------------------- HTML Template (single file) --------------------
var pageTmpl = template.Must(template.New("page").Parse(`
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Stenella – Combined RSS Viewer</title>
<style>
body {font-family:Arial,sans-serif;margin:0;padding:0;background:#fafafa;}
header {background:#004466;color:#fff;padding:1rem;text-align:center;}
.container {max-width:900px;margin:auto;padding:1rem;}
#feed {margin-top:1rem;}
.item {border-bottom:1px solid #ddd;padding:.5rem 0;}
.title {font-weight:bold;font-size:1.1rem;}
.meta {color:#666;font-size:.9rem;}
.desc {margin-top:.3rem;}
#sources {margin-top:1rem;}
.source {display:flex;align-items:center;margin-bottom:.3rem;}
.source span {flex:1;word-break:break-all;}
.source button {margin-left:.5rem;}
form {display:flex;margin-top:.5rem;}
input[type=text] {flex:1;padding:.4rem;}
button {padding:.4rem .8rem;margin-left:.3rem;}
</style>
</head>
<body>
<header><h1>Combined RSS Feed</h1></header>
<div class="container">

  <!-- ==== Feed List ==== -->
  <div id="feed"><em>Loading feed items…</em></div>

  <!-- ==== Manage Sources ==== -->
  <section id="sources">
    <h2>Managed RSS Sources</h2>
    <div id="source-list"><em>Loading sources…</em></div>

    <form id="add-form">
      <input type="text" id="new-url" placeholder="https://example.com/feed.rss" required />
      <button type="submit">Add</button>
    </form>
  </section>
</div>

<script>
// ---------- Utility ----------
async function postJSON(url, data) {
	const resp = await fetch(url, {
		method: "POST",
		headers: {"Content-Type":"application/json"},
		body: JSON.stringify(data)
	});
	return resp;
}

// ---------- Load & render feed items ----------
async function loadFeed() {
	const resp = await fetch('/api/feeds');
	if (!resp.ok) {document.getElementById('feed').innerHTML='<em>Error loading feeds</em>';return;}
	const items = await resp.json();
	const container = document.getElementById('feed');
	container.innerHTML = '';

	items.forEach(i => {
		const div = document.createElement('div');
		div.className = 'item';
		div.innerHTML = `
			<div class="title"><a href="${i.link}" target="_blank">${i.title}</a></div>
			<div class="meta">🕒 ${new Date(i.published).toLocaleString()} • ${i.source}</div>
			<div class="desc">${i.description}</div>`;
		container.appendChild(div);
	});
}

// ---------- Load & render source list ----------
async function loadSources() {
	const resp = await fetch('/api/sources');
	if (!resp.ok) {document.getElementById('source-list').innerHTML='<em>Error loading sources</em>';return;}
	const list = await resp.json();
	const container = document.getElementById('source-list');
	container.innerHTML = '';

	list.forEach(url => {
		const div = document.createElement('div');
		div.className = 'source';
		div.innerHTML = `
			<span>${url}</span>
			<button data-url="${url}">✖</button>`;
		container.appendChild(div);
	});

	// Attach removal handlers
	container.querySelectorAll('button').forEach(btn => {
		btn.addEventListener('click', async e => {
			const url = e.target.dataset.url;
			const res = await postJSON('/api/sources/remove', {url});
			if (res.ok) {
				loadSources();   // refresh list
				loadFeed();      // refresh feed view (some items may disappear)
			} else {
				alert('Failed to remove source');
			}
		});
	});
}

// ---------- Add source form ----------
document.getElementById('add-form').addEventListener('submit', async e => {
	e.preventDefault();
	const url = document.getElementById('new-url').value.trim();
	if (!url) return;
	const res = await postJSON('/api/sources/add', {url});
	if (res.status === 201) {
		document.getElementById('new-url').value = '';
		loadSources();
		loadFeed();
	} else if (res.status === 409) {
		alert('Source already exists');
	} else {
		alert('Failed to add source');
	}
});

// Initial load + periodic refresh (2 min)
loadFeed();
loadSources();
setInterval(loadFeed, 120000);
</script>
</body>
</html>
`))

// -------------------- Server entry point --------------------
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/api/feeds", apiFeedsHandler)
	mux.HandleFunc("/api/sources", apiSourcesHandler)
	mux.HandleFunc("/api/sources/add", apiAddSourceHandler)
	mux.HandleFunc("/api/sources/remove", apiRemoveSourceHandler)

	// Register any extra handlers developers added
	for _, eh := range extraHandlers {
		mux.HandleFunc(eh.Pattern, eh.Handler)
	}

	addr := ":8080"
	log.Printf("🚀 Stenella listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}