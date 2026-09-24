package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"azzurrotech/stenella/atpclient"
	"azzurrotech/stenella/feed"
	"azzurrotech/stenella/links"
)

// clientParam returns the portal client id from the query string.
func clientParam(r *http.Request) string { return r.URL.Query().Get("client") }

func intParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// ---- feeds -------------------------------------------------------------------

func (s *Server) handleListFeeds(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	sources := s.feeds.Sources(client)
	outs := make([]*feed.Source, 0, len(sources))
	for _, src := range sources {
		cp := *src
		outs = append(outs, &cp)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"client": client, "sources": outs})
}

func (s *Server) handleAddFeed(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var req struct {
		URL           string `json:"url"`
		Name          string `json:"name"`
		Kind          string `json:"kind"`
		IntervalMin   int    `json:"interval_min"`
		AuthSecret    string `json:"auth_secret"`
		RetentionDays int    `json:"retention_days"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	src, err := s.feeds.AddSource(client, req.Name, req.URL, req.Kind, req.IntervalMin, req.AuthSecret, req.RetentionDays)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// First fetch is eager so the feed is useful immediately.
	go func() {
		ff, ferr := s.feeds.Fetch(client, src, s.secretResolver())
		if ferr == nil {
			s.mirrorToPod(client, ff)
		}
	}()
	s.writeJSON(w, http.StatusCreated, map[string]any{"source": src})
}

func (s *Server) handleUpdateFeed(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	id := r.PathValue("id")
	var req struct {
		Name        string `json:"name"`
		Enabled     *bool  `json:"enabled"`
		IntervalMin int    `json:"interval_min"`
		AuthSecret  string `json:"auth_secret"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	patch := map[string]any{}
	if req.Name != "" {
		patch["name"] = req.Name
	}
	if req.Enabled != nil {
		patch["enabled"] = *req.Enabled
	}
	if req.IntervalMin > 0 {
		patch["interval_min"] = req.IntervalMin
	}
	patch["auth_secret"] = req.AuthSecret
	if err := s.feeds.UpdateSource(client, id, patch); err != nil {
		s.writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	src := s.feeds.GetSource(client, id)
	if src != nil && src.Enabled {
		go func() {
			if ff, err := s.feeds.Fetch(client, src, s.secretResolver()); err == nil {
				s.mirrorToPod(client, ff)
			}
		}()
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"source": src})
}

func (s *Server) handleDeleteFeed(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	id := r.PathValue("id")
	if err := s.feeds.DeleteSource(client, id); err != nil {
		s.writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func (s *Server) handleFetchFeed(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	id := r.PathValue("id")
	src := s.feeds.GetSource(client, id)
	if src == nil {
		s.writeErr(w, http.StatusNotFound, "source not found")
		return
	}
	ff, err := s.feeds.Fetch(client, src, s.secretResolver())
	if err != nil {
		s.writeErr(w, http.StatusBadGateway, "fetch failed: "+err.Error())
		return
	}
	s.mirrorToPod(client, ff)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"source": src.Name, "items": len(ff.Items), "status": "ok",
	})
}

func (s *Server) handleRefreshAll(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	results := s.feeds.FetchAll(client, s.secretResolver(), true)
	for _, res := range results {
		_ = res
	}
	// Mirror freshly fetched caches into pod (one pass, newest first already).
	if pg := s.feeds.Combined(client, feed.Query{Page: 1, PageSize: 1000}); len(pg.Items) > 0 {
		go func() {
			for _, it := range pg.Items {
				cats := strings.Join(it.Categories, "|")
				_, _ = s.atp.UpsertRecord(links.ItemsTable(client), map[string]string{
					"id": it.ID, "source_id": it.SourceID, "source_name": it.SourceName,
					"title": it.Title, "link": it.Link, "guid": it.GUID, "author": it.Author,
					"summary": it.Summary, "content": it.Content, "categories": cats,
					"published": it.Published.Format(time.RFC3339),
					"updated":   it.Updated.Format(time.RFC3339),
					"fetched":   it.Fetched.Format(time.RFC3339),
				})
			}
		}()
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"client": client, "results": results,
		"categories": s.feeds.Categories(client),
	})
}

