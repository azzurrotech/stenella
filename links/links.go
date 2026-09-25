// Package links implements stenella's link tracking between feed elements.
//
// Every link has two halves:
//
//  1. The durable database half — a row in the client's pod namespace
//     (<client>/links), written through atp's pod pass-through so pod remains
//     the single source of truth for the platform's database.
//  2. The filesystem half — a junction directory under
//     <root>/stenella/links/<client>/<id>/ that holds real symbolic links
//     pointing at the linked pod record XML files. The symlinks are the
//     "tracking": they physically connect the two database rows on disk, and
//     going stale is a readable signal that a linked record moved or died.
//
// Sharing reuses the same mechanism: a share is a pod record (<client>/shares)
// plus a junction directory with a symlink to the shared record/table.
package links

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"azzurrotech/stenella/atpclient"
)

// Link relation kinds for the second end of a link.
const (
	ToItem = "item" // a feed element (a pod record in <client>/items)
	ToURL  = "url"  // any external URL (stored as a text file + symlink)
)

// Link is one relationship between two feed elements (or an element and an
// external URL). ID, Created and the to/from fields are the pod record's.
type Link struct {
	ID       string `json:"id"`
	FromID   string `json:"from_id"`
	ToKind   string `json:"to_kind"`
	ToID     string `json:"to_id,omitempty"`
	ToURL    string `json:"to_url,omitempty"`
	Relation string `json:"relation"`
	Label    string `json:"label,omitempty"`
	Created  string `json:"created"`

	// Enrichment (resolved titles/links), filled in by List.
	FromTitle string `json:"from_title,omitempty"`
	FromLink  string `json:"from_link,omitempty"`
	ToTitle   string `json:"to_title,omitempty"`
	ToLink    string `json:"to_link,omitempty"`
}

// Store manages link junctions and their pod records for every client.
type Store struct {
	root string
	atp  *atpclient.Client
}

// New creates a Store rooted at the shared data root (the same root atp uses,
// so pod record paths are <root>/pod/<client>/<table>/<id>.xml).
func New(root string, atp *atpclient.Client) *Store {
	return &Store{root: root, atp: atp}
}

// ItemsTable is the pod table holding a client's feed items.
func ItemsTable(client string) string { return client + "/items" }

// LinksTable is the pod table holding a client's links.
func LinksTable(client string) string { return client + "/links" }

// SharesTable is the pod table holding a client's shares.
func SharesTable(client string) string { return client + "/shares" }

// Create validates both ends through atp, stores the link record in the
// client's pod namespace, and materializes the symlink junction.
func (s *Store) Create(client, fromID, toKind, toID, toURL, relation, label string) (*Link, error) {
	if !validIdentifier(client) || !validIdentifier(fromID) {
		return nil, errors.New("client and from item are required")
	}
	// Validate the from item (a feed element must exist).
	if _, err := s.atp.GetRecord(ItemsTable(client), fromID); err != nil {
		return nil, fmt.Errorf("from item %q: %w", fromID, err)
	}
	kind := strings.ToLower(strings.TrimSpace(toKind))
	toURL = strings.TrimSpace(toURL)
	if kind != ToItem && kind != ToURL {
		return nil, errors.New("to_kind must be \"item\" or \"url\"")
	}
	switch kind {
	case ToItem:
		if !validIdentifier(toID) {
			return nil, errors.New("to item is required")
		}
		if _, err := s.atp.GetRecord(ItemsTable(client), toID); err != nil {
			return nil, fmt.Errorf("to item %q: %w", toID, err)
		}
	case ToURL:
		u, err := url.Parse(strings.TrimSpace(toURL))
		if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(toURL, "\r\n\t") {
			return nil, errors.New("to_url must be an http(s) url")
		}
	}
	if relation == "" {
		relation = "related"
	}
	created := time.Now().UTC().Format(time.RFC3339)

	rec, err := s.atp.UpsertRecord(LinksTable(client), map[string]string{
		"from_id":  fromID,
		"to_kind":  kind,
		"to_id":    toID,
		"to_url":   toURL,
		"relation": relation,
		"label":    label,
		"created":  created,
	})
	if err != nil {
		return nil, fmt.Errorf("pod link record: %w", err)
	}
	id := rec["id"]
	if err := s.materializeJunction(client, id, manifest{
		ID: id, Client: client, FromID: fromID, ToKind: kind, ToID: toID, ToURL: toURL,
		Relation: relation, Label: label, Created: created,
	}); err != nil {
		// The record is the source of truth; a junction failure is not fatal
		// but is surfaced so the operator can inspect the data root.
		return nil, fmt.Errorf("symlink junction: %w", err)
	}
	return &Link{
		ID: id, FromID: fromID, ToKind: kind, ToID: toID, ToURL: toURL,
		Relation: relation, Label: label, Created: created,
	}, nil
}

