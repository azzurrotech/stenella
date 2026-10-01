package web

import (
	"net/http"
	"strings"
	"testing"
)

// The static handler serves the embedded bundle and nothing else. This is the
// join between the two script tags in partials.html and the files on disk: a
// rename in web/static/ that misses the embed pattern fails here rather than as a
// 404 in a browser.
func TestStaticAssetsAreEmbedded(t *testing.T) {
	s, _ := newTestWeb(t)
	for _, name := range []string{"app.js", "collaboration.js", "style.css"} {
		rec := webReq(t, s.Handler(), http.MethodGet, "/s/static/"+name, "", nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /s/static/%s = %d, want 200: %s", name, rec.Code, rec.Body.String())
			continue
		}
		if rec.Body.Len() == 0 {
			t.Errorf("/s/static/%s is empty", name)
		}
	}
	// An unknown file is a 404, not the index page.
	if rec := webReq(t, s.Handler(), http.MethodGet, "/s/static/nope.js", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET /s/static/nope.js = %d, want 404", rec.Code)
	}
}

// Every page includes the scripts partial, and the partial loads vici before the
// bundle that feature-detects it. Order matters: collaboration.js calls
// window.vici at boot, and a script tag after it would see undefined.
func TestScriptOrderInPartial(t *testing.T) {
	s, _ := newTestWeb(t)
	if err := s.parseTemplates(); err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	var sb strings.Builder
	if err := s.templates.ExecuteTemplate(&sb, "scripts", nil); err != nil {
		t.Fatalf("execute scripts: %v", err)
	}
	out := sb.String()
	want := []string{
		"/s/static/lib/veni.js", "/s/static/lib/vidi.js", "/s/static/lib/vici.js",
		"/s/static/lib/vini.js", "/s/static/app.js", "/s/static/collaboration.js",
	}
	last := -1
	for _, src := range want {
		at := strings.Index(out, src)
		if at < 0 {
			t.Fatalf("the scripts partial is missing %s:\n%s", src, out)
		}
		if at < last {
			t.Errorf("%s is loaded after a later bundle:\n%s", src, out)
		}
		last = at
	}
}

// The pages that need a key say so on the <body>, and the bundle keys off that.
// A missing data-page would make collaboration.js return before doing anything,
// with no error anywhere.
func TestPagesDeclareTheirPage(t *testing.T) {
	s, _ := newTestWeb(t)
	if err := s.parseTemplates(); err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	for _, tc := range []struct {
		name       string
		wantPage   string
		wantClient bool
	}{
		{"portal", "portal", true},
		{"admin", "admin", false},
		{"signup", "signup", false},
	} {
		var sb strings.Builder
		if err := s.templates.ExecuteTemplate(&sb, tc.name, struct {
			Title  string
			Client string
			Error  string
		}{Title: "t", Client: "acme"}); err != nil {
			t.Errorf("execute %s: %v", tc.name, err)
			continue
		}
		out := sb.String()
		if !strings.Contains(out, `data-page="`+tc.wantPage+`"`) {
			t.Errorf("%s does not declare data-page=%q", tc.name, tc.wantPage)
		}
		if tc.wantClient && !strings.Contains(out, `data-client="acme"`) {
			t.Errorf("%s does not declare its client", tc.name)
		}
	}
}

// The portal and admin pages both carry the new panes the bundle renders into. A
// missing element is a silent no-op at runtime, so it is asserted here.
func TestCollaborationTargetsExist(t *testing.T) {
	s, _ := newTestWeb(t)
	if err := s.parseTemplates(); err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	for _, tc := range []struct {
		name  string
		ids   []string
		attrs []string
	}{
		{"portal", []string{"items-list", "items-q", "items-search-note", "pane-items", "logout-btn"}, nil},
		{"admin", []string{"usage-view", "retention-view", "retention-sweep-btn"}, []string{`data-tab="usage"`}},
	} {
		var sb strings.Builder
		if err := s.templates.ExecuteTemplate(&sb, tc.name, struct {
			Title  string
			Client string
			Error  string
		}{Title: "t", Client: "acme"}); err != nil {
			t.Errorf("execute %s: %v", tc.name, err)
			continue
		}
		out := sb.String()
		for _, id := range tc.ids {
			if !strings.Contains(out, `id="`+id+`"`) {
				t.Errorf("%s is missing #%s, which the bundle renders into", tc.name, id)
			}
		}
		for _, attr := range tc.attrs {
			if !strings.Contains(out, attr) {
				t.Errorf("%s is missing %s", tc.name, attr)
			}
		}
	}
}
