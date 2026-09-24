package web

import (
	"embed"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"azzurrotech/stenella/feed"
)

//go:embed templates/*.html static/style.css static/app.js
var assets embed.FS

// tplFuncs are shared template helpers.
var tplFuncs = template.FuncMap{
	"safe":  func(s string) template.HTML { return template.HTML(sanitizeHTML(s)) },
	"lower": strings.ToLower,
}

// Server.templates is parsed once and safe for concurrent execution.
func (s *Server) parseTemplates() error {
	t, err := template.New("").Funcs(tplFuncs).ParseFS(assets, "templates/*.html")
	if err != nil {
		return err
	}
	s.templates = t
	return nil
}

// renderPage executes the named template with the shared "head"/"nav"/"foot"
// partials available.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, name string, data any) {
	if s.templates == nil {
		if err := s.parseTemplates(); err != nil {
			http.Error(w, "templates unavailable: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// sanitizeHTML is a conservative allow-list sanitizer for feed content
// rendered on share pages. It strips <script>, <style>, iframes, on* handlers
// and javascript: URLs so a malicious feed cannot run script on our origin.
func sanitizeHTML(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	tokens := []string{
		"<script", "</script", "<iframe", "</iframe", "<style", "</style",
		"<object", "</object", "<embed", "</embed", "<link", "<meta",
	}
	for _, t := range tokens {
		s = strings.ReplaceAll(s, t, "")
	}
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			b.WriteRune(r)
			continue
		}
		if r == '>' {
			inTag = false
			b.WriteRune(r)
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	// Drop event handlers and javascript: URLs inside the remaining tags.
	out := b.String()
	up := strings.NewReplacer(" on", " x-", " ON", " X-")
	out = up.Replace(out)
	out = strings.ReplaceAll(out, "javascript:", "#")
	return out
}

// ---- pages --------------------------------------------------------------------

type homeCard struct {
	ID, Name, Notes string
	SiloURL         string
	FeedURL         string
	Sources, Items  int
}

type homeData struct {
	Title    string
	Host     string
	Cards    []homeCard
	HasFeeds bool
}

// handleHome is the search / link interface. It is deliberately not a direct
// file: it lists every hosted front door (client sites, combined feeds, admin
// consoles) and lets the user search across them.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	clients, _ := s.atp.ListClients()
	cards := make([]homeCard, 0, len(clients))
	for _, c := range clients {
		card := homeCard{
			ID: c.ID, Name: c.Name, Notes: c.Notes,
			SiloURL: "/c/" + c.ID + "/",
			FeedURL: "/s/feed/" + c.ID,
		}
		card.Sources = len(s.feeds.Sources(c.ID))
		card.Items = s.feeds.Combined(c.ID, feed.Query{Page: 1, PageSize: 0}).Total
		cards = append(cards, card)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	hasFeeds := false
	for _, c := range cards {
		if c.Items > 0 || c.Sources > 0 {
			hasFeeds = true
			break
		}
	}
	s.renderPage(w, r, "home", homeData{
		Title: "stenella", Host: s.baseURL, Cards: cards, HasFeeds: hasFeeds,
	})
}

// handlePortal renders the client workspace (JS-driven). The indexed client is
// read from the query string so the URL is shareable between admin and client.
func (s *Server) handlePortal(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	s.renderPage(w, r, "portal", map[string]any{
		"Title":  "Portal",
		"Client": client,
	})
}

// handleAdmin renders the super-admin console (JS-driven). Authentication for
// its API happens via atp's admin session cookie on the same origin.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "admin", map[string]any{
		"Title": "Super Admin",
	})
}

// handleStatic serves styles, the shared app bundle and the four JS libraries.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	switch name {
	case "style.css":
		b, _ := assets.ReadFile("static/style.css")
		if b == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Write(b)
		return
	case "app.js":
		b, _ := assets.ReadFile("static/app.js")
		if b == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(b)
		return
	}
	// /s/static/lib/<libname> serves the Emperor42 libraries passed from main.
	if lib, ok := strings.CutPrefix(name, "lib/"); ok {
		if b, ok2 := s.libs[lib]; ok2 {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Write(b)
			return
		}
	}
	http.NotFound(w, r)
}
