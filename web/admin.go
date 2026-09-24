package web

import (
	"net/http"
	"sort"

	"azzurrotech/stenella/billing"
	"azzurrotech/stenella/feed"
)

// handleAdminSummary proxies atp's platform summary plus a per-client feed
// item count, so the super-admin view has everything in one payload.
func (s *Server) handleAdminSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.atp.Summary()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	summary["feed_counts"] = s.feedCounts()
	s.writeJSON(w, http.StatusOK, summary)
}

func (s *Server) feedCounts() map[string]int {
	out := map[string]int{}
	for _, client := range s.feeds.Clients() {
		out[client] = len(s.feeds.Sources(client))
	}
	return out
}

// handleAdminIncome returns the income calculator report.
func (s *Server) handleAdminIncome(w http.ResponseWriter, r *http.Request) {
	rep, err := s.income.Income()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, rep)
}

// handleAdminPutIncome stores the hosting cost model.
func (s *Server) handleAdminPutIncome(w http.ResponseWriter, r *http.Request) {
	var cfg billing.HostingConfig
	if err := s.readBody(r, &cfg); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.income.SetConfig(cfg); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rep, err := s.income.Income()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleAdminClients(w http.ResponseWriter, r *http.Request) {
	clients, err := s.atp.ListClients()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type out struct {
		Client   any   `json:"client"`
		Sources  int   `json:"sources"`
		ItemHits int64 `json:"items"`
	}
	outs := make([]out, 0, len(clients))
	for _, c := range clients {
		o := out{Client: c, Sources: len(s.feeds.Sources(c.ID))}
		pg := s.feeds.Combined(c.ID, feed.Query{Page: 1, PageSize: 0})
		o.ItemHits = int64(pg.Total)
		outs = append(outs, o)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"clients": outs})
}

func (s *Server) handleAdminClientCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Notes string `json:"notes"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.atp.CreateClient(req.ID, req.Name, req.Notes); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"created": req.ID})
}

func (s *Server) handleAdminClientUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name           string  `json:"name"`
		Disabled       *bool   `json:"disabled"`
		Chargeable     *bool   `json:"chargeable"`
		PricePerGBHour float64 `json:"price_per_gb_hour"`
		RetentionHours int     `json:"retention_hours"`
		Notes          string  `json:"notes"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	patch := map[string]any{
		"name": req.Name, "notes": req.Notes,
		"price_per_gb_hour": req.PricePerGBHour, "retention_hours": req.RetentionHours,
	}
	if req.Disabled != nil {
		patch["disabled"] = *req.Disabled
	}
	if req.Chargeable != nil {
		patch["chargeable"] = *req.Chargeable
	}
	if err := s.atp.UpdateClient(id, patch); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"updated": id})
}

func (s *Server) handleAdminClientDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.atp.DeleteClient(id); err != nil {
		s.writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---- secrets (super admin can manage any client's vault) ------------------------

func (s *Server) handleAdminSecretsList(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	secrets, err := s.atp.ListSecrets(client)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"client": client, "secrets": secrets})
}

func (s *Server) handleAdminSecretSet(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Note  string `json:"note"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.atp.SetSecret(client, req.Name, req.Value, req.Note); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"client": client, "name": req.Name, "set": true})
}

func (s *Server) handleAdminSecretDelete(w http.ResponseWriter, r *http.Request) {
	client, name := r.URL.Query().Get("client"), r.URL.Query().Get("name")
	if err := s.atp.DeleteSecret(client, name); err != nil {
		s.writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// handleAdminFeeds gives the super admin a bird's-eye view of every client's
// feed (sources, items, freshness).
func (s *Server) handleAdminFeeds(w http.ResponseWriter, r *http.Request) {
	clients, err := s.atp.ListClients()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type sourceView struct {
		*feed.Source
		Items int `json:"items"`
	}
	type clientView struct {
		Client  string       `json:"client"`
		Name    string       `json:"name"`
		Sources []sourceView `json:"sources"`
		Total   int          `json:"total_items"`
	}
	views := make([]clientView, 0, len(clients))
	for _, c := range clients {
		cv := clientView{Client: c.ID, Name: c.Name, Sources: []sourceView{}}
		for _, src := range s.feeds.Sources(c.ID) {
			cp := *src
			n := s.feeds.Combined(c.ID, feed.Query{Page: 1, PageSize: 0, Source: src.ID}).Total
			cv.Sources = append(cv.Sources, sourceView{Source: &cp, Items: n})
		}
		cv.Total = s.feeds.Combined(c.ID, feed.Query{Page: 1, PageSize: 0}).Total
		views = append(views, cv)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Client < views[j].Client })
	s.writeJSON(w, http.StatusOK, map[string]any{"clients": views})
}

func (s *Server) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.atp.Settings()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleAdminPutConfig(w http.ResponseWriter, r *http.Request) {
	var patch map[string]any
	if err := s.readBody(r, &patch); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.atp.PutSettings(patch); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := s.atp.Settings()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, cfg)
}
