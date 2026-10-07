package web

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// This file holds the shared per-key rate limiter and its keying rule, so
// stenella's unauthenticated endpoints (signup and portal login) cannot drift
// apart: one implementation, one way to identify the caller, one place to
// audit.

// rateLimiter is a fixed-window counter keyed by an arbitrary string (in
// practice the caller's IP). It hangs off the Server rather than off a package
// variable: a process-global counter would be shared between every test server
// in the same binary, and would quietly make the endpoints untestable.
//
// A fixed window is deliberately simple — it exists so a script cannot grind
// through client ids or portal secrets, not as a substitute for a WAF.
type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	window time.Duration
	burst  int
}

func newRateLimiter(window time.Duration, burst int) *rateLimiter {
	return &rateLimiter{hits: map[string][]time.Time{}, window: window, burst: burst}
}

// allow reports whether one more attempt within the window is permitted, and
// records the attempt when it is.
func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.hits[key][:0]
	for _, at := range l.hits[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= l.burst {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

// newSignupLimiter returns the signup budget (signupWindow/signupBurst, see
// signup.go) as its own instance, so signup and login never share attempts.
func newSignupLimiter() *rateLimiter {
	return newRateLimiter(signupWindow, signupBurst)
}

// newLoginLimiter returns the portal login budget (loginWindow/loginBurst, see
// auth.go) as its own instance.
func newLoginLimiter() *rateLimiter {
	return newRateLimiter(loginWindow, loginBurst)
}

// remoteIP returns the key used to rate-limit the request.
//
// It uses r.RemoteAddr by default: X-Forwarded-For is written by the client on
// a direct deployment, so trusting it would let an attacker rotate the header
// to get a fresh budget for every request. Operators behind a proxy they
// control (Caddy) opt in with --trust-proxy / $STENELLA_TRUST_PROXY=1, which
// is when the header actually carries the real client address.
func (s *Server) remoteIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
	}
	if i := strings.LastIndex(r.RemoteAddr, ":"); i > 0 {
		return strings.Trim(r.RemoteAddr[:i], "[]")
	}
	return r.RemoteAddr
}
