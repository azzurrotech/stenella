package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestSiteDataRejectsUnsafeTablePaths(t *testing.T) {
	s := newTestWebHosted(t)
	for _, table := range []string{
		"../products",
		"products/../secrets",
		"products//nested",
		"products\\nested",
		".",
		"..",
	} {
		rec := hostedReq(t, s, http.MethodGet, "http://localhost/s/data/azzurrotech/"+table)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("table %q: got %d, want 400 (%s)", table, rec.Code, rec.Body.String())
		}
	}
}

func TestSiteDataGuardRejectsEncodedTraversalBeforeMux(t *testing.T) {
	s := newTestWebHosted(t)
	for _, target := range []string{
		"http://localhost/s/data/azzurrotech/products%2F..%2Fsecrets",
		"http://localhost/s/data/azzurrotech/%2e%2e/products",
		"http://localhost/s/data/azzurrotech/products%5c..%5csecrets",
	} {
		rec := hostedReq(t, s, http.MethodGet, target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("encoded path %q: got %d, want 400", target, rec.Code)
		}
	}
}

func TestDisabledClientPublicSurfacesFailClosed(t *testing.T) {
	s := newTestWebHosted(t)
	if err := s.atp.UpdateClient("azzurrotech", map[string]any{"disabled": true}); err != nil {
		t.Fatalf("disable client: %v", err)
	}
	for _, target := range []string{
		"http://localhost/s/data/azzurrotech/products",
		"http://localhost/s/feed/azzurrotech/combined.xml",
		"http://localhost/s/feed/azzurrotech/items",
		"http://azzurro.tech/",
	} {
		rec := hostedReq(t, s, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("disabled public surface %q: got %d, want 404", target, rec.Code)
		}
	}
}

func TestSanitizeHTMLEscapesUntrustedShareContent(t *testing.T) {
	got := sanitizeHTML(`<img src=x onerror=alert(1)> <script>alert(2)</script>`)
	if strings.Contains(got, "<img") || strings.Contains(got, "<script") || !strings.Contains(got, "&lt;img") {
		t.Fatalf("sanitizeHTML returned active or unescaped markup: %q", got)
	}
}

func TestValidPublicTablePath(t *testing.T) {
	for _, table := range []string{"products", "archive/2026", "a_b-c"} {
		if !validPublicTablePath(table) {
			t.Errorf("validPublicTablePath(%q) = false", table)
		}
	}
	for _, table := range []string{"", "/products", "products/", "a//b", "a/../b", `a\b`} {
		if validPublicTablePath(table) {
			t.Errorf("validPublicTablePath(%q) = true", table)
		}
	}
}