// manifest is the junction's sidecar describing what it links.
type manifest struct {
	ID       string `json:"id"`
	Client   string `json:"client"`
	FromID   string `json:"from_id"`
	ToKind   string `json:"to_kind"`
	ToID     string `json:"to_id,omitempty"`
	ToURL    string `json:"to_url,omitempty"`
	Relation string `json:"relation"`
	Label    string `json:"label,omitempty"`
	Created  string `json:"created"`
}

func (s *Store) junctionDir(client, id string) string {
	return filepath.Join(s.root, "stenella", "links", safe(client), safe(id))
}

// materializeJunction creates <root>/stenella/links/<client>/<id>/ with
// manifest.json and real symlinks to the pod record files.
func (s *Store) materializeJunction(client, id string, m manifest) error {
	dir := s.junctionDir(client, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644); err != nil {
		return err
	}
	// from.xml → <root>/pod/<client>/items/<from>.xml
	if err := recordSymlink(dir, "from.xml", s.root, client, "items", m.FromID); err != nil {
		return err
	}
	// to.xml → the item record, or a to.url probe file for external URLs.
	if m.ToKind == ToURL {
		if err := os.WriteFile(filepath.Join(dir, "to.url"), []byte(m.ToURL+"\n"), 0o644); err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, filepath.Join(dir, "to.url"))
		if err != nil {
			return err
		}
		if err := os.Symlink(rel, filepath.Join(dir, "to.xml")); err != nil {
			return err
		}
	} else {
		if err := recordSymlink(dir, "to.xml", s.root, client, "items", m.ToID); err != nil {
			return err
		}
	}
	return nil
}

// recordSymlink creates <dir>/<name> → <root>/pod/<client>/<table>/<id>.xml.
func recordSymlink(dir, name, root, client, table, id string) error {
	target := filepath.Join(root, "pod", safe(client), safe(table), safe(id)+".xml")
	full := filepath.Clean(target)
	prefix := filepath.Clean(filepath.Join(root, "pod")) + string(os.PathSeparator)
	if !strings.HasPrefix(full, prefix) {
		return errors.New("symlink target escapes the pod root")
	}
	rel, err := filepath.Rel(dir, full)
	if err != nil {
		return err
	}
	return os.Symlink(rel, filepath.Join(dir, name))
}

// List returns a client's links, newest first, enriched with the titles of
// both ends.
func (s *Store) List(client string, limit, offset int) ([]Link, error) {
	if !validIdentifier(client) {
		return nil, errors.New("invalid client")
	}
	tq := atpclient.TableQuery{OrderBy: "created", Desc: true}
	if limit > 0 {
		tq.Limit = limit
	}
	if offset > 0 {
		tq.Offset = offset
	}
	recs, _, err := s.atp.QueryTable(LinksTable(client), tq)
	if err != nil {
		if errors.Is(err, &atpclient.HTTPError{}) {
			return nil, err
		}
		return nil, err
	}
	out := make([]Link, 0, len(recs))
	for _, r := range recs {
		ln := Link{
			ID: r["id"], FromID: r["from_id"], ToKind: r["to_kind"], ToID: r["to_id"],
			ToURL: r["to_url"], Relation: r["relation"], Label: r["label"],
			Created: r["created"],
		}
		enrich := func(client, table, id string) (title, link string) {
			if id == "" {
				return "", ""
			}
			if rec, err := s.atp.GetRecord(table, id); err == nil {
				return rec["title"], rec["link"]
			}
			return "", ""
		}
		ln.FromTitle, ln.FromLink = enrich(client, ItemsTable(client), ln.FromID)
		if ln.ToKind == ToURL {
			ln.ToTitle = ln.ToURL
			ln.ToLink = ln.ToURL
		} else {
			ln.ToTitle, ln.ToLink = enrich(client, ItemsTable(client), ln.ToID)
		}
		out = append(out, ln)
	}
	return out, nil
}

// Get reads a single link record.
func (s *Store) Get(client, id string) (*Link, error) {
	if !validIdentifier(client) || !validIdentifier(id) {
		return nil, errors.New("invalid link reference")
	}
	rec, err := s.atp.GetRecord(LinksTable(client), id)
	if err != nil {
		return nil, err
	}
	return &Link{
		ID: rec["id"], FromID: rec["from_id"], ToKind: rec["to_kind"], ToID: rec["to_id"],
		ToURL: rec["to_url"], Relation: rec["relation"], Label: rec["label"], Created: rec["created"],
	}, nil
}

// Delete removes the pod record and the junction directory (symlinks first).
func (s *Store) Delete(client, id string) error {
	if !validIdentifier(client) || !validIdentifier(id) {
		return errors.New("invalid link reference")
	}
	if err := s.atp.DeleteRecord(LinksTable(client), id); err != nil {
		return err
	}
	dir := s.junctionDir(client, id)
	for _, name := range []string{"to.xml", "from.xml"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	_ = os.Remove(filepath.Join(dir, "manifest.json"))
	_ = os.Remove(dir)
	return nil
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 256 || value == "." || value == ".." {
		return false
	}
	if strings.ContainsAny(value, "/\\?#%") || strings.ContainsAny(value, "\r\n\t") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func safe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

func randToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
