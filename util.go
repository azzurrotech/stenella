// util.go — small shared helpers: hashing, slugs, geo parsing, text cleaning.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func nowUTC() time.Time { return time.Now().UTC() }

// shortHash is the de-duplication identity: a one-way digest of its parts,
// truncated to 32 hex characters. It is deliberately irreversible — a link or a
// guid can be matched but never recovered, which is what lets stenella record
// identity without recording the link itself.
func shortHash(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slug lowercases and reduces a string to [a-z0-9-], collapsing runs. Used for
// entity identities and URLs, never for display.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlug.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// titleKey is the last-resort duplicate key when a source gives no guid or link.
func titleKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// cleanInline reduces arbitrary text to safe, link-free, bounded prose. It is
// the sanitiser from the collection layer, given a name that says why it is
// being called at a write site: everything stenella stores is already clean.
func cleanInline(s string, max int) string { return sanitizeText(s, max) }

// canonicalLink normalises a source link to the form used for de-duplication
// identity: scheme and host lowercased, query and fragment dropped, trailing
// slash removed. The result is immediately hashed and never stored, so a story
// that appears under a tracking URL and under its clean URL collapses to one
// identity.
func canonicalLink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = ""
	u.Fragment = ""
	u.RawFragment = ""
	p := strings.TrimRight(u.String(), "/")
	return p
}

// parseGeo reads a "lat,lon" pair produced by formatGeo.
func parseGeo(s string) (lat, lon float64, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	a, b, found := strings.Cut(s, ",")
	if !found {
		return 0, 0, false
	}
	la, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	lo, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	if math.Abs(la) > 90 || math.Abs(lo) > 180 {
		return 0, 0, false
	}
	return la, lo, true
}

// formatGeo renders a coordinate pair for storage, rounding to about a metre.
func formatGeo(lat, lon float64) string {
	return strconv.FormatFloat(lat, 'f', 5, 64) + "," + strconv.FormatFloat(lon, 'f', 5, 64)
}

// intersect reports whether two string slices share an element, comparing
// case-insensitively. It is the primitive behind every rule condition.
func intersect(a, b []string) ([]string, bool) {
	var shared []string
	seen := map[string]bool{}
	for _, x := range a {
		seen[strings.ToLower(strings.TrimSpace(x))] = true
	}
	for _, y := range b {
		y = strings.ToLower(strings.TrimSpace(y))
		if y != "" && seen[y] {
			shared = append(shared, y)
		}
	}
	return shared, len(shared) > 0
}

// subsetAll reports whether every element of want is present in have
// (case-insensitively). Empty want is not a match.
func subsetAll(want, have []string) bool {
	if len(want) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, h := range have {
		seen[strings.ToLower(strings.TrimSpace(h))] = true
	}
	for _, w := range want {
		if !seen[strings.ToLower(strings.TrimSpace(w))] {
			return false
		}
	}
	return true
}

// uniqueStrings removes case-insensitive duplicates, preserving order.
func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		k := strings.ToLower(s)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// containsFold reports whether a slice contains a value, case-insensitively.
func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}
