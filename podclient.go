// podclient.go — stenella's record layer over HTTP.
//
// Stenella no longer links pod as a Go package: pod is a service at
// POD_URL. This file is the client half of that contract. It repeats the two
// pieces of pod semantics a remote store cannot give us for free:
//
//   - the struct codec (record struct <-> map[string]string), so the model in
//     model.go is still written as readable text fields, and
//   - condition evaluation, because pod's REST query surface accepts equality
//     filters only. Fetching a table and filtering here keeps the expressive
//     Cond/Like/And/Or queries the platform already relies on.
//
// The wire shapes are pod's documented JSON: POST /tables, POST /table/<t>,
// GET/DELETE /record/<t>/<id>, GET /table/<t>.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned when pod has no such record.
var ErrNotFound = errors.New("pod: record not found")

// Record is one pod row. Fields are the text values the platform writes.
type Record struct {
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields"`
}

// Values returns the record fields as a map (the codec's input).
func (r *Record) Values() map[string]string { return r.Fields }

// decodeInto rebuilds a struct from the record. pod's HTTP API carries the id
// as the record attribute rather than as a field (an in-process Store kept it
// in both places), so the id is re-attached before the codec runs; otherwise
// every record read over HTTP would come back with an empty ID field.
func (r *Record) decodeInto(out any) error {
	fields := r.Fields
	if _, ok := fields["id"]; !ok && r.ID != "" {
		fields = make(map[string]string, len(r.Fields)+1)
		for k, v := range r.Fields {
			fields[k] = v
		}
		fields["id"] = r.ID
	}
	return applyStruct(fields, out)
}

// Get returns a field value and whether it exists.
func (r *Record) Get(name string) (string, bool) {
	v, ok := r.Fields[name]
	return v, ok
}

// TableInfo summarizes one pod table.
type TableInfo struct {
	Name   string         `json:"name"`
	Count  int            `json:"count"`
	Schema map[string]any `json:"schema,omitempty"`
}

// PodClient talks to a running pod server.
type PodClient struct {
	base string
	http *http.Client
}

