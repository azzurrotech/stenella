package web

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"azzurrotech/stenella/atpclient"
)

// Self-service signup provisions a client end to end: the ATP client record, the
// SONG site silo, the POD namespace tables, the portal secret and the content
// key. It is the only unauthenticated mutating endpoint on the platform, which
// is why it is deliberately narrow — it accepts no configuration, creates no
// upstream integrations, and hands the one-time portal secret back exactly once.
//
// Everything it provisions is per client and idempotent-ish, so a failure part
// way through leaves an inert record rather than a half-configured silo: the
// client can be deleted and signed up again.

const (
	// signupWindow and signupBurst bound how many signups a single source may
	// attempt in a minute. This is not a substitute for a real WAF — it exists so
	// a script cannot enumerate client ids by watching for the difference
	// between "taken" and "created". The counter itself is the shared
	// rateLimiter in ratelimit.go, also used by portal login.
	signupWindow = time.Minute
	signupBurst  = 5
	// signupSecretBytes is the portal secret length. 24 bytes of hex entropy is
	// far beyond guessable, and the secret is the only thing standing between a
	// public signup and someone else's portal.
	signupSecretBytes = 24
)

// clientIDSuggestion matches a reasonable self-chosen handle: lowercase
// alphanumerics, dots, dashes and underscores. It mirrors what the silo router
// will accept, so a client cannot sign up with an id its site could never serve.
var clientIDSuggestion = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}[a-z0-9]$`)

// handleSignupPage renders the signup form.
func (s *Server) handleSignupPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.atp.Settings(); err != nil {
		s.writeErr(w, http.StatusServiceUnavailable, "platform is not configured yet")
		return
	}
	s.renderPage(w, r, "signup", map[string]any{
		"Title":  "Create a workspace",
		"Client": r.URL.Query().Get("client"),
	})
}

// handleSignup provisions a client. It accepts a JSON body or a plain form post,
// so the page works with JavaScript disabled and the API is usable from curl.
func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	if !s.signup.allow(s.remoteIP(r), time.Now()) {
		w.Header().Set("Retry-After", "60")
		s.writeErr(w, http.StatusTooManyRequests, "too many signup attempts; try again in a minute")
		return
	}

	var req struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Notes string `json:"notes"`
	}
	if err := decodeStream(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.ID = strings.ToLower(strings.TrimSpace(req.ID))
	req.Name = strings.TrimSpace(req.Name)
	req.Notes = strings.TrimSpace(req.Notes)

	if !clientIDSuggestion.MatchString(req.ID) {
		s.writeErr(w, http.StatusBadRequest,
			"client id must be 3-64 characters of a-z, 0-9, dot, dash or underscore, starting and ending with a letter or digit")
		return
	}
	if !validSiteClientID(req.ID) {
		s.writeErr(w, http.StatusBadRequest, "that client id is reserved")
		return
	}
	if req.Name == "" {
		req.Name = req.ID
	}
	if len(req.Name) > 120 || len(req.Notes) > 500 {
		s.writeErr(w, http.StatusBadRequest, "name or notes is too long")
		return
	}
	if s.clientExists(req.ID) {
		// Deliberately the same message as every other failure: whether an id is
		// taken is not something an unauthenticated caller gets to learn.
		s.signupFailed(w, r, "that could not be created; try a different client id")
		return
	}

	// randomHex is the one random-token helper (see its contract in auth.go);
	// both call sites use 24 bytes of entropy.
	secret := randomHex(signupSecretBytes)

	// Provision in order, cleaning up on failure. The portal secret is stored
	// first: without it the client exists but nobody can reach it, which is
	// worse than not existing at all.
	if err := s.atp.CreateClient(req.ID, req.Name, req.Notes); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			s.signupFailed(w, r, "that could not be created; try a different client id")
			return
		}
		// A 4xx from the registry is the caller's fault (a reserved id, a bad
		// shape); a 5xx is not. Reporting them the same way would either blame
		// the caller for our outage or hide our outage behind "try again".
		var he *atpclient.HTTPError
		if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
			s.signupFailed(w, r, "that client id cannot be used; try a different one")
			return
		}
		s.writeErr(w, http.StatusBadGateway, "client registry refused the request: "+err.Error())
		return
	}
	fail := func(cause error) {
		_ = s.atp.DeleteClient(req.ID)
		s.writeErr(w, http.StatusBadGateway, "provisioning did not complete: "+cause.Error())
	}
	if err := s.atp.SetSecret(req.ID, "portal", secret, "stenella portal secret (shown once at signup)"); err != nil {
		fail(err)
		return
	}
	if err := s.keys.EnsureClient(req.ID); err != nil {
		fail(err)
		return
	}
	if err := s.provisionTables(req.ID); err != nil {
		fail(err)
		return
	}

	// The content passphrase goes into the same one-time reply as the portal
	// secret. Handing it back here means a client can start reading and writing
	// ciphertext immediately without a second round trip.
	passphrase, err := s.keys.Passphrase(req.ID)
	if err != nil {
		_ = s.atp.DeleteClient(req.ID)
		s.writeErr(w, http.StatusInternalServerError, "content key unavailable: "+err.Error())
		return
	}

	// Log the client straight in: asking someone to paste a secret they just
	// received into another form is a pointless step.
	token, sess := s.sess.put("client "+req.ID, req.ID)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: requestIsHTTPS(r), MaxAge: int(sessionTTL.Seconds()),
	})

	if wantsHTML(r) {
		http.Redirect(w, r, "/s/portal?client="+req.ID+"&welcome=1", http.StatusSeeOther)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"client":     req.ID,
		"name":       req.Name,
		"secret":     secret,
		"passphrase": passphrase,
		"portal_url": "/s/portal?client=" + req.ID,
		"feed_url":   "/s/feed/" + req.ID,
		"expires":    sess.expires.Format(time.RFC3339),
	})
}

// provisionTables creates the pod tables a new client can immediately use. Feed
// items are created on first fetch; the rest exist from the start so the portal
// does not 404 on an empty workspace.
func (s *Server) provisionTables(client string) error {
	// The items table's columns are the envelope: metadata plus ciphertext, never
	// a plaintext body. See plan §4.2. No pin_status column: pins are their own
	// table (see collab.EnsureSchema), not item metadata.
	if err := s.atp.CreateTable(client+"/items", []string{
		"source_id", "source_name", "title_enc", "summary_enc", "content_enc",
		"link", "guid", "author", "categories", "acl_class", "content_bytes",
		"published", "updated", "fetched",
	}); err != nil && !strings.Contains(err.Error(), "exists") {
		return err
	}
	// Comments, pins, links and shares. Comments and pins are this package's
	// tables; links and shares belong to the links store, which is where the
	// single item graph lives.
	if err := s.collab.EnsureSchema(client); err != nil {
		return err
	}
	return s.links.EnsureSchema(client)
}

// signupFailed answers a rejected signup without revealing whether the reason was
// the id being taken.
func (s *Server) signupFailed(w http.ResponseWriter, r *http.Request, msg string) {
	if wantsHTML(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		s.renderPage(w, r, "signup", map[string]any{
			"Title": "Create a workspace", "Error": msg,
		})
		return
	}
	s.writeErr(w, http.StatusBadRequest, msg)
}

// remoteIP (the rate-limit key) lives in ratelimit.go: it uses r.RemoteAddr
// unless the operator explicitly opted into trusting X-Forwarded-For.

// wantsHTML reports whether the caller is a browser form rather than an API
// client. A form post has no Accept: application/json and is not JSON.
func wantsHTML(r *http.Request) bool {
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		return false
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html")
}
