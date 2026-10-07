package web

import (
	"net/http/httptest"
	"testing"
	"time"
)

// Rate-limit keys default to r.RemoteAddr. X-Forwarded-For is client-controlled
// unless a proxy the operator controls sets it, so it is only honoured after
// the explicit --trust-proxy opt-in — otherwise an attacker could rotate the
// header and get a fresh signup/login budget on every request.
func TestRemoteIPTrustProxyOptIn(t *testing.T) {
	s, _ := newTestWeb(t)

	r := httptest.NewRequest("POST", "/s/api/client/login", nil)
	r.RemoteAddr = "203.0.113.7:5000"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")

	if got := s.remoteIP(r); got != "203.0.113.7" {
		t.Errorf("default remoteIP = %q, want RemoteAddr 203.0.113.7 (X-Forwarded-For must be ignored)", got)
	}

	s.cfg.TrustProxy = true
	if got := s.remoteIP(r); got != "198.51.100.9" {
		t.Errorf("TrustProxy remoteIP = %q, want first X-Forwarded-For hop 198.51.100.9", got)
	}

	// With the opt-in but no header, the socket address is still the fallback.
	r.Header.Del("X-Forwarded-For")
	if got := s.remoteIP(r); got != "203.0.113.7" {
		t.Errorf("TrustProxy without header remoteIP = %q, want 203.0.113.7", got)
	}
}

// The shared limiter enforces its burst inside the window and recovers after
// it — the property signup and login both rely on.
func TestRateLimiterBurstAndWindow(t *testing.T) {
	window := time.Minute
	l := newRateLimiter(window, 3)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !l.allow("ip", now) {
			t.Fatalf("attempt %d within burst refused", i+1)
		}
	}
	if l.allow("ip", now) {
		t.Fatal("4th attempt within the window allowed")
	}
	// A different key has its own budget.
	if !l.allow("other", now) {
		t.Fatal("separate key refused")
	}
	// After the window the key recovers.
	if !l.allow("ip", now.Add(window+time.Second)) {
		t.Fatal("attempt after the window refused")
	}
}