func (s *Server) handleImportOPML(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var req struct {
		OPMLText string `json:"opml_text"`
		OPMLURL  string `json:"opml_url"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	var (
		data []byte
		err  error
	)
	if req.OPMLURL != "" {
		data, err = s.fetchURL(req.OPMLURL)
	} else {
		data = []byte(req.OPMLText)
	}
	if err != nil {
		s.writeErr(w, http.StatusBadGateway, "could not fetch OPML: "+err.Error())
		return
	}
	srcs, err := feed.ParseOPML(data)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	added := s.feeds.AddSources(client, srcs)
	s.writeJSON(w, http.StatusCreated, map[string]any{"added": len(added), "sources": added})
}

func (s *Server) handleFeedItems(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	page := intParam(r, "page", 1)
	pageSize := intParam(r, "pageSize", feed.PageSizeDefault())
	if pageSize > 200 {
		pageSize = 200
	}
	q := feed.Query{
		Page:     page,
		PageSize: pageSize,
		Q:        r.URL.Query().Get("q"),
		Source:   r.URL.Query().Get("source"),
		Category: r.URL.Query().Get("category"),
	}
	pg := s.feeds.Combined(client, q)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"client": client, "page": pg.Page, "page_size": pageSize,
		"total": pg.Total, "has_more": pg.HasMore, "items": pg.Items,
		"categories": s.feeds.Categories(client),
	})
}

// ---- links -------------------------------------------------------------------

func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	limit := intParam(r, "limit", 0)
	offset := intParam(r, "offset", 0)
	lns, err := s.links.List(client, limit, offset)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"client": client, "links": lns})
}

func (s *Server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var req struct {
		FromID   string `json:"from_id"`
		ToKind   string `json:"to_kind"`
		ToID     string `json:"to_id"`
		ToURL    string `json:"to_url"`
		Relation string `json:"relation"`
		Label    string `json:"label"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	ln, err := s.links.Create(client, req.FromID, req.ToKind, req.ToID, req.ToURL, req.Relation, req.Label)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"link": ln})
}

func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	id := r.PathValue("id")
	if err := s.links.Delete(client, id); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---- shares ------------------------------------------------------------------

func (s *Server) shareURL(kind, id, token string) (htmlURL, jsonURL string) {
	q := "?t=" + url.QueryEscape(token)
	base := s.baseURL
	return base + "/s/x/" + id + q, base + "/s/api/x/" + id + q
}

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	shares, err := s.shares.List(client, 0, 0)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range shares {
		shares[i].URL, shares[i].JSONURL = s.shareURL(shares[i].Kind, shares[i].ID, shares[i].Token)
		shares[i].Token = ""
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"client": client, "shares": shares})
}

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var req struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
		Title  string `json:"title"`
		Days   int    `json:"days"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	sh, err := s.shares.Create(client, req.Kind, req.Target, req.Title, req.Days)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sh.URL, sh.JSONURL = s.shareURL(sh.Kind, sh.ID, sh.Token)
	tok := sh.Token
	sh.Token = ""
	s.writeJSON(w, http.StatusCreated, map[string]any{"share": sh, "token": tok})
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	id := r.PathValue("id")
	if err := s.shares.Delete(client, id); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---- database (pod through atp) ----------------------------------------------

func (s *Server) handleDBTables(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	tables, err := s.atp.ListTables(client)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"client": client, "tables": tables})
}

func (s *Server) handleDBQuery(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	table := r.URL.Query().Get("table")
	if table == "" || strings.Contains(table, "/") {
		s.writeErr(w, http.StatusBadRequest, "table is required")
		return
	}
	page := intParam(r, "page", 1)
	pageSize := intParam(r, "pageSize", 20)
	if pageSize > 200 {
		pageSize = 200
	}
	tq := atpclient.TableQuery{
		Q:       r.URL.Query().Get("q"),
		OrderBy: r.URL.Query().Get("orderby"),
		Desc:    strings.EqualFold(r.URL.Query().Get("dir"), "desc"),
	}
	if n := intParam(r, "limit", 0); n > 0 {
		tq.Limit = n
	} else {
		tq.Limit = pageSize
	}
	tq.Offset = (page - 1) * pageSize
	if page <= 1 {
		tq.Offset = 0
	}
	recs, count, err := s.atp.QueryTable(client+"/"+table, tq)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"client": client, "table": table, "records": recs, "count": count,
		"page": page, "page_size": pageSize, "has_more": page*pageSize < count,
	})
}

func (s *Server) handleDBInsert(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var req struct {
		Table  string            `json:"table"`
		Fields map[string]string `json:"fields"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Table == "" || strings.Contains(req.Table, "/") {
		s.writeErr(w, http.StatusBadRequest, "table is required")
		return
	}
	rec, err := s.atp.UpsertRecord(client+"/"+req.Table, req.Fields)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"record": rec})
}

