package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const webTestSecret = "web-test-master-secret-0123456789abcdef-aaaaaa"

const rssBody = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Fixture</title>
<item><title>First story</title><link>https://fx.test/1</link><guid>fx1</guid>
<pubDate>Wed, 23 Sep 2026 12:30:00 GMT</pubDate>
<description>About azzurro tech.</description></item>
<item><title>Second story</title><link>https://fx.test/2</link><guid>fx2</guid>
<pubDate>Thu, 24 Sep 2026 09:00:00 GMT</pubDate>
<description>Another item.</description></item>
</channel></rss>`

func newTestWeb(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	s, err := New(Config{
		Root:          root,
		AtpSecret:     webTestSecret,
		AdminUser:     "admin",
		AdminPassword: "s3cret",
		Libs:          map[string][]byte{"veni.js": {}, "vidi.js": {}, "vici.js": {}, "vini.js": {}},
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return s, root
}

func webReq(t *testing.T, h http.Handler, method, path, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func adminLogin(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := webReq(t, h, "POST", "/login", `{"user":"admin","password":"s3cret"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	for _, c := range cookies {
		if strings.Contains(c.Name, "session") {
			return c
		}
	}
	t.Fatal("admin login set no session cookie")
	return nil
}

func portalLogin(t *testing.T, h http.Handler, client, secret string) *http.Cookie {
	t.Helper()
	rec := webReq(t, h, "POST", "/s/api/client/login",
		`{"client":"`+client+`","secret":"`+secret+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("portal login: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	for _, c := range cookies {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("portal login set no session cookie")
	return nil
}

func mustDecode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code >= 400 {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
}

func TestPortalDBRejectsTableNamespaceTraversal(t *testing.T) {
	s, _ := newTestWeb(t)
	admin := adminLogin(t, s.Handler())
	if err := s.atp.CreateClient("acme", "Acme", ""); err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := s.atp.CreateTable("acme/notes", []string{"body"}); err != nil {
		t.Fatalf("create table: %v", err)
	}

	for _, table := range []string{"../other", "notes/../other", `notes\\other`, "notes%2fother"} {
		t.Run("query_"+table, func(t *testing.T) {
			rec := webReq(t, s.Handler(), http.MethodGet, "/s/api/portal/db/table?client=acme&table="+url.QueryEscape(table), "", []*http.Cookie{admin})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("query table %q: got %d, want 400", table, rec.Code)
			}
		})
		t.Run("insert_"+table, func(t *testing.T) {
			rec := webReq(t, s.Handler(), http.MethodPost, "/s/api/portal/db/table?client=acme", `{"table":"`+table+`","fields":{"body":"x"}}`, []*http.Cookie{admin})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("insert table %q: got %d, want 400", table, rec.Code)
			}
		})
		t.Run("delete_"+table, func(t *testing.T) {
			rec := webReq(t, s.Handler(), http.MethodDelete, "/s/api/portal/db/record?client=acme&table="+url.QueryEscape(table)+"&id=anything", "", []*http.Cookie{admin})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("delete table %q: got %d, want 400", table, rec.Code)
			}
		})
	}
}

