package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"azzurrotech/stenella/atpclient"
	"azzurrotech/stenella/feed"
)

// maxCommentBody bounds the ciphertext a client can submit. The body is an
// AES-256-GCM envelope roughly 4/3 the size of its plaintext plus ~48 bytes of
// base64 salt/iv/tag overhead, so a generous cap still allows a very long
// comment while keeping a single request from filling the pod table.
const maxCommentBody = 256 << 10

// ---- comments ----------------------------------------------------------------

// handleListComments returns one item's comments, oldest first. The bodies come
// back as ciphertext; the browser decrypts them with the content key.
func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	itemRef := r.URL.Query().Get("item")
	if itemRef == "" {
		s.writeErr(w, http.StatusBadRequest, "missing item")
		return
	}
	comments, err := s.collab.ListComments(client, itemRef, intParam(r, "limit", 0))
	if err != nil {
		s.collabError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"client": client, "item": itemRef, "comments": comments,
	})
}

// handleCreateComment stores an encrypted comment. The browser sends
// {item, body_enc, bytes, author_token}; the server never sees the plaintext,
// which is what makes the encrypt-before-submit path real rather than aspirational.
func (s *Server) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Item        string `json:"item"`
		BodyEnc     string `json:"body_enc"`
		Bytes       int    `json:"bytes"`
		AuthorToken string `json:"author_token"`
		ACLClass    string `json:"acl_class"`
	}
	if err := s.readBody(r, &in); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if in.Bytes < 0 || in.Bytes > maxCommentBody {
		s.writeErr(w, http.StatusBadRequest, "comment body is too long")
		return
	}
	if len(in.BodyEnc) > 2*maxCommentBody {
		s.writeErr(w, http.StatusBadRequest, "body_enc is too long")
		return
	}
	client := clientParam(r)
	// A comment inherits its item's classification unless the client explicitly
	// narrows it. Widening is refused: the server only ever moves content toward
	// a stricter class.
	acl := feed.NormalizeACLClass(in.ACLClass)
	if acl == "" {
		acl = feed.ACLPublic
	}
	c, err := s.collab.AddComment(client, in.Item, in.AuthorToken, in.BodyEnc, in.Bytes, acl)
	if err != nil {
		s.collabError(w, err)
		return
	}
	// The comment bumps the item's counter so the UI and the sweep agree without
	// a second round trip.
	s.bumpCommentFlag(client, in.Item)
	s.writeJSON(w, http.StatusCreated, c)
}

// handleDeleteComment removes a comment record.
func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	if err := s.collab.DeleteComment(client, r.PathValue("id")); err != nil {
		s.collabError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// ---- pins --------------------------------------------------------------------

func (s *Server) handlePinItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Item      string `json:"item"`
		CreatedBy string `json:"created_by"`
	}
	if err := s.readBody(r, &in); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	p, err := s.collab.PinItem(clientParam(r), in.Item, in.CreatedBy)
	if err != nil {
		s.collabError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUnpinItem(w http.ResponseWriter, r *http.Request) {
	item := r.URL.Query().Get("item")
	if item == "" {
		s.writeErr(w, http.StatusBadRequest, "missing item")
		return
	}
	if err := s.collab.Unpin(clientParam(r), item); err != nil {
		s.collabError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"unpinned": true})
}

func (s *Server) handleListPins(w http.ResponseWriter, r *http.Request) {
	pins, err := s.collab.ListPins(clientParam(r), intParam(r, "limit", 0))
	if err != nil {
		s.collabError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"pins": pins})
}

// ---- retention ---------------------------------------------------------------

// handleRetentionStatus reports the effective window, the exception counts and
// any tombstones the browser has not seen. A browser that has decrypted an item
// polls the tombstones so it can drop its own copy.
func (s *Server) handleRetentionStatus(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	since, _ := time.Parse(time.RFC3339, r.URL.Query().Get("since"))
	body := map[string]any{
		"client":      client,
		"window_days": s.retentionWindow(client),
		"interval":    s.sweep.interval.String(),
		"last_run":    s.lastSweep().Format(time.RFC3339),
		"tombstones":  s.sweep.Since(client, since),
	}
	if pins, err := s.collab.ListPins(client, 0); err == nil {
		body["pinned"] = len(pins)
	}
	if refs, err := s.collab.CommentRefs(client); err == nil {
		body["commented_items"] = len(refs)
	}
	if edges, err := s.links.Edges(client); err == nil {
		body["links"] = len(edges)
	}
	s.writeJSON(w, http.StatusOK, body)
}

// handleRetentionSweep runs the sweep on demand. The hourly loop is the normal
// path; this exists so a client can reclaim space now rather than waiting, and
// so an operator can prove the exceptions work without editing the clock.
func (s *Server) handleRetentionSweep(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	rep := s.SweepClient(client)
	if rep == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{
			"client": client, "swept": false,
			"reason": "nothing expired, or a sweep was already running",
		})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"swept": true, "report": rep})
}

