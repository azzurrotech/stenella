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
	"path"
	"path/filepath"
	"strconv"
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
	// SiteHosts maps a public domain to a client id whose hosted site is
	// served at "/" on that host. Host keys are canonicalized on startup:
	// case, a trailing dot, and an optional valid numeric port are ignored.
	// This is how azzurro.tech becomes the azzurrotech client site without a client
	// prefix in the URL. Invalid host or client values are rejected by New.
	//
	// The platform keeps its own namespace on mapped hosts: anything under
	// /s/ (portal, admin, feeds, shares, JS libraries) stays reachable, and
	// PlatformPath redirects to the stenella portal for the mapped client.
	SiteHosts map[string]string
	// PlatformPath is the path on mapped hosts that redirects to the
	// stenella platform (portal for the mapped client). It is normalized to
	// an absolute, boundary-safe path and defaults to "/platform" when empty.
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
		if envPass := os.Getenv("STENELLA_ADMIN_PASSWORD"); envPass != "" {
			cfg.AdminPassword = envPass
		} else {
			cfg.AdminPassword = "admin"
		}
	}
	siteHosts, err := normalizeSiteHosts(cfg.SiteHosts)
	if err != nil {
		return nil, fmt.Errorf("SiteHosts: %w", err)
	}
	platformPath, err := normalizePlatformPath(cfg.PlatformPath)
	if err != nil {
		return nil, fmt.Errorf("PlatformPath: %w", err)
	}
	cfg.SiteHosts = siteHosts
	cfg.PlatformPath = platformPath
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

// Handler returns the full HTTP handler. Virtual-host dispatch is outermost
// so a mapped public host can never reach a different client's /c/ silo. The
// ATP middleware still owns its reserved API/management paths, and stenella's
// mux handles the remaining platform namespace.
func (s *Server) Handler() http.Handler {
	return s.hostDispatch(s.atpSvc.Middleware(s.siteDataPathGuard(s.mux)))
}

// normalizeSiteHosts returns a private, canonical copy of the host map. Host
// names are case-insensitive, an absolute DNS name may end in a dot, and an
// optional port does not change the site mapping. Everything else must be a
// valid host/client pair; silently dropping a bad entry could expose one host
// while an operator believes it is mapped to another client.
func normalizeSiteHosts(configured map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(configured))
	for rawHost, rawClient := range configured {
		host := hostOnly(strings.TrimSpace(rawHost))
		if host == "" {
			return nil, fmt.Errorf("invalid host %q", rawHost)
		}
		client := strings.TrimSpace(rawClient)
		if !validSiteClientID(client) {
			return nil, fmt.Errorf("invalid client id %q for host %q", rawClient, rawHost)
		}
		if previous, exists := out[host]; exists && previous != client {
			return nil, fmt.Errorf("host %q maps to both clients %q and %q", host, previous, client)
		}
		out[host] = client
	}
	return out, nil
}

func validSiteClientID(client string) bool {
	if client == "" || !isASCIIAlphaNumeric(client[0]) || !isASCIIAlphaNumeric(client[len(client)-1]) {
		return false
	}
	for i := 0; i < len(client); i++ {
		c := client[i]
		if !isASCIIAlphaNumeric(c) && c != '.' && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func isASCIIAlphaNumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// normalizePlatformPath canonicalizes a configured redirect path. A leading
// slash is added for convenience, duplicate/trailing slashes and dot segments
// are cleaned, and paths that would steal the platform or atp namespaces are
// rejected rather than creating a route that can never work.
func normalizePlatformPath(configured string) (string, error) {
	p := strings.TrimSpace(configured)
	if p == "" {
		p = "/platform"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if strings.HasPrefix(p, "//") {
		return "", fmt.Errorf("path %q must not start with //", configured)
	}
	if strings.ContainsAny(p, "?#") {
		return "", fmt.Errorf("path %q must not contain a query or fragment", configured)
	}
	u, err := url.ParseRequestURI(p)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", configured, err)
	}
	if u.Scheme != "" || u.Host != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return "", fmt.Errorf("path %q must not contain a scheme, authority, query, or fragment", configured)
	}
	p = path.Clean(u.Path)
	if p == "." || p == "/" || !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q must name a route below /", configured)
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f || p[i] == '\\' {
			return "", fmt.Errorf("path %q contains an invalid character", configured)
		}
	}

	// /s belongs to stenella; the remaining roots are claimed by atp's outer
	// middleware before hostDispatch gets a chance to see the request.
	if pathAtOrBelow(p, "/s") {
		return "", fmt.Errorf("path %q is reserved", p)
	}
	for _, reserved := range []string{"/api", "/c", "/gw"} {
		if pathAtOrBelow(p, reserved) {
			return "", fmt.Errorf("path %q is reserved", p)
		}
	}
	// atp owns these roots, but only at a complete path-segment boundary:
	// /login-page and /healthcheck remain valid static-site paths.
	for _, reserved := range []string{"/clients", "/login", "/logout", "/health"} {
		if pathAtOrBelow(p, reserved) {
			return "", fmt.Errorf("path %q is reserved", p)
		}
	}
	return p, nil
}

