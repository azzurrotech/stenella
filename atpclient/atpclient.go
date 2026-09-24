// Package atpclient is stenella's in-process client for the embedded atp
// orchestrator. Instead of calling song, pod or shepherd directly, stenella
// talks to atp's own HTTP handler: every request flows through atp's auth,
// scoping, usage logging and billing layers exactly like a real client would.
//
// The handler is invoked in-process (no network) using net/http/httptest, so
// this stays standard-library only and adds no latency.
package atpclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client is a handle on the embedded atp HTTP surface.
type Client struct {
	handler http.Handler

	mu     sync.RWMutex
	token  string // admin bearer token (X-ATP-Token) used for admin APIs
	user   string
	pass   string
	expiry time.Time
}

// New wraps an atp http.Handler (web.ATPService.Handler() or Middleware(...)).
func New(handler http.Handler, adminUser, adminPassword string) *Client {
	return &Client{handler: handler, user: adminUser, pass: adminPassword}
}

// Handler exposes the wrapped atp handler (used by the server wiring).
func (c *Client) Handler() http.Handler { return c.handler }

// ---- internal request plumbing ---------------------------------------------

const maxBody = 64 << 20

type response struct {
	status int
	body   []byte
	header http.Header
}

// do performs a request against the atp handler. when token is non-empty it is
// sent as the X-ATP-Token admin header; cookies are sent verbatim.
func (c *Client) do(method, path string, query url.Values, body any, token string, cookies []*http.Cookie) (*response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	u := path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req := httptest.NewRequest(method, u, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("X-ATP-Token", token)
	}
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	rr := httptest.NewRecorder()
	c.handler.ServeHTTP(rr, req)
	res := rr.Result()
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, err
	}
	return &response{status: res.StatusCode, body: b, header: res.Header}, nil
}

// ---- admin session ----------------------------------------------------------

// Login authenticates to atp's admin API and caches the session token.
func (c *Client) Login() error {
	var out struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
	}
	if err := c.JSONPublic("POST", "/login", nil, map[string]any{"user": c.user, "password": c.pass}, &out); err != nil {
		return fmt.Errorf("atp login: %w", err)
	}
	if out.Token == "" {
		return errors.New("atp login: empty token")
	}
	c.mu.Lock()
	c.token = out.Token
	c.expiry = time.Now().Add(24 * time.Hour) // re-login before the cookie-style TTL matters
	c.mu.Unlock()
	return nil
}