func (s *Server) handleDBDelete(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	table, id := r.URL.Query().Get("table"), r.URL.Query().Get("id")
	if table == "" || id == "" {
		s.writeErr(w, http.StatusBadRequest, "table and id are required")
		return
	}
	if err := s.atp.DeleteRecord(client+"/"+table, id); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---- sites (song through atp) --------------------------------------------------

func (s *Server) handleSitesMeta(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	files, err := s.atp.ListSiloFiles(client, "")
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"client": client, "files": len(files), "public_url": "/c/" + client + "/",
	})
}

func (s *Server) handleSitesFiles(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	path := r.URL.Query().Get("path")
	files, err := s.atp.ListSiloFiles(client, path)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"client": client, "path": path, "files": files})
}

func (s *Server) handleSitesCreate(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var op atpclient.SongFileOp
	if err := s.readBody(r, &op); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.atp.CreateSongFile(client, op); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"path": op.Path})
}

func (s *Server) handleSitesUpdate(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var op atpclient.SongFileOp
	if err := s.readBody(r, &op); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.atp.UpdateSongFile(client, op); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"path": op.Path})
}

func (s *Server) handleSitesDelete(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	path := r.URL.Query().Get("path")
	if err := s.atp.DeleteSongFile(client, path); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": path})
}

// ---- secrets & payment (atp vault through atp) ---------------------------------

func (s *Server) handleListClientSecrets(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	secrets, err := s.atp.ListSecrets(client)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"client": client, "secrets": secrets})
}

func (s *Server) handleSetClientSecret(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Note  string `json:"note"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" {
		s.writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.atp.SetSecret(client, req.Name, req.Value, req.Note); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"client": client, "name": req.Name, "note": req.Note, "set": true,
	})
}

func (s *Server) handleDeleteClientSecret(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	name := r.URL.Query().Get("name")
	if err := s.atp.DeleteSecret(client, name); err != nil {
		s.writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// paymentShape is what the "payment" vault secret holds (JSON).
var paymentShape = map[string]string{
	"name": "", "email": "", "billing_email": "", "card_last4": "",
	"currency": "USD", "address": "", "notes": "",
}

func (s *Server) handleGetPayment(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	payment := map[string]string{}
	for k, v := range paymentShape {
		payment[k] = v
	}
	if v, err := s.atp.GetSecret(client, "payment"); err == nil && v != "" {
		var stored map[string]string
		if err := json.Unmarshal([]byte(v), &stored); err == nil {
			for k, val := range stored {
				payment[k] = val
			}
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"client": client, "payment": payment})
}

func (s *Server) handlePutPayment(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var payment map[string]string
	if err := s.readBody(r, &payment); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	stored := map[string]string{}
	for k, v := range paymentShape {
		stored[k] = v
	}
	for k, v := range payment {
		stored[k] = strings.TrimSpace(v)
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.atp.SetSecret(client, "payment", string(data), "billing/payment details"); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"client": client, "payment": stored})
}

// ---- billing & usage -----------------------------------------------------------

func (s *Server) handleClientBilling(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	cost, err := s.atp.Billing(client)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	// Forecasts from atp's own billing numbers (no separate billing engine).
	price := 0.0
	if v, ok := cost["price_per_gb_hour"].(float64); ok {
		price = v
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"client": client, "cost": cost, "price_per_gb_hour": price,
	})
}

func (s *Server) handleClientUsage(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	limit := intParam(r, "limit", 200)
	records, err := s.atp.Usage(client, limit)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	hourly, _ := s.atp.Hourly(client)
	s.writeJSON(w, http.StatusOK, map[string]any{"client": client, "records": records, "hourly": hourly})
}

// ---- security (shepherd through atp) --------------------------------------------

func (s *Server) handleIssueKey(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var req struct {
		Subject  string   `json:"subject"`
		Scopes   []string `json:"scopes"`
		Roles    []string `json:"roles"`
		Audience string   `json:"audience"`
		TTL      string   `json:"ttl"`
		Count    int      `json:"count"`
		Block    string   `json:"block"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	args := map[string]any{
		"subject": req.Subject, "scopes": req.Scopes, "roles": req.Roles,
		"audience": req.Audience, "ttl": req.TTL,
	}
	var out map[string]any
	var err error
	if req.Block != "" || req.Count > 1 {
		args["count"] = req.Count
		args["block"] = req.Block
		out, err = s.atp.IssueBlock(client, args)
	} else {
		out, err = s.atp.IssueKey(client, args)
	}
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleVerifyKey(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	t := r.URL.Query().Get("t")
	out, err := s.atp.VerifyToken(client, t)
	if err != nil {
		s.writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	var req struct {
		Token string `json:"token"`
		Block string `json:"block"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.atp.Revoke(client, map[string]any{"token": req.Token, "block": req.Block}); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

func (s *Server) fetchURL(raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid url")
	}
	return httpFetch(raw)
}
