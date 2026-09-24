// Package web implements the stenella data platform as a server that embeds
// atp and uses it as middleware.
//
// atp keeps its own namespace (/api, /clients, /c, /gw, /login, /health);
// stenella serves its search/link homepage at "/" and everything else under
// /s/: the client portal, the super-admin view, the public combined feeds and
// the share URLs. Every song/pod/shepherd feature a page needs is called
// through atp's own HTTP handler (see atpclient) — nothing is re-implemented
// here, and stenella never imports song, pod or shepherd directly.
package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	atpweb "azzurrotech/atp/web"
	"azzurrotech/stenella/atpclient"
	"azzurrotech/stenella/billing"
	"azzurrotech/stenella/feed"
	"azzurrotech/stenella/links"
)

// Config wires the embedded atp orchestrator and stenella itself.
type Config struct {
	// Root is the shared data root (clients, secrets, song + pod stores, and
	// stenella's own feeds/links/shares/config live under it).
	Root string
	// AtpSecret is the atp master secret (>= 32 bytes).
	AtpSecret string
	// AdminUser / AdminPassword are the atp administrator credentials.
	AdminUser     string
	AdminPassword string
	// PublicBase prefixes share URLs when non-empty (e.g. https://azzurro.tech).
	PublicBase string
	// Libs are the four Emperor42 JS libraries, keyed by filename
	// (veni.js, vidi.js, vici.js, vini.js), served at /s/static/lib/<name>.
	Libs map[string][]byte
	// SiteHosts maps a public domain (host:port stripped, lower-cased) to a
	// client id whose hosted site is served at "/" on that host. This is how
	// azzurro.tech becomes the azzurrotech client site without a client
	// prefix in the URL.
	//
	// The platform keeps its own namespace on mapped hosts: anything under
	// /s/ (portal, admin, feeds, shares, JS libraries) stays reachable, and
	// PlatformPath redirects to the stenella portal for the mapped client.
	SiteHosts map[string]string
	// PlatformPath is the path on mapped hosts that redirects to the
	// stenella platform (portal for the mapped client). Defaults to
	// "/platform" when empty.
	PlatformPath string
}

// Server is a configured stenella instance.
type Server struct {
	cfg     Config
	root    string
	atpSvc  *atpweb.ATPService
	atp     *atpclient.Client
	feeds   *feed.Engine
	links   *links.Store
	shares  *links.Shares
	income  *billing.Calculator
	sess    *sessionStore
	libs    map[string][]byte
	baseURL string

	templates *template.Template
	mux       *http.ServeMux
}

// New builds the embedded atp service plus stenella's routes.
func New(cfg Config) (*Server, error) {
	if cfg.Root == "" {
		cfg.Root = "./data"
	}
	if cfg.AdminUser == "" {
		cfg.AdminUser = "admin"
	}
	if cfg.AdminPassword == "" {
		cfg.AdminPassword = "admin"
	}
	if cfg.PlatformPath == "" {
		cfg.PlatformPath = "/platform"
	}
	if cfg.SiteHosts == nil {
		cfg.SiteHosts = map[string]string{}
	}
	atpSvc, err := atpweb.NewATPService(atpweb.Options{
		Port:          "8084",
		Root:          cfg.Root,
		Secret:        cfg.AtpSecret,
		AdminUser:     cfg.AdminUser,
		AdminPassword: cfg.AdminPassword,
	})
	if err != nil {
		return nil, fmt.Errorf("atp: %w", err)
	}
	atp := atpclient.New(atpSvc.Handler(), cfg.AdminUser, cfg.AdminPassword)
	if err := atp.Login(); err != nil {
		// atp's own auth is stdlib HMAC; a failing login means misconfig.
		return nil, fmt.Errorf("atp admin login: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.Root, "stenella"), 0o755); err != nil {
		return nil, err
	}

	fe, err := feed.New(feed.Options{Root: filepath.Join(cfg.Root, "stenella")})
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:     cfg,
		root:    cfg.Root,
		atpSvc:  atpSvc,
		atp:     atp,
		feeds:   fe,
		links:   links.New(cfg.Root, atp),
		shares:  links.NewShares(cfg.Root, atp),
		sess:    newSessionStore(),
		libs:    cfg.Libs,
		baseURL: strings.TrimSuffix(cfg.PublicBase, "/"),
	}
	s.income, err = billing.New(cfg.Root, atp)
	if err != nil {
		return nil, err
	}

	s.mux = http.NewServeMux()
	s.routes()
	return s, nil
}

// Handler returns the full HTTP handler: atp's middleware first, then
// stenella's own mux (wrapped in the virtual-host dispatcher) for everything
// atp does not own.
func (s *Server) Handler() http.Handler {
	return s.atpSvc.Middleware(s.hostDispatch(s.mux))
}

// hostOnly normalises an HTTP Host header to a bare, lower-cased domain.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// cloneReq returns a copy of r whose URL path is rewritten to newPath (the
// query string is preserved) — used to forward a mapped-host request into atp's
// public /c/{client}/... surface without touching the client-visible URL.
func cloneReq(r *http.Request, newPath string) *http.Request {
	r2 := r.Clone(r.Context())
	u := *r.URL
	u.Path = newPath
	u.RawPath = ""
	r2.URL = &u
	r2.RequestURI = newPath
	if u.RawQuery != "" {
		r2.RequestURI += "?" + u.RawQuery
	}
	return r2
}