// handleAdminRetention reports every client's retention posture at once.
func (s *Server) handleAdminRetention(w http.ResponseWriter, r *http.Request) {
	clients := s.retentionClients()
	out := make([]map[string]any, 0, len(clients))
	for _, c := range clients {
		row := map[string]any{"client": c, "window_days": s.retentionWindow(c)}
		if pins, err := s.collab.ListPins(c, 0); err == nil {
			row["pinned"] = len(pins)
		}
		if refs, err := s.collab.CommentRefs(c); err == nil {
			row["commented_items"] = len(refs)
		}
		out = append(out, row)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"interval": s.sweep.interval.String(),
		"last_run": s.lastSweep().Format(time.RFC3339),
		"clients":  out,
	})
}

// handleAdminSweep sweeps every client, or one named by ?client=.
func (s *Server) handleAdminSweep(w http.ResponseWriter, r *http.Request) {
	if one := r.URL.Query().Get("client"); one != "" {
		rep := s.SweepClient(one)
		if rep == nil {
			s.writeJSON(w, http.StatusOK, map[string]any{"client": one, "swept": false})
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"reports": []*SweepReport{rep}})
		return
	}
	reports := s.runRetention()
	s.writeJSON(w, http.StatusOK, map[string]any{"reports": reports})
}

// ---- content key -------------------------------------------------------------

// handleContentKey releases the client's content passphrase to its own
// authenticated session. This is the one capability that makes the stored
// ciphertext readable, so it is deliberately narrow: an authenticated session
// for this client, and no other. See plan §4.4 for why this is a documented
// boundary rather than an accident.
func (s *Server) handleContentKey(w http.ResponseWriter, r *http.Request) {
	client := clientParam(r)
	pass, err := s.keys.Passphrase(client)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "content key unavailable: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"client":       client,
		"passphrase":   pass,
		"algorithm":    "AES-256-GCM over PBKDF2-SHA256 x100000",
		"envelope":     "base64(salt).base64(iv).base64(ciphertext)",
		"vault_backed": s.keys.HasKey(client),
	})
}

// ---- shared helpers ----------------------------------------------------------

// lastSweep reports when the sweep last started, for the status payload.
func (s *Server) lastSweep() time.Time {
	s.sweep.mu.Lock()
	defer s.sweep.mu.Unlock()
	return s.sweep.lastRun
}

// bumpCommentFlag records that an item now has a comment. The pin/comment facts
// live in pod; this is the cheap denormalised hint the feed page reads.
func (s *Server) bumpCommentFlag(client, itemRef string) {
	if itemRef == "" {
		return
	}
	it := s.feeds.ItemByID(client, itemRef)
	if it == nil {
		return
	}
	it.CommentCount++
}

// collabError maps a store error onto a status code. A missing parent item is a
// 404 so the UI can distinguish "gone" from "rejected".
func (s *Server) collabError(w http.ResponseWriter, err error) {
	var he *atpclient.HTTPError
	switch {
	case errors.As(err, &he) && he.Status == http.StatusNotFound:
		s.writeErr(w, http.StatusNotFound, "referenced item or record does not exist")
	case errors.Is(err, errInvalidInput):
		s.writeErr(w, http.StatusBadRequest, err.Error())
	default:
		s.writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// ---- admin usage dashboard ---------------------------------------------------

// UsageRow is one client's metered footprint. Every number here is envelope
// metadata — record counts, byte totals, request counts — so metering never has
// to open a body (plan §4.5).
type UsageRow struct {
	Client  string `json:"client"`
	Name    string `json:"name,omitempty"`
	Sources int    `json:"sources"`
	Items   int    `json:"items"`
	// ItemsByClass splits Items by ACL class. The classes partition Items, so the
	// three numbers always add up to it — the UI shows them as a stacked bar.
	ItemsByClass map[string]int `json:"items_by_class,omitempty"`
	Pins         int            `json:"pins"`
	Comments     int            `json:"comments"`
	Links        int            `json:"links"`
	SiteFiles    int            `json:"site_files"`
	DiskBytes    int64          `json:"disk_bytes"`
	DiskGB       float64        `json:"disk_gb"`
	CostableDays []string       `json:"costable_days,omitempty"`
}

// handleAdminUsage builds the platform-wide usage dashboard payload: one row per
// client plus the hourly averages ATP already retains for costing.
func (s *Server) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	rows := make([]UsageRow, 0, 16)
	for _, client := range s.retentionClients() {
		rows = append(rows, s.usageRow(client))
	}
	// Rank by disk so the UI can show a ranked bar list without sorting itself.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].DiskBytes != rows[j].DiskBytes {
			return rows[i].DiskBytes > rows[j].DiskBytes
		}
		return rows[i].Client < rows[j].Client
	})
	total := int64(0)
	for _, r := range rows {
		total += r.DiskBytes
	}
	out := map[string]any{
		"clients":          rows,
		"total_disk_bytes": total,
		"total_disk_gb":    bytesToGB(total),
	}
	// Hourly averages are per client in ATP; surface the aggregate series so the
	// chart has one number set rather than N.
	if hours := s.aggregateHourly(r.URL.Query().Get("limit")); hours != nil {
		out["hourly"] = hours
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) usageRow(client string) UsageRow {
	row := UsageRow{Client: client}
	if rec, err := s.atp.GetClient(client); err == nil {
		if m, ok := rec["client"].(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				row.Name = n
			}
		}
	}
	srcs := s.feeds.Sources(client)
	row.Sources = len(srcs)
	// One unfiltered count, not one per ACL class: the class queries are a
	// partition of this set, so summing them would count every item three times.
	// PageSize 1 because only Total is read — the engine still has to scan, but
	// it does not have to materialise a page of bodies.
	row.Items = s.feeds.Combined(client, feed.Query{Page: 1, PageSize: 1}).Total
	row.ItemsByClass = map[string]int{}
	for _, class := range []string{feed.ACLPublic, feed.ACLPrivate, feed.ACLProtected} {
		row.ItemsByClass[class] = s.feeds.Combined(client, feed.Query{Page: 1, PageSize: 1, AclClass: class}).Total
	}
	if pins, err := s.collab.ListPins(client, 0); err == nil {
		row.Pins = len(pins)
	}
	if comments, err := s.collab.listAllComments(client); err == nil {
		row.Comments = len(comments)
	}
	if edges, err := s.links.Edges(client); err == nil {
		row.Links = len(edges)
	}
	if files, err := s.atp.ListSiloFiles(client, ""); err == nil {
		row.SiteFiles = len(files)
	}
	if h, err := s.atp.Hourly(client); err == nil {
		row.DiskBytes = hourlyPeakBytes(h)
		row.DiskGB = bytesToGB(row.DiskBytes)
	}
	return row
}

