// clients.go — the atp and song HTTP clients.
//
// Stenella is an independent service now: it no longer embeds atp or song. The
// console still has to report the platform surfaces an operator needs, so it
// asks the services that own them. Every call here is read-only and degrades to
// an empty report when the service is down; a missing atp must not take the
// OSINT platform with it.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ATPClient talks to a running atp server.
type ATPClient struct {
	base string
	http *http.Client
}

// NewATPClient returns a client for base, e.g. "http://127.0.0.1:8080".
func NewATPClient(base string) *ATPClient {
	return &ATPClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 10 * time.Second}}
}

// Base returns the service URL this client talks to.
func (c *ATPClient) Base() string { return c.base }

// ATPStatus is the subset of /api/platform the console reports.
type ATPStatus struct {
	Service  string           `json:"service"`
	Version  string           `json:"version"`
	Root     string           `json:"root"`
	Addr     string           `json:"addr"`
	Services []map[string]any `json:"services"`
	Warnings []string         `json:"warnings"`
}

// Platform reads atp's identity and service map.
func (c *ATPClient) Platform() (*ATPStatus, error) {
	var out ATPStatus
	if err := c.get("/api/platform", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SecretInfo is the metadata atp's vault exposes. It never includes a value.
type SecretInfo struct {
	Name    string    `json:"name"`
	Note    string    `json:"note,omitempty"`
	Updated time.Time `json:"updated"`
}

// Secrets lists the vault's application names and secret metadata.
func (c *ATPClient) Secrets() ([]string, map[string][]SecretInfo, error) {
	var listing struct {
		Apps []string `json:"apps"`
	}
	if err := c.get("/secrets", &listing); err != nil {
		return nil, nil, err
	}
	out := map[string][]SecretInfo{}
	for _, app := range listing.Apps {
		var one struct {
			Secrets []SecretInfo `json:"secrets"`
		}
		if err := c.get("/secrets/"+app, &one); err != nil {
			continue
		}
		if one.Secrets == nil {
			one.Secrets = []SecretInfo{}
		}
		out[app] = one.Secrets
	}
	if listing.Apps == nil {
		listing.Apps = []string{}
	}
	return listing.Apps, out, nil
}

// Health probes atp's /api/platform (atp has no separate /health).
func (c *ATPClient) Health() error {
	_, err := c.Platform()
	return err
}

// Static lists the names in atp's encrypted static overlay.
func (c *ATPClient) Static() ([]string, error) {
	var out struct {
		Static []string `json:"static"`
	}
	if err := c.get("/api/static", &out); err != nil {
		return nil, err
	}
	if out.Static == nil {
		out.Static = []string{}
	}
	return out.Static, nil
}

func (c *ATPClient) get(path string, out any) error {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		return fmt.Errorf("atp: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("atp: GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// SongClient talks to a running song server.
type SongClient struct {
	base string
	http *http.Client
}

// NewSongClient returns a client for base.
func NewSongClient(base string) *SongClient {
	return &SongClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 10 * time.Second}}
}

// Base returns the service URL this client talks to.
func (c *SongClient) Base() string { return c.base }

// Health probes song's /_song/health endpoint.
func (c *SongClient) Health() (map[string]any, error) {
	var out map[string]any
	if err := c.get("/_song/health", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Silos reads the store root and hosted silos.
func (c *SongClient) Silos() (string, []string, error) {
	var out struct {
		Root  string   `json:"root"`
		Silos []string `json:"silos"`
	}
	if err := c.get("/_song/silos", &out); err != nil {
		return "", nil, err
	}
	if out.Silos == nil {
		out.Silos = []string{}
	}
	return out.Root, out.Silos, nil
}

// Components lists song's registered Web Components.
func (c *SongClient) Components() ([]string, error) {
	var out struct {
		Components []string `json:"components"`
	}
	if err := c.get("/_song/components", &out); err != nil {
		return nil, err
	}
	if out.Components == nil {
		out.Components = []string{}
	}
	return out.Components, nil
}

func (c *SongClient) get(path string, out any) error {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		return fmt.Errorf("song: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("song: GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
