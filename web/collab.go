package web

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"azzurrotech/stenella/atpclient"
	"azzurrotech/stenella/crypt"
)

// errInvalidInput marks a validation failure in the collaboration store, so the
// handler layer can pick a status code without matching on message text.
var errInvalidInput = errors.New("invalid input")

// collabStore owns the collaboration layer over pod: comments, pins and the
// item-level link graph. Each is a pod table inside the client's namespace, so
// records are addressable, indexable and exportable through the same driver as
// everything else — no side database.
//
// Comment bodies follow encrypt-before-submit: the browser encrypts with vici
// under the client content passphrase and POSTs only ciphertext. This store
// never decrypts a comment it did not have to, and never searches one.
type collabStore struct {
	atp    *atpclient.Client
	keys   *crypt.KeyStore
	exists ItemFinder
}

// ItemFinder reports whether an item id is live for a client.
//
// Pod is the mirror, not the source of truth: the feed engine holds an item the
// moment a fetch stores it, while the pod row only appears when the mirror loop
// runs. A pin or comment posted in that window must not be refused, so the
// engine is consulted first and pod second. Both agree once the mirror catches
// up, and both say no once the item has been swept from each.
type ItemFinder func(client, id string) bool

func newCollab(atp *atpclient.Client, keys *crypt.KeyStore, exists ItemFinder) *collabStore {
	return &collabStore{atp: atp, keys: keys, exists: exists}
}

// itemLive reports whether an item can be referred to. It returns the underlying
// error when the answer is a definitive no (so the handler can answer 404) and
// nil when the item is live.
func (c *collabStore) itemLive(client, id string) error {
	if c.exists != nil && c.exists(client, id) {
		return nil
	}
	if _, err := c.atp.GetRecord(client+"/items", id); err != nil {
		return err
	}
	return nil
}

// Pod table names for the collaboration layer.
const (
	commentsTable = "comments"
	pinsTable     = "pins"
)

// ---- comments ----------------------------------------------------------------

// Comment is one client-authored note on a feed item. BodyEnc is the vici
// payload; the browser holds the passphrase, so the server cannot read it.
type Comment struct {
	ID          string `json:"id"`
	ItemRef     string `json:"item_ref"`
	AuthorToken string `json:"author_token,omitempty"`
	BodyEnc     string `json:"body_enc"`
	// Bytes is the plaintext length, supplied by the browser. It lets the UI
	// size a placeholder without the server ever decrypting the body.
	Bytes    int    `json:"bytes,omitempty"`
	Created  string `json:"created"`
	ACLClass string `json:"acl_class,omitempty"`
}

