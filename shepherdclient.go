// shepherdclient.go — capability shares over HTTP.
//
// Stenella used to call shepherd's token manager in-process. Now shepherd is a
// service (SHEPHERD_URL), and a share is one admin call to POST /api/keys with
// the target in the token metadata. The semantics are unchanged: the token is a
// revocable pointer, not an access-control decision.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultShareTTL is the lifetime used when the caller does not supply one.
const DefaultShareTTL = 7 * 24 * time.Hour

// ParseTTL reads a duration string, returning def when it is empty or invalid.
func ParseTTL(raw string, def time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if d, err := time.ParseDuration(raw + "h"); err == nil && d > 0 {
		return d
	}
	return def
}

// Share is the JSON-facing result of issuing a capability token.
type Share struct {
	Token    string    `json:"token"`
	Kind     string    `json:"kind"`
	ID       string    `json:"id"`
	JTI      string    `json:"jti,omitempty"`
	Issuer   string    `json:"issuer,omitempty"`
	Subject  string    `json:"subject,omitempty"`
	IssuedAt time.Time `json:"issuedAt"`
	Expires  time.Time `json:"expires"`
}

// ShareOptions controls Issue.
type ShareOptions struct {
	Kind    string
	ID      string
	TTL     time.Duration
	Subject string
	Scopes  []string
}

// ShepherdClient talks to a running shepherd server.
type ShepherdClient struct {
	base  string
	admin string
	http  *http.Client
}

// NewShepherdClient returns a client for base. admin is sent as
// X-Shepherd-Admin; it may be empty for public endpoints.
func NewShepherdClient(base, admin string) *ShepherdClient {
	return &ShepherdClient{
		base:  strings.TrimRight(base, "/"),
		admin: admin,
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// Base returns the service URL this client talks to.
func (c *ShepherdClient) Base() string { return c.base }

// Health probes /health and returns shepherd's identity report.
func (c *ShepherdClient) Health() (map[string]any, error) {
	var out map[string]any
	if err := c.do(http.MethodGet, "/health", nil, &out, false); err != nil {
		return nil, err
	}
	return out, nil
}

// Issuer returns shepherd's token issuer, or "" when it is unreachable.
func (c *ShepherdClient) Issuer() string {
	var out struct {
		Issuer string `json:"issuer"`
	}
	if err := c.do(http.MethodGet, "/health", nil, &out, false); err != nil {
		return ""
	}
	return out.Issuer
}

// Firewall fetches the firewall policy. A missing admin token yields an empty
// report rather than failing the console.
func (c *ShepherdClient) Firewall() (defaultAllow bool, rules []map[string]any, err error) {
	var out struct {
		DefaultAllow bool             `json:"default_allow"`
		Rules        []map[string]any `json:"rules"`
	}
	if err := c.do(http.MethodGet, "/api/firewall/rules", nil, &out, true); err != nil {
		return false, nil, err
	}
	if out.Rules == nil {
		out.Rules = []map[string]any{}
	}
	return out.DefaultAllow, out.Rules, nil
}

// Issue creates a capability token for one target.
func (c *ShepherdClient) Issue(o ShareOptions) (*Share, error) {
	kind := strings.ToLower(strings.TrimSpace(o.Kind))
	if kind == "" {
		kind = "record"
	}
	id := strings.TrimSpace(o.ID)
	if id == "" {
		return nil, fmt.Errorf("share: target id is required")
	}
	ttl := o.TTL
	if ttl <= 0 {
		ttl = DefaultShareTTL
	}
	subject := strings.TrimSpace(o.Subject)
	if subject == "" {
		subject = "share"
	}
	scopes := o.Scopes
	if len(scopes) == 0 {
		scopes = []string{"share"}
	}
	body := map[string]any{
		"subject": subject,
		"scopes":  scopes,
		"ttl":     ttl.String(),
		"meta":    map[string]string{"kind": kind, "target": id},
	}
	var out struct {
		Token  string `json:"token"`
		Claims struct {
			JTI      string `json:"jti"`
			Issuer   string `json:"iss"`
			Subject  string `json:"sub"`
			IssuedAt int64  `json:"iat"`
			Expires  int64  `json:"exp"`
		} `json:"claims"`
	}
	if err := c.do(http.MethodPost, "/api/keys", body, &out, true); err != nil {
		return nil, err
	}
	return &Share{
		Token:    out.Token,
		Kind:     kind,
		ID:       id,
		JTI:      out.Claims.JTI,
		Issuer:   out.Claims.Issuer,
		Subject:  out.Claims.Subject,
		IssuedAt: time.Unix(out.Claims.IssuedAt, 0).UTC(),
		Expires:  time.Unix(out.Claims.Expires, 0).UTC(),
	}, nil
}

func (c *ShepherdClient) do(method, path string, body any, out any, admin bool) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if admin && c.admin != "" {
		req.Header.Set("X-Shepherd-Admin", c.admin)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("shepherd: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("shepherd: %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
