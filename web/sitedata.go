package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"azzurrotech/stenella/atpclient"
)

// This file implements stenella's public site-data endpoint, the bridge that
// lets a client's hosted website (e.g. azzurro.tech) render its own content
// without any session credentials.
//
// A client site is pure static files in its song silo; the dynamic content it
// shows (products, posts, …) lives in the client's pod tables. /s/data/{client}/
// {table} exposes those tables as public, read-only JSON so the Emperor42
// front-end (vidi mainly) can render them.
//
// Only tables inside the client's own namespace are readable, and the platform
// management tables (shares/links) stay private — a client controls what it
// publishes by what it stores in a given table.

// clientExists reports whether id is a registered, enabled atp client. Public
// site data is intentionally unavailable for disabled tenants; an operator can
// still use the authenticated admin surface to inspect or restore them.
func (s *Server) clientExists(id string) bool {
	rec, err := s.atp.GetClient(id)
	if err != nil {
		return false
	}
	if disabled, ok := rec["disabled"].(bool); ok && disabled {
		return false
	}
	if client, ok := rec["client"].(map[string]any); ok {
		if disabled, ok := client["disabled"].(bool); ok && disabled {
			return false
		}
	}
	return true
}

// validPublicTablePath keeps the public bridge inside the client's namespace.
// Pod table names are slash-separated for a few internal callers, so safe
// nested names remain supported; empty, dot, traversal, control, and separator
// variants are rejected before the path reaches atp.
func validPublicTablePath(table string) bool {
	if table == "" || len(table) > 256 || strings.HasPrefix(table, "/") || strings.HasSuffix(table, "/") {
		return false
	}
	for _, segment := range strings.Split(table, "/") {
		if segment == "" || segment == "." || segment == ".." || len(segment) > 128 {
			return false
		}
		for i := 0; i < len(segment); i++ {
			c := segment[i]
			// A decoded path segment must not acquire URL syntax or a second
			// decoding round. Pod table names are plain path components; the
			// public endpoint never needs query/fragment/percent delimiters.
			if c < 0x20 || c == 0x7f || c == '\\' || c == '?' || c == '#' || c == '%' {
				return false
			}
		}
	}
	return true
}

// siteDataPathGuard rejects malformed public-data paths before http.ServeMux
// gets a chance to clean them. Go's mux canonicalizes `..`, duplicate slashes,
// and dot segments with a 307 response; returning the validation error first
// keeps the endpoint fail-closed instead of accidentally changing its meaning.
func (s *Server) siteDataPathGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if invalidSiteDataPath(r) {
			s.writeErr(w, http.StatusBadRequest, "invalid site-data path")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func invalidSiteDataPath(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	// EscapedPath retains encoded slashes/dots long enough for us to decode and
	// validate the actual segments. Falling back to Path is safe for requests
	// constructed by tests or internal callers that have no RawPath.
	p := r.URL.EscapedPath()
	if p == "" {
		p = r.URL.Path
	}
	decoded, err := url.PathUnescape(p)
	if err != nil {
		return strings.HasPrefix(p, "/s/data/") || strings.HasPrefix(r.URL.Path, "/s/data/")
	}
	const prefix = "/s/data/"
	if !strings.HasPrefix(decoded, prefix) {
		return false
	}
	rest := strings.TrimPrefix(decoded, prefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || !validSiteClientID(parts[0]) {
		return true
	}
	return !validPublicTablePath(parts[1])
}

// handleSiteData serves a client's pod table as public JSON. Response shape:
//
//	{"client": "azzurrotech", "table": "products", "count": 5, "records": […]}
//
// Supported query parameters: limit (default 200, max 1000), q (text search),
// orderby and dir=desc.
func (s *Server) handleSiteData(w http.ResponseWriter, r *http.Request) {
	client := r.PathValue("client")
	table := r.PathValue("table")
	if client == "" || table == "" {
		s.writeErr(w, http.StatusBadRequest, "client and table are required")
		return
	}
	if !validPublicTablePath(table) {
		s.writeErr(w, http.StatusBadRequest, "invalid table path")
		return
	}
	if !s.clientExists(client) {
		s.writeErr(w, http.StatusNotFound, "client not found")
		return
	}

	// The management tables of the platform stay private even on the public
	// endpoint (share records contain tokens).
	if table == "shares" || table == "links" || strings.HasPrefix(table, "shares/") || strings.HasPrefix(table, "links/") {
		s.writeErr(w, http.StatusForbidden, "this table is not public")
		return
	}

	limit := intParam(r, "limit", 200)
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 256 {
		s.writeErr(w, http.StatusBadRequest, "q is too long")
		return
	}
	orderBy := strings.TrimSpace(r.URL.Query().Get("orderby"))
	if len(orderBy) > 128 {
		s.writeErr(w, http.StatusBadRequest, "orderby is too long")
		return
	}
	for i := 0; i < len(orderBy); i++ {
		c := orderBy[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			s.writeErr(w, http.StatusBadRequest, "invalid orderby")
			return
		}
	}
	desc := strings.EqualFold(r.URL.Query().Get("dir"), "desc")

	recs, count, err := s.atp.QueryTable(client+"/"+table, atpclient.TableQuery{
		Limit:   limit,
		OrderBy: orderBy,
		Desc:    desc,
		Q:       q,
	})
	if err != nil {
		// Unknown table or empty namespace → 404; anything else stays honest.
		s.writeErr(w, http.StatusNotFound, "table not found: "+err.Error())
		return
	}

	payload := map[string]any{
		"client":  client,
		"table":   table,
		"count":   count,
		"limit":   limit,
		"records": recs,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "could not encode site data")
		return
	}
	sum := sha256.Sum256(encoded)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(encoded, '\n'))
}