// AddComment stores an encrypted comment against an item. The item must exist,
// which is checked through pod: a comment pointing at a pruned item is not
// something the retention sweep can honour, so it is refused at write time.
func (c *collabStore) AddComment(client, itemRef, authorToken, bodyEnc string, bytes int, acl string) (*Comment, error) {
	if itemRef == "" {
		return nil, fmt.Errorf("%w: item_ref is required", errInvalidInput)
	}
	if strings.TrimSpace(bodyEnc) == "" {
		return nil, fmt.Errorf("%w: body_enc is required, comments are encrypted before submit", errInvalidInput)
	}
	if err := c.itemLive(client, itemRef); err != nil {
		return nil, err
	}
	rec, err := c.atp.UpsertRecord(client+"/"+commentsTable, map[string]string{
		"item_ref":     itemRef,
		"author_token": authorToken,
		"body_enc":     bodyEnc,
		"bytes":        strconv.Itoa(bytes),
		"acl_class":    acl,
		"created":      time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	// The create response echoes the stored row but not Bytes: the client
	// just sent that number, so there is nothing to read back here. Listing
	// goes through commentFromRow, the one reader-side row mapping.
	return &Comment{
		ID:          rec["id"],
		ItemRef:     rec["item_ref"],
		AuthorToken: rec["author_token"],
		BodyEnc:     rec["body_enc"],
		Created:     rec["created"],
		ACLClass:    rec["acl_class"],
	}, nil
}

// commentFromRow maps one pod comment row onto the API Comment. It is the
// single row→Comment mapping: ListComments and the usage dashboard's
// listAllComments both go through it, so a column rename or a new field lands
// in every reader at once.
func commentFromRow(r map[string]string) Comment {
	n, _ := strconv.Atoi(r["bytes"])
	return Comment{
		ID: r["id"], ItemRef: r["item_ref"], AuthorToken: r["author_token"],
		BodyEnc: r["body_enc"], Bytes: n, Created: r["created"], ACLClass: r["acl_class"],
	}
}

// ListComments returns an item's comments, oldest first (a thread reads
// top to bottom).
func (c *collabStore) ListComments(client, itemRef string, limit int) ([]Comment, error) {
	if itemRef == "" {
		return nil, fmt.Errorf("%w: item_ref is required", errInvalidInput)
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	recs, _, err := c.atp.QueryTable(client+"/"+commentsTable, atpclient.TableQuery{
		Limit: limit, OrderBy: "created",
	})
	if err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(recs))
	for _, r := range recs {
		if r["item_ref"] != itemRef {
			continue
		}
		out = append(out, commentFromRow(r))
	}
	return out, nil
}

// DeleteComment removes a comment record.
func (c *collabStore) DeleteComment(client, id string) error {
	if id == "" {
		return fmt.Errorf("%w: comment id is required", errInvalidInput)
	}
	return c.atp.DeleteRecord(client+"/"+commentsTable, id)
}

// itemRefSet collects the non-empty item_ref values from rows of a table keyed
// by item_ref. It is the shape both exemption sets (comments, pins) need.
func itemRefSet(recs []map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, r := range recs {
		if ref := strings.TrimSpace(r["item_ref"]); ref != "" {
			out[ref] = true
		}
	}
	return out
}

// CommentRefs returns the set of item ids that have at least one comment. The
// retention sweep uses it as an exemption set.
func (c *collabStore) CommentRefs(client string) (map[string]bool, error) {
	recs, _, err := c.atp.QueryTable(client+"/"+commentsTable, atpclient.TableQuery{Limit: 10000})
	if err != nil {
		return nil, err
	}
	return itemRefSet(recs), nil
}

// ---- pins --------------------------------------------------------------------

// Pin is a client's explicit keep-mark on an item. A pinned item is exempt from
// the retention sweep for as long as the pin exists.
type Pin struct {
	ID        string `json:"id"`
	ItemRef   string `json:"item_ref"`
	CreatedBy string `json:"created_by,omitempty"`
	Created   string `json:"created"`
}

// PinItem creates a pin. Pinning the same item twice is not an error: the
// existing pin is returned, so a double-click cannot produce two records that
// both have to be found and removed later.
func (c *collabStore) PinItem(client, itemRef, createdBy string) (*Pin, error) {
	if itemRef == "" {
		return nil, fmt.Errorf("%w: item_ref is required", errInvalidInput)
	}
	if err := c.itemLive(client, itemRef); err != nil {
		return nil, err
	}
	if existing, err := c.findPin(client, itemRef); err == nil && existing != nil {
		return existing, nil
	}
	rec, err := c.atp.UpsertRecord(client+"/"+pinsTable, map[string]string{
		"item_ref":   itemRef,
		"created_by": createdBy,
		"created":    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	return &Pin{ID: rec["id"], ItemRef: rec["item_ref"], CreatedBy: rec["created_by"], Created: rec["created"]}, nil
}

// Unpin removes the pin for an item.
func (c *collabStore) Unpin(client, itemRef string) error {
	p, err := c.findPin(client, itemRef)
	if err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	return c.atp.DeleteRecord(client+"/"+pinsTable, p.ID)
}

// PinnedItems returns the set of pinned item ids.
func (c *collabStore) PinnedItems(client string) (map[string]bool, error) {
	recs, _, err := c.atp.QueryTable(client+"/"+pinsTable, atpclient.TableQuery{Limit: 10000})
	if err != nil {
		return nil, err
	}
	return itemRefSet(recs), nil
}

// ListPins returns the client's pins, newest first.
func (c *collabStore) ListPins(client string, limit int) ([]Pin, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	recs, _, err := c.atp.QueryTable(client+"/"+pinsTable, atpclient.TableQuery{
		Limit: limit, OrderBy: "created", Desc: true,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Pin, 0, len(recs))
	for _, r := range recs {
		out = append(out, Pin{ID: r["id"], ItemRef: r["item_ref"], CreatedBy: r["created_by"], Created: r["created"]})
	}
	return out, nil
}

func (c *collabStore) findPin(client, itemRef string) (*Pin, error) {
	recs, _, err := c.atp.QueryTable(client+"/"+pinsTable, atpclient.TableQuery{Limit: 10000})
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if strings.TrimSpace(r["item_ref"]) == itemRef {
			return &Pin{ID: r["id"], ItemRef: itemRef, CreatedBy: r["created_by"], Created: r["created"]}, nil
		}
	}
	return nil, nil
}

// EnsureSchema creates the collaboration tables for a client if absent. It is
// called during provisioning so a brand-new client can comment and pin
// immediately instead of on first use.
//
// The item-link graph is deliberately absent. It is the links store's table
// (<client>/links), which already holds item→item edges plus junctions on disk
// and powers the shares. A second table for the same edges would be two
// sources of truth for one graph, and the sweep would have to pick one to walk.
func (c *collabStore) EnsureSchema(client string) error {
	for _, table := range []string{commentsTable, pinsTable} {
		cols := map[string][]string{
			commentsTable: {"item_ref", "author_token", "body_enc", "bytes", "acl_class", "created"},
			pinsTable:     {"item_ref", "created_by", "created"},
		}[table]
		// An existing table reports an error from pod; that is the normal path
		// for every client after the first call.
		_ = c.atp.CreateTable(client+"/"+table, cols)
	}
	return nil
}
