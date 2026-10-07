package web

import (
	"net/http"
	"strings"
	"testing"
)

// The portal login is unauthenticated, so it must be throttled: without a
// limit, one process-external script could grind through client ids and
// secrets undisturbed. Same budget as signup, 429 with Retry-After beyond it.
func TestClientLoginRateLimited(t *testing.T) {
	s, _ := newTestWeb(t)

	body := `{"client":"acmecorp","secret":"guess"}`
	for i := 1; i <= loginBurst; i++ {
		rec := doJSON(t, s, http.MethodPost, "/s/api/client/login", body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401 (%s)", i, rec.Code, rec.Body.String())
		}
	}
	rec := doJSON(t, s, http.MethodPost, "/s/api/client/login", body)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d: got %d, want 429 (%s)", loginBurst+1, rec.Code, rec.Body.String())
	}
	if ra := rec.Header().Get("Retry-After"); ra != "60" {
		t.Errorf("Retry-After = %q, want 60", ra)
	}
}

// The login must not distinguish "no such or disabled client" from "wrong
// secret": a different message or status would turn the endpoint into a client
// enumeration oracle. Server-side logs keep the specific reason.
func TestClientLoginFailureIsUniform(t *testing.T) {
	s, _ := newTestWeb(t)

	// Provision a real client with a known portal secret via self-service signup.
	rec := doJSON(t, s, http.MethodPost, "/s/api/signup", `{"id":"enumcorp","name":"Enum"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup: got %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Secret string `json:"secret"`
	}
	decodeInto(t, rec, &out)
	if out.Secret == "" {
		t.Fatal("signup returned no portal secret")
	}

	wrongSecret := doJSON(t, s, http.MethodPost, "/s/api/client/login",
		`{"client":"enumcorp","secret":"not-the-secret"}`)
	unknownClient := doJSON(t, s, http.MethodPost, "/s/api/client/login",
		`{"client":"ghostcorp","secret":"not-the-secret"}`)

	if wrongSecret.Code != http.StatusUnauthorized || unknownClient.Code != http.StatusUnauthorized {
		t.Fatalf("statuses: wrong secret %d, unknown client %d; want 401 for both",
			wrongSecret.Code, unknownClient.Code)
	}
	if wrongSecret.Body.String() != unknownClient.Body.String() {
		t.Errorf("failure bodies differ — enumeration oracle:\n  wrong secret: %s\n  unknown client: %s",
			wrongSecret.Body.String(), unknownClient.Body.String())
	}

	// And a correct secret still signs in (the uniformity must not break auth).
	ok := doJSON(t, s, http.MethodPost, "/s/api/client/login",
		`{"client":"enumcorp","secret":"`+out.Secret+`"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("valid login: got %d, want 200 (%s)", ok.Code, ok.Body.String())
	}
}

// Every stenella JSON endpoint caps the request body at maxBodyBytes; a body
// over the cap must come back as the endpoint's normal 400, not be buffered.
func TestRequestBodySizeCap(t *testing.T) {
	s, _ := newTestWeb(t)

	huge := `{"client":"acmecorp","secret":"` + strings.Repeat("a", maxBodyBytes+1<<20) + `"}`
	rec := doJSON(t, s, http.MethodPost, "/s/api/client/login", huge)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized JSON login: got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid body") {
		t.Errorf("oversized JSON login body = %s, want the standard invalid-body error", rec.Body.String())
	}

	// The form path (signup) is capped too: ParseForm would otherwise read an
	// unbounded urlencoded body into memory.
	form := doForm(t, s, http.MethodPost, "/s/api/signup",
		"id=bigcorp&name="+strings.Repeat("a", maxBodyBytes))
	if form.Code != http.StatusBadRequest {
		t.Fatalf("oversized form signup: got %d, want 400 (%s)", form.Code, form.Body.String())
	}
}