// NewPodClient returns a client for base, e.g. "http://127.0.0.1:8081".
func NewPodClient(base string) *PodClient {
	return &PodClient{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// Base returns the service URL this client talks to.
func (c *PodClient) Base() string { return c.base }

// Health probes pod's /health endpoint.
func (c *PodClient) Health() error { return c.getJSON("/health", nil) }

func (c *PodClient) do(method, path string, body any, out any) error {
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
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("pod: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("pod: %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("pod: decode %s: %w", path, err)
	}
	return nil
}

func (c *PodClient) getJSON(path string, out any) error {
	return c.do(http.MethodGet, path, nil, out)
}

func tablePath(table string) string { return "/table/" + url.PathEscape(table) }

func recordPath(table, id string) string {
	return "/record/" + url.PathEscape(table) + "/" + url.PathEscape(id)
}

// EnsureSchema creates or widens a table so its columns match the struct.
func (c *PodClient) EnsureSchema(table string, sample any) error {
	names, err := structColumns(sample)
	if err != nil {
		return err
	}
	cols := make([]map[string]string, 0, len(names))
	for _, n := range names {
		cols = append(cols, map[string]string{"name": n, "type": "text"})
	}
	if err := c.do(http.MethodPost, "/tables", map[string]any{"table": table, "columns": cols}, nil); err != nil {
		return fmt.Errorf("pod: schema %s: %w", table, err)
	}
	return nil
}

// Upsert writes a record struct to a table.
func (c *PodClient) Upsert(table, id string, rec any) error {
	fields, err := structFields(rec)
	if err != nil {
		return err
	}
	body := map[string]any{"id": id}
	for k, v := range fields {
		body[k] = v
	}
	if err := c.do(http.MethodPost, tablePath(table), body, nil); err != nil {
		return fmt.Errorf("pod: upsert %s/%s: %w", table, id, err)
	}
	return nil
}

// GetStruct reads one record and decodes it into out.
func (c *PodClient) GetStruct(table, id string, out any) error {
	var rec Record
	if err := c.do(http.MethodGet, recordPath(table, id), nil, &rec); err != nil {
		return err
	}
	return rec.decodeInto(out)
}

// Delete removes one record.
func (c *PodClient) Delete(table, id string) error {
	return c.do(http.MethodDelete, recordPath(table, id), nil, nil)
}

// List returns every record in a table.
func (c *PodClient) List(table string) ([]*Record, error) {
	var out struct {
		Count   int       `json:"count"`
		Records []*Record `json:"records"`
	}
	if err := c.getJSON(tablePath(table), &out); err != nil {
		return nil, err
	}
	if out.Records == nil {
		out.Records = []*Record{}
	}
	return out.Records, nil
}

// IDs lists the ids in a table.
func (c *PodClient) IDs(table string) ([]string, error) {
	recs, err := c.List(table)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// Tables lists pod's tables.
func (c *PodClient) Tables() ([]TableInfo, error) {
	var out struct {
		Tables []TableInfo `json:"tables"`
	}
	if err := c.getJSON("/tables", &out); err != nil {
		return nil, err
	}
	if out.Tables == nil {
		out.Tables = []TableInfo{}
	}
	sort.SliceStable(out.Tables, func(i, j int) bool { return out.Tables[i].Name < out.Tables[j].Name })
	return out.Tables, nil
}

// Query fetches a table and applies the condition, ordering and paging here.
func (c *PodClient) Query(table string, cond Cond, opts *QueryOptions) ([]*Record, error) {
	recs, err := c.List(table)
	if err != nil {
		return nil, err
	}
	if cond != nil {
		filtered := make([]*Record, 0, len(recs))
		for _, r := range recs {
			if cond.Match(r) {
				filtered = append(filtered, r)
			}
		}
		recs = filtered
	}
	if opts != nil {
		if opts.OrderBy != "" {
			sort.Slice(recs, func(i, j int) bool {
				a, _ := recs[i].Get(opts.OrderBy)
				b, _ := recs[j].Get(opts.OrderBy)
				cmp := sortValues(a, b)
				if opts.Desc {
					return cmp > 0
				}
				return cmp < 0
			})
		}
		off := opts.Offset
		if off < 0 {
			off = 0
		}
		if len(recs) > off {
			recs = recs[off:]
		} else {
			recs = []*Record{}
		}
		if opts.Limit > 0 && len(recs) > opts.Limit {
			recs = recs[:opts.Limit]
		}
	}
	return recs, nil
}

// QueryStruct decodes every matching record into a new slice of T. Records
// that no longer decode are skipped, as pod's own QueryStruct does.
func QueryStruct[T any](c *PodClient, table string, cond Cond, opts *QueryOptions) ([]*T, error) {
	recs, err := c.Query(table, cond, opts)
	if err != nil {
		return nil, err
	}
	out := make([]*T, 0, len(recs))
	for _, r := range recs {
		var v T
		if err := r.decodeInto(&v); err != nil {
			continue
		}
		out = append(out, &v)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// conditions
// ---------------------------------------------------------------------------

// Op is a comparison operator used by predicates.
type Op int

const (
	OpEq Op = iota
	OpNe
	OpGt
	OpGe
	OpLt
	OpLe
	OpLike
	OpNotLike
	OpIsNull
	OpIsNotNull
)

// Predicate is a single field comparison.
type Predicate struct {
	Field    string
	Op       Op
	Value    string
	HasValue bool
	Null     bool
}

// Cond is a boolean filter tree over records.
type Cond interface{ Match(r *Record) bool }

// PredCond wraps a single predicate.
type PredCond struct{ P Predicate }

func (c PredCond) Match(r *Record) bool {
	v, has := r.Get(c.P.Field)
	return c.P.Matches(v, has)
}

// AndCond matches when both sides match.
type AndCond struct{ L, R Cond }

func (c AndCond) Match(r *Record) bool { return c.L.Match(r) && c.R.Match(r) }

// OrCond matches when either side matches.
type OrCond struct{ L, R Cond }

func (c OrCond) Match(r *Record) bool { return c.L.Match(r) || c.R.Match(r) }

// Matches evaluates the predicate against a field value.
func (p Predicate) Matches(v string, has bool) bool {
	switch p.Op {
	case OpEq:
		if p.Null {
			return false
		}
		if !p.HasValue {
			return true
		}
		return has && v == p.Value
	case OpNe:
		if p.Null {
			return false
		}
		if !p.HasValue {
			return !has || v != ""
		}
		return !has || v != p.Value
	case OpIsNull:
		return !has
	case OpIsNotNull:
		return has
	case OpLike:
		if !p.HasValue || !has {
			return false
		}
		return likeMatch(p.Value, v)
	case OpNotLike:
		if !p.HasValue || !has {
			return true
		}
		return !likeMatch(p.Value, v)
	default:
		if !p.HasValue || !has {
			return false
		}
		return compareValues(v, p.Value, p.Op)
	}
}

// likeMatch implements SQL LIKE semantics for '%' and '_'.
func likeMatch(pattern, s string) bool {
	if pattern == "" {
		return s == ""
	}
	switch pattern[0] {
	case '%':
		i := 0
		for i < len(pattern) && pattern[i] == '%' {
			i++
		}
		rest := pattern[i:]
		if rest == "" {
			return true
		}
		for j := 0; j <= len(s); j++ {
			if likeMatch(rest, s[j:]) {
				return true
			}
		}
		return false
	case '_':
		return len(s) >= 1 && likeMatch(pattern[1:], s[1:])
	default:
		return len(s) >= 1 && s[0] == pattern[0] && likeMatch(pattern[1:], s[1:])
	}
}

func compareValues(a, b string, op Op) bool {
	af, aerr := strconv.ParseFloat(strings.TrimSpace(a), 64)
	bf, berr := strconv.ParseFloat(strings.TrimSpace(b), 64)
	var cmp int
	if aerr == nil && berr == nil {
		switch {
		case af < bf:
			cmp = -1
		case af > bf:
			cmp = 1
		}
	} else {
		cmp = strings.Compare(a, b)
	}
	switch op {
	case OpGt:
		return cmp > 0
	case OpGe:
		return cmp >= 0
	case OpLt:
		return cmp < 0
	case OpLe:
		return cmp <= 0
	}
	return false
}

// sortValues compares two values for ORDER BY, numeric-aware.
func sortValues(a, b string) int {
	af, aerr := strconv.ParseFloat(strings.TrimSpace(a), 64)
	bf, berr := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if aerr == nil && berr == nil {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(a, b)
}

// QueryOptions controls ordering and paging.
type QueryOptions struct {
	OrderBy string
	Desc    bool
	Limit   int
	Offset  int
}

// ---------------------------------------------------------------------------
// struct codec (pod's mapping, kept local now that pod is remote)
// ---------------------------------------------------------------------------

var structTimeType = reflect.TypeOf(time.Time{})

func fieldTag(f reflect.StructField) (name string, omit bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "-", false
	}
	name = f.Name
	if tag == "" {
		return name, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] != "" {
		name = parts[0]
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omit = true
		}
	}
	return name, omit
}

// structFields flattens a record struct into pod's field map.
func structFields(v any) (map[string]string, error) {
	rv, err := structValue(v)
	if err != nil {
		return nil, err
	}
	rt := rv.Type()
	out := make(map[string]string, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, omit := fieldTag(f)
		if name == "-" {
			continue
		}
		s, skip, err := encodeStructValue(rv.Field(i), omit)
		if err != nil {
			return nil, fmt.Errorf("pod codec: %s.%s: %w", rt.Name(), f.Name, err)
		}
		if skip {
			continue
		}
		out[name] = s
	}
	return out, nil
}

// applyStruct rebuilds a record struct from pod's field map.
func applyStruct(m map[string]string, v any) error {
	rv, err := structValue(v)
	if err != nil {
		return err
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _ := fieldTag(f)
		if name == "-" {
			continue
		}
		s, ok := m[name]
		if !ok {
			continue
		}
		if err := decodeStructValue(s, rv.Field(i)); err != nil {
			return fmt.Errorf("pod codec: %s.%s: %w", rt.Name(), f.Name, err)
		}
	}
	return nil
}

// structColumns derives the column names from a record struct.
func structColumns(sample any) ([]string, error) {
	rv, err := structValue(sample)
	if err != nil {
		return nil, err
	}
	rt := rv.Type()
	names := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _ := fieldTag(f)
		if name == "-" {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

func structValue(v any) (reflect.Value, error) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return reflect.Value{}, fmt.Errorf("pod codec: nil %T", v)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("pod codec: %T is not a struct", v)
	}
	return rv, nil
}

func encodeStructValue(fv reflect.Value, omit bool) (s string, skip bool, err error) {
	if !fv.IsValid() {
		return "", true, nil
	}
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			return "", true, nil
		}
		fv = fv.Elem()
	}
	if fv.Type() == structTimeType {
		t := fv.Interface().(time.Time)
		if t.IsZero() {
			return "", omit, nil
		}
		return t.UTC().Format(time.RFC3339), false, nil
	}
	switch fv.Kind() {
	case reflect.String:
		s = fv.String()
		return s, omit && s == "", nil
	case reflect.Bool:
		b := fv.Bool()
		if omit && !b {
			return "", true, nil
		}
		return strconv.FormatBool(b), false, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := fv.Int()
		if omit && n == 0 {
			return "", true, nil
		}
		return strconv.FormatInt(n, 10), false, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n := fv.Uint()
		if omit && n == 0 {
			return "", true, nil
		}
		return strconv.FormatUint(n, 10), false, nil
	case reflect.Float32, reflect.Float64:
		f := fv.Float()
		if omit && f == 0 {
			return "", true, nil
		}
		return strconv.FormatFloat(f, 'g', -1, 64), false, nil
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		if omit && fv.Len() == 0 {
			return "", true, nil
		}
		b, err := json.Marshal(fv.Interface())
		if err != nil {
			return "", false, err
		}
		return string(b), false, nil
	}
	return "", false, fmt.Errorf("unsupported field kind %s", fv.Kind())
}

func decodeStructValue(s string, fv reflect.Value) error {
	if fv.Type() == structTimeType {
		t, err := parseTime(s)
		if err != nil {
			return err
		}
		fv.Set(reflect.ValueOf(t))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		return json.Unmarshal([]byte(s), fv.Addr().Interface())
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}

// parseTime accepts the RFC 3339 form the codec writes, plus pod's form forms.
func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, l := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("pod codec: cannot parse time %q", s)
}