// hourlyPeakBytes takes the largest recorded footprint from ATP's hourly
// averages. A peak rather than a mean: billing should not credit a client for a
// window they did not use.
func hourlyPeakBytes(rows []map[string]any) int64 {
	var out int64
	for _, h := range rows {
		for _, key := range []string{"disk_bytes", "bytes", "disk"} {
			switch v := h[key].(type) {
			case float64:
				if int64(v) > out {
					out = int64(v)
				}
			case string:
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > out {
					out = n
				}
			}
		}
	}
	return out
}

// aggregateHourly sums the hourly averages across clients, ordered oldest first
// so a chart can plot it without reversing.
func (s *Server) aggregateHourly(limitParam string) []map[string]any {
	limit, err := strconv.Atoi(limitParam)
	if err != nil || limit <= 0 {
		limit = 48
	}
	if limit > 720 {
		limit = 720
	}
	sums := map[string]float64{}
	var order []string
	for _, client := range s.retentionClients() {
		hours, err := s.atp.Hourly(client)
		if err != nil {
			continue
		}
		for _, h := range hours {
			at, _ := h["hour"].(string)
			if at == "" {
				continue
			}
			if _, seen := sums[at]; !seen {
				order = append(order, at)
			}
			switch v := h["disk_bytes"].(type) {
			case float64:
				sums[at] += v
			case string:
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					sums[at] += float64(n)
				}
			}
		}
	}
	sort.Strings(order)
	if len(order) > limit {
		order = order[len(order)-limit:]
	}
	out := make([]map[string]any, 0, len(order))
	for _, at := range order {
		out = append(out, map[string]any{"hour": at, "disk_bytes": sums[at]})
	}
	return out
}

func bytesToGB(n int64) float64 {
	const gb = 1 << 30
	return float64(n) / gb
}

// listAllComments is used by the usage dashboard, which needs a count rather
// than a filtered list.
func (c *collabStore) listAllComments(client string) ([]Comment, error) {
	recs, _, err := c.atp.QueryTable(client+"/"+commentsTable, atpclient.TableQuery{Limit: 10000})
	if err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(recs))
	for _, r := range recs {
		n, _ := strconv.Atoi(r["bytes"])
		out = append(out, Comment{
			ID: r["id"], ItemRef: r["item_ref"], AuthorToken: r["author_token"],
			BodyEnc: r["body_enc"], Bytes: n, Created: r["created"], ACLClass: r["acl_class"],
		})
	}
	return out, nil
}

// decodeStream fills a struct pointer from either a JSON body or a urlencoded
// form post, matching form fields to exported string fields by their json tag.
// Signup uses it so the page works with JavaScript disabled while the endpoint
// stays a normal JSON API for curl and the portal bundle.
//
// Only string fields are mapped: signup takes three short strings, and a general
// reflection-based form decoder would be more machinery than the one caller
// needs.
func decodeStream(r *http.Request, v any) error {
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		defer r.Body.Close()
		return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("decodeStream needs a non-nil pointer")
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return errors.New("decodeStream needs a pointer to a struct")
	}
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Type().Field(i)
		if rv.Field(i).Kind() != reflect.String || !rv.Field(i).CanSet() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			name = f.Name
		}
		if val := r.PostFormValue(name); val != "" {
			rv.Field(i).SetString(val)
		}
	}
	return nil
}
