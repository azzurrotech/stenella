package web

import (
	"net/http"
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

// clientExists reports whether id is a registered atp client.
func (s *Server) clientExists(id string) bool {
	clients, err := s.atp.ListClients()
	if err != nil {
		return false
	}
	for _, c := range clients {
		if c.ID == id {
			return true
		}
	}
	return false
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
	q := r.URL.Query().Get("q")
	orderBy := r.URL.Query().Get("orderby")
	desc := r.URL.Query().Get("dir") == "desc"

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

	// Public content changes rarely; a short cache window helps both the site
	// and the platform under load.
	w.Header().Set("Cache-Control", "public, max-age=60")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"client":  client,
		"table":   table,
		"count":   count,
		"limit":   limit,
		"records": recs,
	})
}