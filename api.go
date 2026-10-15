// api.go — stenella's HTTP handlers.
//
// The routes are registered by App.routes. Every handler is an http.HandlerFunc
// that speaks JSON (the API) or HTML (the console). They make no assumptions
// about transport or middleware beyond what atp already provides; the shared
// HTTP helpers live in http.go.
package main

import (
	"net/http"
	"strings"

	"azzurrotech/atp"
)

func (a *App) listSources(w http.ResponseWriter, r *http.Request) {
	srcs, err := a.store.Sources()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": srcs})
}

func (a *App) createSource(w http.ResponseWriter, r *http.Request) {
	var s Source
	if !decode(w, r, &s) {
		return
	}
	if strings.TrimSpace(s.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if s.Kind == "" {
		s.Kind = sourceKindFeed
	}
	if !isSourceKind(s.Kind) {
		writeErr(w, http.StatusBadRequest,
			"kind must be one of feed (RSS 2.0 / RSS 1.0 RDF / Atom / JSON Feed), page or dataset")
		return
	}
	// a feed or a page is fetched from somewhere; a dataset is material the
	// analyst imports, so it has no URL to fetch and never should be marked
	// failed for lacking one.
	if s.Kind != sourceKindDataset && strings.TrimSpace(s.URL) == "" {
		writeErr(w, http.StatusBadRequest, "url is required for a feed or page source; a dataset is imported")
		return
	}
	if s.Collection == "" {
		s.Collection = "all"
	}
	if s.Reliability == "" {
		s.Reliability = "C"
	}
	if s.ACL == "" {
		s.ACL = "public"
	}
	s.Enabled = true
	if err := a.store.PutSource(&s); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.flushAggrs()
	writeJSON(w, http.StatusCreated, s)
}

func (a *App) getSource(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "source id required")
		return
	}
	src, err := a.store.Source(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "source not found")
		return
	}
	items, _ := a.store.ItemsIn(src.Collection, src.Name, 50)
	writeJSON(w, http.StatusOK, map[string]any{"source": src, "items": items})
}

func (a *App) sourceAction(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "source id required")
		return
	}
	var in struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch in.Action {
	case "fetch", "refresh":
		rep, err := a.refreshOne(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, rep)
	case "enable", "disable":
		src, err := a.store.Source(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "source not found")
			return
		}
		src.Enabled = in.Action == "enable"
		if err := a.store.PutSource(src); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.flushAggrs()
		writeJSON(w, http.StatusOK, src)
	case "delete":
		if err := a.store.DeleteSource(id); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.flushAggrs()
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	default:
		writeErr(w, http.StatusBadRequest, "action must be fetch, enable, disable or delete")
	}
}

func (a *App) deleteSource(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	if err := a.store.DeleteSource(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.flushAggrs()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func (a *App) listCollections(w http.ResponseWriter, r *http.Request) {
	colls, err := a.store.Collections()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": colls})
}

func (a *App) collectionAction(w http.ResponseWriter, r *http.Request) {
	id := atp.PathParam(r, "id")
	var in struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch in.Action {
	case "refresh":
		rep, err := a.refresh(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, rep)
	default:
		writeErr(w, http.StatusBadRequest, "unknown action")
	}
}

func (a *App) flushAggrs() {
	a.aggMu.Lock()
	defer a.aggMu.Unlock()
	a.aggs = map[string]*aggregator{}
}
