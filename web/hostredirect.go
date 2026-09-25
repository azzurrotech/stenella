package web

import (
	"net/http"
	"net/url"
	"strings"
)

// mappedResponseWriter keeps Song's internal /{client}/... redirect paths
// from leaking through a mapped public host. Song is mounted behind ATP's
// /c/{client}/ handler, but its directory/index redirects are generated from
// the rewritten internal path. Without this adapter, /docs would redirect to
// /azzurrotech/docs/ and the next request would be scoped a second time.
type mappedResponseWriter struct {
	http.ResponseWriter
	client        string
	originalHost  string
	originalQuery string
	rewritten     bool
}

// Unwrap lets net/http's ResponseController reach optional interfaces on the
// underlying writer (Flush, Hijack, and Push) without exposing them as public
// methods on every wrapper version.
func (w *mappedResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *mappedResponseWriter) rewriteLocation() {
	if w.rewritten {
		return
	}
	w.rewritten = true
	location := w.Header().Get("Location")
	if location == "" {
		return
	}
	rewritten, ok := publicSongRedirect(location, w.client, w.originalHost, w.originalQuery)
	if ok {
		w.Header().Set("Location", rewritten)
	}
}

func (w *mappedResponseWriter) WriteHeader(status int) {
	w.rewriteLocation()
	w.ResponseWriter.WriteHeader(status)
}

func (w *mappedResponseWriter) Write(body []byte) (int, error) {
	w.rewriteLocation()
	return w.ResponseWriter.Write(body)
}

func publicSongRedirect(location, client, originalHost, originalQuery string) (string, bool) {
	u, err := url.Parse(location)
	if err != nil || u.User != nil || (u.IsAbs() && u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	prefix := "/" + client
	if u.Path != prefix && !strings.HasPrefix(u.Path, prefix+"/") {
		return "", false
	}
	if u.IsAbs() {
		u.Host = originalHost
	}
	u.Path = strings.TrimPrefix(u.Path, prefix)
	if u.Path == "" {
		u.Path = "/"
	}
	u.RawPath = ""
	if u.RawQuery == "" && originalQuery != "" {
		u.RawQuery = originalQuery
	}
	if u.IsAbs() {
		return u.String(), true
	}
	// RequestURI retains a relative path and query without manufacturing a
	// scheme/host that Song did not know about.
	return u.RequestURI(), true
}
