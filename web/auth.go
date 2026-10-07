package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"azzurrotech/stenella/atpclient"
)

// sessionCookie is stenella's own portal session cookie (separate from atp's
// admin cookie so clients are not given the admin's cookie).
const sessionCookie = "stenella_session"

// sessionTTL is how long a client portal login lasts without refresh.
const sessionTTL = 24 * time.Hour

const (
	// loginWindow and loginBurst bound portal login attempts per source per
	// minute. Login is unauthenticated and answers "right/wrong", so without a
	// limit it is an online password-guessing oracle. The budget matches
	// signup's; the counter is the shared rateLimiter (ratelimit.go).
	loginWindow = time.Minute
	loginBurst  = 5
)

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
)

// session is one client portal login.
type session struct {
	Client  string
	label   string
	expires time.Time
}

// sessionStore keeps portal sessions in memory (signed cookie-style tokens are
// atp/shepherd territory; stenella's sessions are short-lived and stateless to
// the extent that they are just random capability handles).
type sessionStore struct {
	mu sync.Mutex
	m  map[string]*session
}

func newSessionStore() *sessionStore {
	return &sessionStore{m: map[string]*session{}}
}

func (ss *sessionStore) put(label, client string) (token string, sess *session) {
	token = randomHex(24)
	sess = &session{Client: client, label: label, expires: time.Now().Add(sessionTTL)}
	ss.mu.Lock()
	ss.m[token] = sess
	ss.mu.Unlock()
	return token, sess
}

func (ss *sessionStore) get(token string) *session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.m[token]
	if !ok {
		return nil
	}
	if time.Now().After(s.expires) {
		delete(ss.m, token)
		return nil
	}
	return s
}

func (ss *sessionStore) del(token string) {
	ss.mu.Lock()
	delete(ss.m, token)
	ss.mu.Unlock()
}

// randomHex is the package's single random-token generator: n bytes of
// cryptographic randomness in, 2n lowercase hex characters out (the session
// token at 24 bytes, the signup portal secret at signupSecretBytes = 24).
//
// It panics rather than returning an error. A fallback secret generated from
// anything weaker than the OS RNG would be worse than a dead request, and on
// the toolchains this module builds with crypto/rand.Read does not report
// errors at all — it aborts internally — so an error branch here would be dead
// code that only invites callers to ignore it.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ---- gates ------------------------------------------------------------------

// clientGate wraps a handler that must be the indexed client's own session or
// an authenticated atp admin acting on their behalf.
func (s *Server) clientGate(fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := r.URL.Query().Get("client")
		if client == "" {
			s.writeErr(w, http.StatusBadRequest, "missing client")
			return
		}
		if !validSiteClientID(client) {
			s.writeErr(w, http.StatusBadRequest, "invalid client")
			return
		}
		if err := s.requireClient(r, client); err != nil {
			if errors.Is(err, errUnauthorized) {
				s.writeErr(w, http.StatusUnauthorized, "sign in to access this portal")
				return
			}
			s.writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		fn(w, r)
	})
}

// adminGate wraps a handler requiring a valid atp admin session.
func (s *Server) adminGate(fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			s.writeErr(w, http.StatusUnauthorized, "admin session required")
			return
		}
		fn(w, r)
	})
}

// requireClient passes when the request's portal session belongs to client, or
// when the request carries valid atp admin credentials (impersonation).
func (s *Server) requireClient(r *http.Request, client string) error {
	if s.sessionClient(r) == client {
		return nil
	}
	if s.isAdmin(r) {
		return nil
	}
	// The two failures mean different things to a caller, so they are different
	// errors: no usable session at all is 401, while a valid session belonging to
	// some other client is 403. Collapsing them into one would tell a signed-in
	// user to re-authenticate when re-authenticating cannot help.
	if s.sessionClient(r) != "" {
		return errForbidden
	}
	return errUnauthorized
}

