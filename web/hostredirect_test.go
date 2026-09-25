package web

import "testing"

func TestPublicSongRedirect(t *testing.T) {
	cases := []struct {
		name, location, want string
		ok                   bool
	}{
		{"directory", "/azzurrotech/docs/", "/docs/", true},
		{"root", "/azzurrotech", "/", true},
		{"query", "/azzurrotech/docs/?from=site", "/docs/?from=site", true},
		{"preserve-query", "/azzurrotech/docs/", "/docs/?from=site", true},
		{"absolute", "http://internal/azzurrotech/docs/", "http://azzurro.tech/docs/", true},
		{"unrelated", "/other/docs/", "", false},
		{"prefix-collision", "/azzurrotech-other/docs/", "", false},
		{"userinfo", "http://user@internal/azzurrotech/docs/", "", false},
		{"scheme", "javascript:/azzurrotech/docs/", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := ""
			if tc.name == "preserve-query" {
				query = "from=site"
			}
			got, ok := publicSongRedirect(tc.location, "azzurrotech", "azzurro.tech", query)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("publicSongRedirect(%q) = %q,%v; want %q,%v", tc.location, got, ok, tc.want, tc.ok)
			}
		})
	}
}