// Token returns the cached admin token, re-logging in when needed.
func (c *Client) Token() (string, error) {
	c.mu.RLock()
	tok, expiry := c.token, c.expiry
	c.mu.RUnlock()
	if tok != "" && time.Now().Before(expiry) {
		return tok, nil
	}
	if err := c.Login(); err != nil {
		return "", err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token, nil
}

// AdminValid reports whether a bearer token or session cookie authenticates to
// the atp admin API. The request is dispatched to atp's own gate.
func (c *Client) AdminValid(token string, cookies []*http.Cookie) bool {
	if token != "" {
		res, err := c.do("GET", "/api/summary", nil, nil, token, nil)
		return err == nil && res.status == http.StatusOK
	}
	if len(cookies) == 0 {
		return false
	}
	res, err := c.do("GET", "/api/summary", nil, nil, "", cookies)
	return err == nil && res.status == http.StatusOK
}

// AdminCookie is a convenience head for building cookies.
func AdminCookie(name, value string) *http.Cookie {
	return &http.Cookie{Name: name, Value: value}
}

// ---- JSON helpers ------------------------------------------------------------

// JSON performs an authenticated admin request and decodes the JSON body into
// out (when out is non-nil). Query params are appended for GET/DELETE.
func (c *Client) JSON(method, path string, query url.Values, body, out any) error {
	tok, err := c.Token()
	if err != nil {
		return err
	}
	return c.JSONWithToken(method, path, query, body, out, tok)
}

// JSONWithToken performs an admin request using an explicit token.
func (c *Client) JSONWithToken(method, path string, query url.Values, body, out any, token string) error {
	res, err := c.do(method, path, query, body, token, nil)
	if err != nil {
		return err
	}
	return c.decode(res, out)
}

// JSONPublic performs an unauthenticated request (public atp endpoints).
func (c *Client) JSONPublic(method, path string, query url.Values, body, out any) error {
	res, err := c.do(method, path, query, body, "", nil)
	if err != nil {
		return err
	}
	return c.decode(res, out)
}

func (c *Client) decode(res *response, out any) error {
	if res.status >= 400 {
		msg := strings.TrimSpace(string(res.body))
		if msg == "" {
			msg = http.StatusText(res.status)
		}
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return &HTTPError{Status: res.status, Body: msg}
	}
	if out == nil || len(res.body) == 0 {
		return nil
	}
	if err := json.Unmarshal(res.body, out); err != nil {
		return fmt.Errorf("atp: decode %d %s: %w (%s)", res.status, res.header.Get("Content-Type"), err, truncate(string(res.body), 200))
	}
	return nil
}

// HTTPError is an HTTP-level error from atp.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("atp returned %d: %s", e.Status, e.Body) }

// Is returns true when target is an *HTTPError with the same status.
func (e *HTTPError) Is(target error) bool {
	t, ok := target.(*HTTPError)
	return ok && t.Status == e.Status
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- typed helpers (thin, stateless wrappers over atp's own handlers) --------

// Summary is atp's platform overview.
func (c *Client) Summary() (map[string]any, error) {
	var out map[string]any
	if err := c.JSON("GET", "/api/summary", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ClientRecord mirrors what atp returns for a client.
type ClientRecord struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Created        string  `json:"created"`
	Disabled       bool    `json:"disabled,omitempty"`
	Chargeable     bool    `json:"chargeable"`
	PricePerGBHour float64 `json:"price_per_gb_hour"`
	RetentionHours int     `json:"retention_hours"`
	Notes          string  `json:"notes,omitempty"`
}

// ListClients returns every registered client.
func (c *Client) ListClients() ([]ClientRecord, error) {
	var out struct {
		Clients []ClientRecord `json:"clients"`
	}
	if err := c.JSON("GET", "/api/clients", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Clients, nil
}

// GetClient returns a single client plus an overview payload.
func (c *Client) GetClient(id string) (map[string]any, error) {
	var out map[string]any
	if err := c.JSON("GET", "/api/clients/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateClient registers a client (atp also provisions the song silo).
func (c *Client) CreateClient(id, name, notes string) error {
	return c.JSON("POST", "/api/clients", nil, map[string]any{"id": id, "name": name, "notes": notes}, nil)
}

// UpdateClient patches a client record (only non-zero fields are sent).
func (c *Client) UpdateClient(id string, patch map[string]any) error {
	if len(patch) == 0 {
		return nil
	}
	return c.JSON("PUT", "/api/clients/"+url.PathEscape(id), nil, patch, nil)
}

// DeleteClient removes a client registry entry and its song silo.
func (c *Client) DeleteClient(id string) error {
	return c.JSON("DELETE", "/api/clients/"+url.PathEscape(id), nil, nil, nil)
}

// Secret is a vault entry as returned by atp's list endpoint (never the value).
type Secret struct {
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
	Set  string `json:"set,omitempty"`
}

// ListSecrets lists a client's vault entries.
func (c *Client) ListSecrets(client string) ([]Secret, error) {
	var out struct {
		Secrets []Secret `json:"secrets"`
	}
	if err := c.JSON("GET", "/api/clients/"+url.PathEscape(client)+"/secrets", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Secrets, nil
}

// SetSecret upserts a vault entry.
func (c *Client) SetSecret(client, name, value, note string) error {
	return c.JSON("POST", "/api/clients/"+url.PathEscape(client)+"/secrets", nil,
		map[string]any{"name": name, "value": value, "note": note}, nil)
}

// GetSecret returns the decrypted vault value (admin only).
func (c *Client) GetSecret(client, name string) (string, error) {
	var out struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := c.JSON("GET", "/api/clients/"+url.PathEscape(client)+"/secrets/"+url.PathEscape(name), nil, nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// DeleteSecret removes a vault entry.
func (c *Client) DeleteSecret(client, name string) error {
	return c.JSON("DELETE", "/api/clients/"+url.PathEscape(client)+"/secrets/"+url.PathEscape(name), nil, nil, nil)
}

// podRecord is one row of a pod table as returned by atp's pod pass-through.
type podRecord struct {
	ID            string            `json:"id"`
	Created       string            `json:"created,omitempty"`
	Updated       string            `json:"updated,omitempty"`
	Version       int               `json:"version"`
	SchemaVersion int               `json:"schema_version"`
	Fields        map[string]string `json:"fields"`
}

// podList is the JSON shape of a table query.
type podList struct {
	Count   int         `json:"count"`
	Records []podRecord `json:"records"`
}

// TableQuery options mirror the pod query parameters.
type TableQuery struct {
	Limit   int
	Offset  int
	OrderBy string
	Desc    bool
	Q       string
}

// QueryTable reads records from a pod table through atp's /api/pod pass-through.
// It returns the records and the count pod reported.
func (c *Client) QueryTable(table string, tq TableQuery) ([]map[string]string, int, error) {
	q := url.Values{}
	if tq.Limit > 0 {
		q.Set("limit", itoa(tq.Limit))
	}
	if tq.Offset > 0 {
		q.Set("offset", itoa(tq.Offset))
	}
	if tq.OrderBy != "" {
		q.Set("orderby", tq.OrderBy)
	}
	if tq.Desc {
		q.Set("dir", "desc")
	}
	if tq.Q != "" {
		q.Set("q", tq.Q)
	}
	q.Set("format", "json")
	var out podList
	if err := c.JSON("GET", "/api/pod/table/"+escapePath(table), q, nil, &out); err != nil {
		return nil, 0, err
	}
	recs := make([]map[string]string, 0, len(out.Records))
	for _, r := range out.Records {
		r.Fields["id"] = r.ID
		r.Fields["created"] = r.Created
		r.Fields["updated"] = r.Updated
		recs = append(recs, r.Fields)
	}
	return recs, out.Count, nil
}

// GetRecord reads a single pod record.
func (c *Client) GetRecord(table, id string) (map[string]string, error) {
	var out podRecord
	if err := c.JSON("GET", "/api/pod/record/"+escapePath(table)+"/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	out.Fields["id"] = out.ID
	out.Fields["created"] = out.Created
	out.Fields["updated"] = out.Updated
	return out.Fields, nil
}

// UpsertRecord inserts or updates a pod record (id inside values, or empty to
// generate). Returns the stored record (including its id).
func (c *Client) UpsertRecord(table string, values map[string]string) (map[string]string, error) {
	var out struct {
		Status  string    `json:"status"`
		ID      string    `json:"id"`
		Created bool      `json:"created"`
		Record  podRecord `json:"record"`
	}
	if err := c.JSON("POST", "/api/pod/table/"+escapePath(table), nil, values, &out); err != nil {
		return nil, err
	}
	rec := out.Record.Fields
	rec["id"] = out.Record.ID
	rec["created"] = out.Record.Created
	rec["updated"] = out.Record.Updated
	return rec, nil
}

// DeleteRecord removes a pod record.
func (c *Client) DeleteRecord(table, id string) error {
	return c.JSON("DELETE", "/api/pod/record/"+escapePath(table)+"/"+url.PathEscape(id), nil, nil, nil)
}

// ListTables enumerates a client's pod tables with row counts.
func (c *Client) ListTables(client string) ([]map[string]any, error) {
	var out struct {
		Tables []map[string]any `json:"tables"`
	}
	if err := c.JSON("GET", "/api/clients/"+url.PathEscape(client)+"/tables", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Tables, nil
}

// CreateTable creates a pod table with the given columns.
func (c *Client) CreateTable(table string, cols []string) error {
	body := map[string]any{"table": table}
	colObjs := make([]map[string]any, 0, len(cols))
	for _, col := range cols {
		colObjs = append(colObjs, map[string]any{"name": col, "type": "text"})
	}
	if len(colObjs) > 0 {
		body["columns"] = colObjs
	}
	return c.JSON("POST", "/api/pod/tables", nil, body, nil)
}

// Billing returns atp's billing summary for a client.
func (c *Client) Billing(client string) (map[string]any, error) {
	var out map[string]any
	if err := c.JSON("GET", "/api/clients/"+url.PathEscape(client)+"/billing", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Hourly returns per-hour usage rollups for a client.
func (c *Client) Hourly(client string) ([]map[string]any, error) {
	var out struct {
		Hourly []map[string]any `json:"hourly"`
	}
	if err := c.JSON("GET", "/api/clients/"+url.PathEscape(client)+"/usage/hourly", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Hourly, nil
}

// Usage returns the most recent request-log records for a client.
func (c *Client) Usage(client string, limit int) ([]map[string]any, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	var out struct {
		Records []map[string]any `json:"records"`
	}
	if err := c.JSON("GET", "/api/clients/"+url.PathEscape(client)+"/usage", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Records, nil
}

// Settings returns atp's platform settings.
func (c *Client) Settings() (map[string]any, error) {
	var out map[string]any
	if err := c.JSON("GET", "/api/config", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutSettings updates atp platform settings (only non-zero values overwrite).
func (c *Client) PutSettings(patch map[string]any) error {
	return c.JSON("PUT", "/api/config", nil, patch, nil)
}

// IssueKey issues a capability token for a client through atp's scoped shep
// endpoint (the client scope is forced by atp itself).
func (c *Client) IssueKey(client string, args map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := c.JSON("POST", "/api/clients/"+url.PathEscape(client)+"/keys", nil, args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// IssueBlock issues a key block for a client (atp passes count through to the
// embedded shepherd manager).
func (c *Client) IssueBlock(client string, args map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := c.JSON("POST", "/api/clients/"+url.PathEscape(client)+"/keys/block", nil, args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Revoke invalidates a token or whole block for a client.
func (c *Client) Revoke(client string, args map[string]any) error {
	return c.JSON("POST", "/api/clients/"+url.PathEscape(client)+"/revoke", nil, args, nil)
}

// VerifyToken checks a capability token against a client's scope.
func (c *Client) VerifyToken(client, token string) (map[string]any, error) {
	var out map[string]any
	q := url.Values{"t": {token}}
	if err := c.JSON("GET", "/api/clients/"+url.PathEscape(client)+"/verify", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListSiloFiles lists a client's song files (their hosted web app).
func (c *Client) ListSiloFiles(client, dir string) ([]map[string]any, error) {
	q := url.Values{}
	if dir != "" {
		q.Set("path", dir)
	}
	var out struct {
		Files []map[string]any `json:"files"`
	}
	if err := c.JSON("GET", "/api/song/silos/"+url.PathEscape(client)+"/files", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

// SongFileOp is the body song's file endpoints expect.
type SongFileOp struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Encrypt   *bool  `json:"encrypt,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

// CreateSongFile creates or overwrites a file in the client's song silo.
func (c *Client) CreateSongFile(client string, op SongFileOp) error {
	return c.JSON("POST", "/api/song/silos/"+url.PathEscape(client)+"/file", nil, op, nil)
}

// UpdateSongFile updates a file in the client's song silo.
func (c *Client) UpdateSongFile(client string, op SongFileOp) error {
	return c.JSON("PUT", "/api/song/silos/"+url.PathEscape(client)+"/file", nil, op, nil)
}

// DeleteSongFile removes a file from the client's song silo.
func (c *Client) DeleteSongFile(client, path string) error {
	q := url.Values{"path": {path}}
	return c.JSON("DELETE", "/api/song/silos/"+url.PathEscape(client)+"/file", q, nil, nil)
}

// JSKey returns the client-scoped JS crypto key from atp (for vici).
func (c *Client) JSKey(client string) (map[string]any, error) {
	var out map[string]any
	if err := c.JSON("GET", "/api/clients/"+url.PathEscape(client)+"/jskey", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// escapePath escapes each segment of a slash-separated path so that pod table
// names (which may contain client/table) map to a single safe path.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	return strings.Join(segs, "/")
}