// sessionClient returns the client id of the portal session, or "".
func (s *Server) sessionClient(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	sess := s.sess.get(c.Value)
	if sess == nil {
		return ""
	}
	return sess.Client
}

// isAdmin reports whether the request authenticates to atp's admin API
// (bearer header or the atp_session cookie atp set for the admin UI).
func (s *Server) isAdmin(r *http.Request) bool {
	if tok := r.Header.Get("X-ATP-Token"); tok != "" {
		if s.atp.AdminValid(tok, nil) {
			return true
		}
	}
	cookies := r.Cookies()
	if len(cookies) == 0 {
		return false
	}
	return s.atp.AdminValid("", cookies)
}

func requestIsHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

// ---- login handlers -----------------------------------------------------------

// handleClientLogin authenticates a client with the value of their "portal"
// (or "password") secret in atp's vault. Super admins rotate that secret to
// change a client's login.
func (s *Server) handleClientLogin(w http.ResponseWriter, r *http.Request) {
	// Throttle before doing any work: an unauthenticated verifier with no rate
	// limit is a guessing oracle. Keyed by the real client IP unless the
	// operator opted into --trust-proxy (see ratelimit.go).
	if !s.login.allow(s.remoteIP(r), time.Now()) {
		w.Header().Set("Retry-After", "60")
		s.writeErr(w, http.StatusTooManyRequests, "too many login attempts; try again in a minute")
		return
	}
	var req struct {
		Client string `json:"client"`
		Secret string `json:"secret"`
	}
	if !s.decodeBody(w, r, &req) {
		return
	}
	req.Client = strings.TrimSpace(req.Client)
	if req.Client == "" || req.Secret == "" {
		s.writeErr(w, http.StatusBadRequest, "client and secret are required")
		return
	}
	if !validSiteClientID(req.Client) {
		s.writeErr(w, http.StatusBadRequest, "invalid client")
		return
	}
	exists := s.clientExists(req.Client)
	got := ""
	if exists {
		for _, name := range []string{"portal", "password"} {
			if v, err := s.atp.GetSecret(req.Client, name); err == nil {
				got = v
				break
			}
		}
	}
	// Exactly one constant-time comparison on every path: an unknown client
	// compares against "" the same way a wrong secret compares against the
	// stored value, so both cases cost the same.
	match := secretEqual(got, req.Secret)
	if !exists {
		match = false
	}
	if !match {
		if !exists {
			// The specific reason stays in the server log only. The response is
			// identical to a wrong secret on purpose: a different message would
			// turn this unauthenticated endpoint into a client-enumeration
			// oracle.
			log.Printf("web: portal login for unknown or disabled client %q", req.Client)
		} else {
			log.Printf("web: portal login failed for client %q: bad secret", req.Client)
		}
		s.writeErr(w, http.StatusUnauthorized, "invalid client or secret")
		return
	}
	token, sess := s.sess.put("client "+req.Client, req.Client)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: requestIsHTTPS(r), MaxAge: int(sessionTTL.Seconds()),
	})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "client": req.Client, "label": sess.label, "expires": sess.expires.Format(time.RFC3339),
	})
}

// handleClientLogout clears the portal session.
func (s *Server) handleClientLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sess.del(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: requestIsHTTPS(r), MaxAge: -1})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWhoami reports the current portal identity + role.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	sessClient := s.sessionClient(r)
	admin := s.isAdmin(r)
	role := "none"
	label := ""
	switch {
	case sessClient == client:
		role = "client"
		label = sessClient
	case admin:
		role = "admin"
		label = "(super admin)"
	}
	if role == "none" {
		s.writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	rec, err := s.atp.GetClient(client)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, "client not found")
		return
	}
	var clientRec atpclient.ClientRecord
	if b, err := json.Marshal(rec["client"]); err == nil {
		_ = json.Unmarshal(b, &clientRec)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"role": role, "label": label, "client": clientRec,
		"silo_url": rec["silo_url"], "admin": admin,
	})
}
