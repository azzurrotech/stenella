package stenella_legacy

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

type FeedItem struct {
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Description string    `json:"description"`
	Published   time.Time `json:"published"`
	Source      string    `json:"source"`
}

var (
	feedSources = []string{
		"https://news.ycombinator.com/rss",
		"https://www.reddit.com/r/golang/.rss",
	}
	srcMu sync.RWMutex
)

func FetchFeed(feedURL string) ([]FeedItem, error) {
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

func parsePubDate(v string) (time.Time, error) {
	layouts := []string{
		time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339,
	}
	var t time.Time
	var err error
	for _, l := range layouts {
		t, err = time.Parse(l, v)
		if err == nil {
			return t, nil
		}
	}
	if t, err = time.Parse("Mon, 02 Jan 2006 15:04:05", v); err == nil {
		return t, nil
	}
	return time.Now(), fmt.Errorf("unparseable date %q", v)
}

func AggregateFeeds() ([]FeedItem, error) {
	srcMu.RLock()
	sources := make([]string, len(feedSources))
	copy(sources, feedSources)
	srcMu.RUnlock()

	var all []FeedItem
	for _, src := range sources {
		itms, err := FetchFeed(src)
		if err != nil {
			log.Printf("[WARN] could not fetch %s: %v", src, err)
			continue
		}
		all = append(all, itms...)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Published.After(all[j].Published)
	})
	return all, nil
}

func GetSources() []string {
	srcMu.RLock()
	list := make([]string, len(feedSources))
	copy(list, feedSources)
	srcMu.RUnlock()
	return list
}

func AddSource(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || !u.IsAbs() {
		return fmt.Errorf("invalid URL")
	}
	srcMu.Lock()
	defer srcMu.Unlock()
	for _, s := range feedSources {
		if s == rawURL {
			return fmt.Errorf("source already exists")
		}
	}
	feedSources = append(feedSources, rawURL)
	return nil
}

func RemoveSource(rawURL string) error {
	srcMu.Lock()
	defer srcMu.Unlock()
	for i, s := range feedSources {
		if s == rawURL {
			feedSources = append(feedSources[:i], feedSources[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("source not found")
}

type extraHandler struct {
	pattern string
	handler http.HandlerFunc
}

var (
	extraHandlers []extraHandler
	extraMu       sync.Mutex
)

func AddExtraHandler(pattern string, handler http.HandlerFunc) {
	extraMu.Lock()
	defer extraMu.Unlock()
	extraHandlers = append(extraHandlers, extraHandler{pattern: pattern, handler: handler})
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		extraMu.Lock()
		handlers := make([]extraHandler, len(extraHandlers))
		copy(handlers, extraHandlers)
		extraMu.Unlock()
		for _, eh := range handlers {
			if r.URL.Path == eh.pattern {
				eh.handler(w, r)
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	PageTmpl.Execute(w, nil)
}

func apiFeedsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := AggregateFeeds()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func apiSourcesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sources := GetSources()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sources)
}

func apiAddSourceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if err := AddSource(req.URL); err != nil {
		if err.Error() == "source already exists" {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func apiRemoveSourceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if err := RemoveSource(req.URL); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func SetupMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/api/feeds", apiFeedsHandler)
	mux.HandleFunc("/api/sources", apiSourcesHandler)
	mux.HandleFunc("/api/sources/add", apiAddSourceHandler)
	mux.HandleFunc("/api/sources/remove", apiRemoveSourceHandler)
	return mux
}

var PageTmpl = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Stenella – Combined RSS Viewer</title>
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
</style></head>
<body>
<header><h1>Combined RSS Feed</h1></header>
<div class="container">
  <div id="feed"><em>Loading feed items…</em></div>
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
async function postJSON(url, data) {
	const resp = await fetch(url, {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(data)});
	return resp;
}
async function loadFeed() {
	const resp = await fetch('/api/feeds');
	if (!resp.ok) {document.getElementById('feed').innerHTML='<em>Error loading feeds</em>';return;}
	const items = await resp.json();
	const container = document.getElementById('feed'); container.innerHTML = '';
	items.forEach(i => {
		const div = document.createElement('div'); div.className = 'item';
		div.innerHTML = '<div class="title"><a href="'+i.link+'" target="_blank">'+i.title+'</a></div><div class="meta">'+new Date(i.published).toLocaleString()+' &bull; '+i.source+'</div><div class="desc">'+i.description+'</div>';
		container.appendChild(div);
	});
}
async function loadSources() {
	const resp = await fetch('/api/sources');
	if (!resp.ok) {document.getElementById('source-list').innerHTML='<em>Error loading sources</em>';return;}
	const list = await resp.json();
	const container = document.getElementById('source-list'); container.innerHTML = '';
	list.forEach(url => {
		const div = document.createElement('div'); div.className = 'source';
		div.innerHTML = '<span>'+url+'</span><button data-url="'+url+'">✖</button>';
		container.appendChild(div);
	});
	container.querySelectorAll('button').forEach(btn => {
		btn.addEventListener('click', async e => {
			const url = e.target.dataset.url;
			const res = await postJSON('/api/sources/remove', {url});
			if (res.ok) { loadSources(); loadFeed(); } else { alert('Failed to remove source'); }
		});
	});
}
document.getElementById('add-form').addEventListener('submit', async e => {
	e.preventDefault();
	const url = document.getElementById('new-url').value.trim();
	if (!url) return;
	const res = await postJSON('/api/sources/add', {url});
	if (res.status === 201) { document.getElementById('new-url').value = ''; loadSources(); loadFeed(); }
	else if (res.status === 409) { alert('Source already exists'); } else { alert('Failed to add source'); }
});
loadFeed(); loadSources();
setInterval(loadFeed, 120000);
</script>
</body></html>`))