// hostDispatch implements virtual hosting: for hosts listed in
// Config.SiteHosts the mapped client's hosted site (their song silo, served
// via atp's public /c/{client}/... handler so usage metering and scoping
// still apply) is served at "/", PlatformPath redirects to the stenella
// platform, and everything under /s/ remains the platform's own namespace.
//
// atp's Middleware runs before this handler and already owns /api, /clients,
// /c, /gw, /login and /health, so this only ever sees stenella's paths.
func (s *Server) hostDispatch(next http.Handler) http.Handler {
	plat := s.cfg.PlatformPath
	if plat == "" {
		plat = "/platform"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, ok := s.cfg.SiteHosts[hostOnly(r.Host)]
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path

		// The documented "platform" endpoint: azzurro.tech/platform should
		// land on the stenella platform automatically.
		if p == plat || strings.HasPrefix(p, plat+"/") {
			target := "/s/portal?client=" + url.QueryEscape(client)
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}

		// The platform keeps its own namespace on mapped hosts.
		if p == "/s" || strings.HasPrefix(p, "/s/") {
			next.ServeHTTP(w, r)
			return
		}

		// Everything else on a mapped host is the client's hosted site.
		var newPath string
		if p == "/" {
			newPath = "/c/" + client + "/"
		} else {
			newPath = "/c/" + client + p
		}
		s.atp.Handler().ServeHTTP(w, cloneReq(r, newPath))
	})
}

// Routes registers every stenella path.
func (s *Server) routes() {
	m := s.mux

	// Pages.
	m.HandleFunc("GET /{$}", s.handleHome)
	m.HandleFunc("GET /s/portal", s.handlePortal)
	m.HandleFunc("GET /s/admin", s.handleAdmin)
	m.HandleFunc("GET /s/feed/{client}", s.handleClientFeedPage)
	m.HandleFunc("GET /s/feed/{client}/combined.xml", s.handleCombinedRSS)
	m.HandleFunc("GET /s/feed/{client}/combined.atom", s.handleCombinedAtom)
	m.HandleFunc("GET /s/feed/{client}/items", s.handleFeedItemsPublic)
	m.HandleFunc("GET /s/x/{id}", s.handleSharePage)
	m.HandleFunc("GET /s/static/{file...}", s.handleStatic)
	m.HandleFunc("GET /s/data/{client}/{table...}", s.handleSiteData)

	// Client sessions.
	m.HandleFunc("POST /s/api/client/login", s.handleClientLogin)
	m.HandleFunc("POST /s/api/client/logout", s.handleClientLogout)

	// Portal API — client session OR atp admin (impersonating the client).
	m.Handle("GET /s/api/portal/whoami", s.clientGate(s.handleWhoami))
	m.Handle("GET /s/api/portal/feeds", s.clientGate(s.handleListFeeds))
	m.Handle("POST /s/api/portal/feeds", s.clientGate(s.handleAddFeed))
	m.Handle("PUT /s/api/portal/feeds/{id}", s.clientGate(s.handleUpdateFeed))
	m.Handle("DELETE /s/api/portal/feeds/{id}", s.clientGate(s.handleDeleteFeed))
	m.Handle("POST /s/api/portal/feeds/{id}/fetch", s.clientGate(s.handleFetchFeed))
	m.Handle("POST /s/api/portal/import", s.clientGate(s.handleImportOPML))
	m.Handle("GET /s/api/portal/items", s.clientGate(s.handleFeedItems))
	m.Handle("POST /s/api/portal/refresh", s.clientGate(s.handleRefreshAll))

	m.Handle("GET /s/api/portal/links", s.clientGate(s.handleListLinks))
	m.Handle("POST /s/api/portal/links", s.clientGate(s.handleCreateLink))
	m.Handle("DELETE /s/api/portal/links/{id}", s.clientGate(s.handleDeleteLink))

	m.Handle("GET /s/api/portal/shares", s.clientGate(s.handleListShares))
	m.Handle("POST /s/api/portal/shares", s.clientGate(s.handleCreateShare))
	m.Handle("DELETE /s/api/portal/shares/{id}", s.clientGate(s.handleDeleteShare))

	m.Handle("GET /s/api/portal/db/tables", s.clientGate(s.handleDBTables))
	m.Handle("GET /s/api/portal/db/table", s.clientGate(s.handleDBQuery))
	m.Handle("POST /s/api/portal/db/table", s.clientGate(s.handleDBInsert))
	m.Handle("DELETE /s/api/portal/db/record", s.clientGate(s.handleDBDelete))

	m.Handle("GET /s/api/portal/sites/meta", s.clientGate(s.handleSitesMeta))
	m.Handle("GET /s/api/portal/sites/files", s.clientGate(s.handleSitesFiles))
	m.Handle("POST /s/api/portal/sites/file", s.clientGate(s.handleSitesCreate))
	m.Handle("PUT /s/api/portal/sites/file", s.clientGate(s.handleSitesUpdate))
	m.Handle("DELETE /s/api/portal/sites/file", s.clientGate(s.handleSitesDelete))

	m.Handle("GET /s/api/portal/secrets", s.clientGate(s.handleListClientSecrets))
	m.Handle("POST /s/api/portal/secrets", s.clientGate(s.handleSetClientSecret))
	m.Handle("DELETE /s/api/portal/secrets", s.clientGate(s.handleDeleteClientSecret))
	m.Handle("GET /s/api/portal/payment", s.clientGate(s.handleGetPayment))
	m.Handle("PUT /s/api/portal/payment", s.clientGate(s.handlePutPayment))

	m.Handle("GET /s/api/portal/billing", s.clientGate(s.handleClientBilling))
	m.Handle("GET /s/api/portal/usage", s.clientGate(s.handleClientUsage))

	m.Handle("POST /s/api/portal/keys", s.clientGate(s.handleIssueKey))
	m.Handle("GET /s/api/portal/keys/verify", s.clientGate(s.handleVerifyKey))
	m.Handle("POST /s/api/portal/revoke", s.clientGate(s.handleRevokeKey))

	// Super-admin API (atp admin required).
	m.Handle("GET /s/api/admin/summary", s.adminGate(s.handleAdminSummary))
	m.Handle("GET /s/api/admin/income", s.adminGate(s.handleAdminIncome))
	m.Handle("PUT /s/api/admin/income", s.adminGate(s.handleAdminPutIncome))
	m.Handle("GET /s/api/admin/clients", s.adminGate(s.handleAdminClients))
	m.Handle("POST /s/api/admin/client", s.adminGate(s.handleAdminClientCreate))
	m.Handle("PUT /s/api/admin/client/{id}", s.adminGate(s.handleAdminClientUpdate))
	m.Handle("DELETE /s/api/admin/client/{id}", s.adminGate(s.handleAdminClientDelete))
	m.Handle("GET /s/api/admin/secrets", s.adminGate(s.handleAdminSecretsList))
	m.Handle("POST /s/api/admin/secrets", s.adminGate(s.handleAdminSecretSet))
	m.Handle("DELETE /s/api/admin/secrets", s.adminGate(s.handleAdminSecretDelete))
	m.Handle("GET /s/api/admin/feeds", s.adminGate(s.handleAdminFeeds))
	m.Handle("GET /s/api/admin/config", s.adminGate(s.handleAdminConfig))
	m.Handle("PUT /s/api/admin/config", s.adminGate(s.handleAdminPutConfig))

	// Public share JSON (for vidi embedding) and share page.
	m.HandleFunc("GET /s/api/x/{id}", s.handleShareJSON)
}

