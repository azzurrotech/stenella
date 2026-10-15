// http.go — the shared HTTP plumbing every handler uses.
//
// Handlers are plain http.HandlerFuncs registered on atp with srv.Handle, so
// this file is deliberately small: JSON in, JSON out, and a couple of parsing
// helpers. Nothing here adds security to a response — atp's access model is
// "no added security for hosted resources", and the shareable-feed guarantee is
// enforced by the sanitiser, not by this layer.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// maxBody caps a posted JSON document.
const maxBody = 1 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// decode reads a JSON body into v, answering 400 itself on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	if r.Body == nil {
		writeErr(w, http.StatusBadRequest, "empty body")
		return false
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// formValue reads a value from either the query string or a posted form, so the
// same handler serves the API and a plain HTML form.
func formValue(r *http.Request, key string) string {
	if v := r.URL.Query().Get(key); v != "" {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(r.FormValue(key))
}

func queryInt(r *http.Request, key string, def int) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// pathID returns the last path segment, which is how the /{id} style routes
// pass their identifier.
func pathID(r *http.Request) string {
	p := strings.Trim(r.URL.Path, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func must(err error) {
	if err != nil {
		log.Fatalf("stenella: %v", err)
	}
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(nowUTC().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// hostPort renders a listen address as a host:port for logs.
func hostPort(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}

// errNotFound lets handlers translate a pod miss into a 404 without importing
// pod's error chain everywhere.
var errNotFound = errors.New("not found")
