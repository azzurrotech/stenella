package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
)

// session is one client portal login.
type session struct {
	Client  string
	label   string
	created time.Time
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
	sess = &session{Client: client, label: label, created: time.Now(), expires: time.Now().Add(sessionTTL)}
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
	var req struct {
		Client string `json:"client"`
		Secret string `json:"secret"`
	}
	if err := s.readBody(r, &req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid body")
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
	if !s.clientExists(req.Client) {
		s.writeErr(w, http.StatusUnauthorized, "unknown or disabled client")
		return
	}
	got := ""
	for _, name := range []string{"portal", "password"} {
		if v, err := s.atp.GetSecret(req.Client, name); err == nil {
			got = v
			break
		}
	}
	if got == "" || !secretEqual(got, req.Secret) {
		// Equal work regardless of success so timing does not leak presence.
		secretEqual(got, req.Secret)
		s.writeErr(w, http.StatusUnauthorized, "invalid secret")
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
