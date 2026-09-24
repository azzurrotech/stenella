package links

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"azzurrotech/stenella/atpclient"
)

// Share kinds.
const (
	ShareItem  = "item"  // one feed element
	ShareLink  = "link"  // one link between elements
	ShareTable = "table" // a whole pod table (rendered by vidi)
)

// Share is a public, token-protected view onto a client resource. The pod
// record lives in <client>/shares; the junction holds a symlink to the shared
// record/table in the filesystem.
type Share struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Title   string `json:"title"`
	Token   string `json:"token,omitempty"`
	Created string `json:"created"`
	Expires string `json:"expires,omitempty"`
	URL     string `json:"url,omitempty"`
	JSONURL string `json:"json_url,omitempty"`
}

// Shares manages share records + junctions and keeps a tiny public index
// (share id → client) so token URLs resolve without knowing the client.
type Shares struct {
	root string
	atp  *atpclient.Client

	mu  sync.RWMutex
	idx map[string]string
}

// NewShares loads the public index (if any) and returns a Shares manager.
func NewShares(root string, atp *atpclient.Client) *Shares {
	s := &Shares{root: root, atp: atp, idx: map[string]string{}}
	if data, err := os.ReadFile(s.indexPath()); err == nil {
		_ = json.Unmarshal(data, &s.idx)
	}
	return s
}

func (s *Shares) indexPath() string {
	return filepath.Join(s.root, "stenella", "shares", "index.json")
}

func (s *Shares) junctionDir(client, id string) string {
	return filepath.Join(s.root, "stenella", "shares", safe(client), safe(id))
}

func (s *Shares) persistIndex() {
	data, err := json.MarshalIndent(s.idx, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.indexPath(), data, 0o644)
}

// Create makes a share. days <= 0 means no expiry.
func (s *Shares) Create(client, kind, target, title string, days int) (*Share, error) {
	if client == "" {
		return nil, errors.New("client is required")
	}
	kind = strings.ToLower(kind)
	switch kind {
	case ShareItem, ShareLink, ShareTable:
	default:
		return nil, errors.New("share kind must be item, link or table")
	}
	// Validate the target through atp so broken shares are rejected early.
	switch kind {
	case ShareItem:
		if _, err := s.atp.GetRecord(ItemsTable(client), target); err != nil {
			return nil, fmt.Errorf("item %q: %w", target, err)
		}
	case ShareLink:
		if _, err := s.atp.GetRecord(LinksTable(client), target); err != nil {
			return nil, fmt.Errorf("link %q: %w", target, err)
		}
	case ShareTable:
		if _, _, err := s.atp.QueryTable(client+"/"+target, atpclient.TableQuery{Limit: 1}); err != nil {
			return nil, fmt.Errorf("table %q: %w", target, err)
		}
	}
	if title == "" {
		title = target
	}
	now := time.Now().UTC()
	created := now.Format(time.RFC3339)
	expires := ""
	if days > 0 {
		expires = now.Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
	}
	token := randToken()

	rec, err := s.atp.UpsertRecord(SharesTable(client), map[string]string{
		"kind": kind, "target": target, "title": title,
		"token": token, "created": created, "expires": expires,
	})
	if err != nil {
		return nil, fmt.Errorf("pod share record: %w", err)
	}
	id := rec["id"]

	// Junction with a symlink to the shared pod record (or table directory).
	dir := s.junctionDir(client, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	mb, _ := json.MarshalIndent(map[string]string{
		"id": id, "client": client, "kind": kind, "target": target, "title": title,
		"created": created, "expires": expires,
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644)

	var symTarget string
	switch kind {
	case ShareItem:
		symTarget = filepath.Join(s.root, "pod", safe(client), "items", safe(target)+".xml")
	case ShareLink:
		symTarget = filepath.Join(s.root, "pod", safe(client), "links", safe(target)+".xml")
	case ShareTable:
		symTarget = filepath.Join(s.root, "pod", safe(client), safe(target))
	}
	if rel, err := filepath.Rel(dir, symTarget); err == nil {
		_ = os.Symlink(rel, filepath.Join(dir, "target"))
	}

	s.mu.Lock()
	s.idx[id] = client
	s.persistIndex()
	s.mu.Unlock()

	return &Share{
		ID: id, Kind: kind, Target: target, Title: title, Token: token,
		Created: created, Expires: expires,
	}, nil
}

// ClientFor resolves the owner client of a share id (public index lookup).
func (s *Shares) ClientFor(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.idx[id]
	return c, ok
}

// Get reads a share record.
func (s *Shares) Get(client, id string) (*Share, error) {
	rec, err := s.atp.GetRecord(SharesTable(client), id)
	if err != nil {
		return nil, err
	}
	return &Share{
		ID: rec["id"], Kind: rec["kind"], Target: rec["target"], Title: rec["title"],
		Token: rec["token"], Created: rec["created"], Expires: rec["expires"],
	}, nil
}

// List returns a client's shares, newest first.
func (s *Shares) List(client string, limit, offset int) ([]Share, error) {
	tq := atpclient.TableQuery{OrderBy: "created", Desc: true}
	if limit > 0 {
		tq.Limit = limit
	}
	if offset > 0 {
		tq.Offset = offset
	}
	recs, _, err := s.atp.QueryTable(SharesTable(client), tq)
	if err != nil {
		return nil, err
	}
	out := make([]Share, 0, len(recs))
	for _, r := range recs {
		out = append(out, Share{
			ID: r["id"], Kind: r["kind"], Target: r["target"], Title: r["title"],
			Created: r["created"], Expires: r["expires"],
		})
	}
	return out, nil
}

// Valid reports whether a presented token matches and the share hasn't lapsed.
func (sh *Share) Valid(presented string) bool {
	if sh.Token == "" || presented == "" || sh.Token != presented {
		return false
	}
	if sh.Expires != "" {
		if exp, err := time.Parse(time.RFC3339, sh.Expires); err == nil && time.Now().After(exp) {
			return false
		}
	}
	return true
}

// Delete removes the record, junction and index entry.
func (s *Shares) Delete(client, id string) error {
	if err := s.atp.DeleteRecord(SharesTable(client), id); err != nil {
		return err
	}
	dir := s.junctionDir(client, id)
	_ = os.Remove(filepath.Join(dir, "target"))
	_ = os.Remove(filepath.Join(dir, "manifest.json"))
	_ = os.Remove(dir)
	s.mu.Lock()
	delete(s.idx, id)
	s.persistIndex()
	s.mu.Unlock()
	return nil
}