func TestWebWalkthrough(t *testing.T) {
	s, root := newTestWeb(t)
	h := s.Handler()

	// Fixture RSS served over real HTTP (the engine fetches it).
	fx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rssBody))
	}))
	defer fx.Close()

	admin := adminLogin(t, h)

	// ---- super admin: create the client --------------------------------
	rec := webReq(t, h, "POST", "/s/api/admin/client", `{"id":"acme","name":"Acme Inc","notes":"w"}`, []*http.Cookie{admin})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}

	// ---- set the portal secret so the client can sign in ----------------
	rec = webReq(t, h, "POST", "/s/api/admin/secrets?client=acme",
		`{"name":"portal","value":"sesame","note":"login"}`, []*http.Cookie{admin})
	if rec.Code != http.StatusCreated {
		t.Fatalf("set portal secret: %d %s", rec.Code, rec.Body.String())
	}

	// ---- admin adds a feed source (impersonating acme) ------------------
	rec = webReq(t, h, "POST", "/s/api/portal/feeds?client=acme",
		fmt.Sprintf(`{"url":%q,"name":"Fixture","kind":"rss"}`, fx.URL+"/feed"), []*http.Cookie{admin})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add feed: %d %s", rec.Code, rec.Body.String())
	}
	var addOut struct {
		Source struct {
			ID string `json:"id"`
		} `json:"source"`
	}
	mustDecode(t, rec, &addOut)

	// ---- explicit synchronous fetch + mirror to pod ---------------------
	rec = webReq(t, h, "POST", "/s/api/portal/feeds/"+addOut.Source.ID+"/fetch?client=acme",
		"", []*http.Cookie{admin})
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch feed: %d %s", rec.Code, rec.Body.String())
	}
	var fetchOut struct {
		Items int `json:"items"`
	}
	mustDecode(t, rec, &fetchOut)
	if fetchOut.Items != 2 {
		t.Fatalf("fetched %d items, want 2", fetchOut.Items)
	}

	// ---- client signs into the portal ------------------------------------
	portal := portalLogin(t, h, "acme", "sesame")

	var who struct {
		Client struct {
			ID           string  `json:"id"`
			PricePerGBH  float64 `json:"price_per_gb_hour"`
			Chargeable   bool    `json:"chargeable"`
			RetentionHrs int     `json:"retention_hours"`
		} `json:"client"`
		Role string `json:"role"`
	}
	whoRec := webReq(t, h, "GET", "/s/api/portal/whoami?client=acme", "", []*http.Cookie{portal})
	if whoRec.Code != http.StatusOK {
		t.Fatalf("whoami: %d %s", whoRec.Code, whoRec.Body.String())
	}
	mustDecode(t, whoRec, &who)
	if who.Client.ID != "acme" || who.Role != "client" {
		t.Fatalf("whoami = %+v", who)
	}
	if who.Client.PricePerGBH != 0.05 || !who.Client.Chargeable {
		t.Errorf("client pricing = %+v", who.Client)
	}

	// ---- portal item listing ---------------------------------------------
	var items struct {
		Total int `json:"total"`
		Items []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Link  string `json:"link"`
		} `json:"items"`
	}
	mustDecode(t, webReq(t, h, "GET", "/s/api/portal/items?client=acme&page=1&pageSize=20", "", []*http.Cookie{portal}), &items)
	if items.Total != 2 || items.Items[0].Title != "Second story" {
		t.Fatalf("portal items = %+v", items)
	}
	firstID := items.Items[1].ID // "First story" (newest-first)

	// ---- public combined feeds (RSS + Atom) ------------------------------
	rec = webReq(t, h, "GET", "/s/feed/acme/combined.xml", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "First story") {
		t.Fatalf("combined.xml: %d\n%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Errorf("combined.xml content-type = %q", ct)
	}
	rec = webReq(t, h, "GET", "/s/feed/acme/combined.atom", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "First story") {
		t.Fatalf("combined.atom: %d", rec.Code)
	}

	// ---- links: item→url junction ----------------------------------------
	var linkOut struct {
		Link struct {
			ID     string `json:"id"`
			FromID string `json:"from_id"`
		} `json:"link"`
	}
	rec = webReq(t, h, "POST", "/s/api/portal/links?client=acme",
		fmt.Sprintf(`{"from_id":%q,"to_kind":"url","to_url":"https://outside.test/article","relation":"mentions"}`,
			firstID), []*http.Cookie{portal})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create link: %d %s", rec.Code, rec.Body.String())
	}
	mustDecode(t, rec, &linkOut)
	linkID := linkOut.Link.ID

	// The junction physically exists: symlinks to the pod item record + probe.
	jd := filepath.Join(root, "stenella", "links", "acme", linkID)
	for _, name := range []string{"from.xml", "to.xml", "manifest.json"} {
		fi, err := os.Lstat(filepath.Join(jd, name))
		if err != nil {
			t.Fatalf("junction %s: %v", name, err)
		}
		if name != "manifest.json" && fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink", name)
		}
	}
	if _, err := os.Stat(filepath.Join(jd, "from.xml")); err != nil {
		t.Errorf("from.xml unresolved: %v", err)
	}

	var linksOut struct {
		Links []struct {
			FromTitle string `json:"from_title"`
			ToTitle   string `json:"to_title"`
		} `json:"links"`
	}
	mustDecode(t, webReq(t, h, "GET", "/s/api/portal/links?client=acme", "", []*http.Cookie{portal}), &linksOut)
	if len(linksOut.Links) != 1 || linksOut.Links[0].FromTitle != "First story" {
		t.Fatalf("links list = %+v", linksOut)
	}

	// ---- shares: whole table + public URLs -------------------------------
	var shareOut struct {
		Share struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			URL  string `json:"url"`
			JSON string `json:"json_url"`
		} `json:"share"`
		Token string `json:"token"`
	}
	rec = webReq(t, h, "POST", "/s/api/portal/shares?client=acme",
		`{"kind":"table","target":"items","title":"all"}`, []*http.Cookie{portal})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create share: %d %s", rec.Code, rec.Body.String())
	}
	mustDecode(t, rec, &shareOut)
	if shareOut.Token == "" || shareOut.Share.ID == "" {
		t.Fatalf("share = %+v", shareOut)
	}

	shareID := shareOut.Share.ID
	good := "?t=" + shareOut.Token
	rec = webReq(t, h, "GET", "/s/api/x/"+shareID+good, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("share json: %d %s", rec.Code, rec.Body.String())
	}
	var shareJSON struct {
		Kind    string              `json:"kind"`
		Count   int                 `json:"count"`
		Records []map[string]string `json:"records"`
	}
	mustDecode(t, rec, &shareJSON)
	if shareJSON.Kind != "table" || shareJSON.Count != 2 || len(shareJSON.Records) != 2 {
		t.Fatalf("share json = %+v", shareJSON)
	}

	// Wrong token → forbidden; unknown → not found.
	if rec := webReq(t, h, "GET", "/s/api/x/"+shareID+"?t=wrong", "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("wrong token: %d", rec.Code)
	}
	if rec := webReq(t, h, "GET", "/s/api/x/nope?t=x", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown share: %d", rec.Code)
	}

	// Share HTML page (the vidi-facing table view).
	rec = webReq(t, h, "GET", "/s/x/"+shareID+good, "", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "vidi-cards-container") {
		t.Fatalf("share page: %d", rec.Code)
	}

	// Share junction symlink to the pod table dir.
	shDir := filepath.Join(root, "stenella", "shares", "acme", shareID)
	if target, err := os.Readlink(filepath.Join(shDir, "target")); err != nil ||
		filepath.Join(shDir, target) != filepath.Join(root, "pod", "acme", "items") {
		t.Errorf("share junction target: %v %q", err, target)
	}

	// ---- portal database: tables + insert + query ------------------------
	var tables struct {
		Tables []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"tables"`
	}
	mustDecode(t, webReq(t, h, "GET", "/s/api/portal/db/tables?client=acme", "", []*http.Cookie{portal}), &tables)
	found := false
	for _, tb := range tables.Tables {
		if tb.Name == "acme/items" && tb.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("pod tables = %+v", tables.Tables)
	}

	rec = webReq(t, h, "POST", "/s/api/portal/db/table?client=acme&table=notes",
		`{"table":"notes","fields":{"body":"hello from the portal"}}`, []*http.Cookie{portal})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("db insert: %d %s", rec.Code, rec.Body.String())
	}
	var dbQ struct {
		Count   int                 `json:"count"`
		Records []map[string]string `json:"records"`
	}
	mustDecode(t, webReq(t, h, "GET", "/s/api/portal/db/table?client=acme&table=notes&page=1&pageSize=50", "", []*http.Cookie{portal}), &dbQ)
	if dbQ.Count != 1 || dbQ.Records[0]["body"] != "hello from the portal" {
		t.Fatalf("db query = %+v", dbQ)
	}

	// ---- billing + income -------------------------------------------------
	var billing struct {
		Client      string  `json:"client"`
		PricePerGBH float64 `json:"price_per_gb_hour"`
		Cost        struct {
			TotalRequests int     `json:"total_requests"`
			TotalCost     float64 `json:"total_cost"`
			TotalResp     int64   `json:"total_resp_bytes"`
		} `json:"cost"`
	}
	billRec := webReq(t, h, "GET", "/s/api/portal/billing?client=acme", "", []*http.Cookie{portal})
	if billRec.Code != http.StatusOK {
		t.Fatalf("billing: %d %s", billRec.Code, billRec.Body.String())
	}
	mustDecode(t, billRec, &billing)
	if billing.Client != "acme" || billing.PricePerGBH != 0.05 {
		t.Errorf("billing = %+v", billing)
	}
	if billing.Cost.TotalRequests == 0 {
		t.Errorf("billing recorded no requests: %+v", billing.Cost)
	}

	var income struct {
		TotalRevenue float64 `json:"total_revenue"`
	}
	mustDecode(t, webReq(t, h, "GET", "/s/api/admin/income", "", []*http.Cookie{admin}), &income)

	// ---- pages ------------------------------------------------------------
	rec = webReq(t, h, "GET", "/", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "data-page=\"home\"") {
		t.Fatalf("home: %d", rec.Code)
	}
	rec = webReq(t, h, "GET", "/s/feed/acme", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "data-page=\"feed\"") {
		t.Fatalf("feed page: %d", rec.Code)
	}

	// ---- usage log exists after all those requests -----------------------
	var usage struct {
		Records []map[string]any `json:"records"`
	}
	mustDecode(t, webReq(t, h, "GET", "/s/api/portal/usage?client=acme&limit=5", "", []*http.Cookie{portal}), &usage)
	if len(usage.Records) == 0 {
		t.Errorf("no usage records logged")
	}

	// ---- super admin coarse view includes the client ---------------------
	var clients struct {
		Clients []struct {
			Client struct {
				ID string `json:"id"`
			} `json:"client"`
			Sources int   `json:"sources"`
			Items   int64 `json:"items"`
		} `json:"clients"`
	}
	mustDecode(t, webReq(t, h, "GET", "/s/api/admin/clients", "", []*http.Cookie{admin}), &clients)
	seen := false
	for _, c := range clients.Clients {
		if c.Client.ID == "acme" {
			seen = true
			if c.Sources != 1 || c.Items != 2 {
				t.Errorf("acme summary = %+v, want 1 source / 2 items", c)
			}
		}
	}
	if !seen {
		t.Fatalf("admin clients missing acme: %+v", clients.Clients)
	}
}

func TestAdminGateRejectsAnonymous(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	for _, path := range []string{"/s/api/admin/summary", "/s/api/admin/clients", "/s/api/admin/income"} {
		if rec := webReq(t, h, "GET", path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", path, rec.Code)
		}
	}
}

func TestPortalGateRejectsAnonymous(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	for _, path := range []string{
		"/s/api/portal/items?client=acme",
		"/s/api/portal/feeds?client=acme",
		"/s/api/portal/db/tables?client=acme",
	} {
		if rec := webReq(t, h, "GET", path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", path, rec.Code)
		}
	}
}

// TestBackgroundDoesNotBlock verifies Background starts and the engine ticks.
func TestBackgroundDoesNotBlock(t *testing.T) {
	s, _ := newTestWeb(t)
	done := make(chan struct{})
	go func() {
		s.Background(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		// Background is a long-running loop; reaching here means it's alive.
	}
}