func pathAtOrBelow(requestPath, base string) bool {
	return requestPath == base || strings.HasPrefix(requestPath, base+"/")
}

// hostOnly canonicalizes an HTTP Host authority for lookup. It returns an
// empty string for malformed authorities so a bad Host header can never fall
// through to a configured site mapping.
func hostOnly(host string) string {
	if host == "" || host != strings.TrimSpace(host) {
		return ""
	}
	u, err := url.Parse("//" + host)
	if err != nil || u.Scheme != "" || u.User != nil || u.Host == "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return ""
	}
	// The URL parser accepts an empty port (for example, "example.com:").
	// It has no useful Host-header meaning, so require a digit when supplied.
	if strings.HasSuffix(u.Host, ":") {
		return ""
	}
	if port := u.Port(); port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return ""
		}
	}
	name := strings.ToLower(u.Hostname())
	if strings.Contains(name, ":") { // IPv6 authorities must use brackets.
		ip := net.ParseIP(name)
		if ip == nil {
			return ""
		}
		return ip.String()
	}
	name = strings.TrimSuffix(name, ".")
	if !validHostName(name) {
		return ""
	}
	return name
}

func validHostName(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || !isASCIIAlphaNumeric(label[0]) || !isASCIIAlphaNumeric(label[len(label)-1]) {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !isASCIIAlphaNumeric(c) && c != '-' {
				return false
			}
		}
	}
	return true
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
	r2.RequestURI = u.RequestURI()
	return r2
}

// atpOwnedPath reports whether ATP owns a path on a mapped host. It matches
// complete path segments, so /healthcheck and /login-page remain available to
// a static site while /health and /login retain their ATP meanings.
func atpOwnedPath(p string) bool {
	for _, base := range []string{"/api", "/clients", "/gw", "/c"} {
		if pathAtOrBelow(p, base) {
			return true
		}
	}
	return p == "/login" || p == "/logout" || p == "/health"
}

func safeMappedSitePath(p string) bool {
	if p == "" || p[0] != '/' || strings.ContainsAny(p, "\\\r\n\t") {
		return false
	}
	if p == "/" {
		return true
	}
	trimmed := strings.TrimPrefix(p, "/")
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" {
		return false
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// hostDispatch implements virtual hosting: for hosts listed in
// Config.SiteHosts the mapped client's hosted site (their song silo, served
// via ATP's public /c/{client}/... handler so usage metering and scoping
// still apply) is served at "/", PlatformPath redirects to the stenella
// platform, and everything under /s/ remains the platform's own namespace.
//
// Dispatch is deliberately outside ATP middleware. Otherwise a request such
// as /c/other-client/... on a trusted mapped host would be handled by ATP
// before the host mapping and could serve another tenant's JavaScript under
// the mapped origin.
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

		// The platform keeps its own namespace on mapped hosts. Check this
		// before the configurable platform path so /s always remains owned by
		// stenella, even if a Server was assembled without New's validation.
		if pathAtOrBelow(p, "/s") {
			next.ServeHTTP(w, r)
			return
		}

		// The documented "platform" endpoint: azzurro.tech/platform should
		// land on the stenella platform automatically. Match complete path
		// segments so /platformish remains part of the hosted site.
		if pathAtOrBelow(p, plat) {
			target := "/s/portal?client=" + url.QueryEscape(client)
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}

		// ATP's reserved paths remain available on a mapped host, but /c is
		// special: only the mapped client's own silo may be addressed there.
		if atpOwnedPath(p) {
			if p == "/c" || strings.HasPrefix(p, "/c/") {
				segments := strings.Split(strings.TrimPrefix(p, "/c/"), "/")
				if len(segments) == 0 || segments[0] == "" || segments[0] != client {
					http.NotFound(w, r)
					return
				}
				// Song may emit an internal /{client}/... redirect even when
				// the request arrived through the explicit /c/ compatibility
				// route. Rewrite that response to the mapped public namespace.
				wrapped := &mappedResponseWriter{
					ResponseWriter: w,
					client:         client,
					originalHost:   r.Host,
					originalQuery:  r.URL.RawQuery,
				}
				next.ServeHTTP(wrapped, r)
				return
			}
			// Preserve ATP's normal auth/usage handling for the other reserved
			// ATP paths (the mapped client's /c route was handled above).
			next.ServeHTTP(w, r)
			return
		}

		// Everything else on a mapped host is the client's hosted site. Route
		// through the middleware (rather than Handler directly) so usage
		// metering and Song's public silo checks remain active. Reject raw
		// traversal/separator variants before ATP's mux gets a chance to
		// canonicalize a path that could otherwise escape the mapped client.
		if !safeMappedSitePath(p) {
			http.NotFound(w, r)
			return
		}
		newPath := "/c/" + client
		if p == "/" {
			newPath += "/"
		} else {
			newPath += p
		}
		wrapped := &mappedResponseWriter{
			ResponseWriter: w,
			client:         client,
			originalHost:   r.Host,
			originalQuery:  r.URL.RawQuery,
		}
		next.ServeHTTP(wrapped, cloneReq(r, newPath))
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