// ---- helpers shared by handlers ---------------------------------------------

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) writeErr(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, map[string]any{"error": msg})
}

func (s *Server) readBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// secretEqual is a constant-time string comparison used for portal logins.
func secretEqual(a, b string) bool {
	if len(a) != len(b) {
		// Still do equal work to avoid leaking length differences cheaply.
		subtle.ConstantTimeCompare([]byte(a), []byte(b))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// mirrorToPod writes a client's fetched items into their pod items table so
// the feed is addressable as database rows (rendered by vidi, linkable,
// shareable). The id is the stable feed item hash.
func (s *Server) mirrorToPod(client string, fetched *feed.FetchedFeed) {
	table := links.ItemsTable(client)
	for i := range fetched.Items {
		it := &fetched.Items[i]
		cats := strings.Join(it.Categories, "|")
		_, err := s.atp.UpsertRecord(table, map[string]string{
			"id":          it.ID,
			"source_id":   it.SourceID,
			"source_name": it.SourceName,
			"title":       it.Title,
			"link":        it.Link,
			"guid":        it.GUID,
			"author":      it.Author,
			"summary":     it.Summary,
			"content":     it.Content,
			"categories":  cats,
			"published":   it.Published.Format(time.RFC3339),
			"updated":     it.Updated.Format(time.RFC3339),
			"fetched":     it.Fetched.Format(time.RFC3339),
		})
		if err != nil && !isNotFound(err) {
			log.Printf("web: mirror %s item %s: %v", client, it.ID, err)
		}
	}
}

// secretResolver lets the feed engine fetch named vault secrets for
// authenticated sources.
func (s *Server) secretResolver() feed.SecretResolver {
	return func(client, name string) (string, error) {
		if name == "" {
			return "", nil
		}
		return s.atp.GetSecret(client, name)
	}
}

// Background starts the feed refresher and atp's own maintenance.
func (s *Server) Background(ctx context.Context) {
	go s.feeds.Run(ctx, s.secretResolver())
}

// IsNotFound maps error helpers to a sentinel for the mirror loop.
func isNotFound(err error) bool {
	var he *atpclient.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

// httpClient is shared by outgoing fetches (feeds and OPML imports).
var httpClient = &http.Client{Timeout: 20 * time.Second}

func httpFetch(raw string) ([]byte, error) {
	res, err := httpClient.Get(raw)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("remote returned %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 8<<20))
}
