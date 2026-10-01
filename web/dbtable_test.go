package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"azzurrotech/stenella/atpclient"
)

// The create-table route shipped in web.go with no handler behind it. These
// tests pin the endpoint's contract: it creates a table inside the caller's
// namespace, refuses names that would escape it, and refuses column names that
// pod would echo back into markup.

func TestCreateTableThroughPortal(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	admin := setupACLPortal(t, h, "acme")

	rec := webReq(t, h, "POST", "/s/api/portal/db/table/create?client=acme",
		`{"table":"widgets","columns":["name","price"]}`, []*http.Cookie{admin})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create table: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Client  string   `json:"client"`
		Table   string   `json:"table"`
		Columns []string `json:"columns"`
	}
	mustDecode(t, rec, &out)
	if out.Table != "widgets" || len(out.Columns) != 2 {
		t.Fatalf("create response = %+v", out)
	}

	// The table is immediately usable through the normal insert path.
	rec = webReq(t, h, "POST", "/s/api/portal/db/table?client=acme",
		`{"table":"widgets","fields":{"name":"cog","price":"3"}}`, []*http.Cookie{admin})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("insert into new table: %d %s", rec.Code, rec.Body.String())
	}

	var q struct {
		Count int
	}
	mustDecode(t, webReq(t, h, "GET",
		"/s/api/portal/db/table?client=acme&table=widgets&page=1&pageSize=10", "",
		[]*http.Cookie{admin}), &q)
	if q.Count != 1 {
		t.Fatalf("new table count = %d, want 1", q.Count)
	}
}

// Creating a table is the one database endpoint that introduces new schema, so
// it must refuse the same namespace escapes the other endpoints reject.
func TestCreateTableRejectsNamespaceEscape(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	admin := setupACLPortal(t, h, "acme")

	bad := []string{
		"../victim",
		"a/b",
		"/leading",
		"trailing/",
		"..",
		"",
	}
	for _, name := range bad {
		rec := webReq(t, h, "POST", "/s/api/portal/db/table/create?client=acme",
			fmt.Sprintf(`{"table":%q,"columns":["a"]}`, name), []*http.Cookie{admin})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("table %q accepted: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestCreateTableRejectsMalformedColumns(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	admin := setupACLPortal(t, h, "acme")

	bad := []string{
		"with space",
		"quote\"injection",
		"tag<b>",
		"slash/name",
		"equals=sign",
		"",
	}
	for _, col := range bad {
		rec := webReq(t, h, "POST", "/s/api/portal/db/table/create?client=acme",
			fmt.Sprintf(`{"table":"t","columns":[%q]}`, col), []*http.Cookie{admin})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("column %q accepted: %d %s", col, rec.Code, rec.Body.String())
		}
	}
}

// The route exists, so it must be behind the same authentication as every other
// portal endpoint.
func TestCreateTableRequiresAuth(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()

	rec := webReq(t, h, "POST", "/s/api/portal/db/table/create?client=acme",
		`{"table":"sneaky","columns":["a"]}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous create table: %d %s", rec.Code, rec.Body.String())
	}
}

// Bulk insert is wired to the same batch endpoint; a single bad row must not
// discard the good ones.
func TestDBBulkInsert(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	admin := setupACLPortal(t, h, "acme")

	if err := s.atp.CreateTable("acme/rows", []string{"name"}); err != nil {
		t.Fatalf("create table: %v", err)
	}

	rec := webReq(t, h, "POST", "/s/api/portal/db/table/bulk?client=acme&table=rows",
		`{"records":[{"name":"a"},{"name":"b"},{"name":"c"}]}`, []*http.Cookie{admin})
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk insert: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Inserted int `json:"inserted"`
	}
	mustDecode(t, rec, &out)
	if out.Inserted != 3 {
		t.Fatalf("inserted = %d, want 3", out.Inserted)
	}

	// An empty batch is a no-op, not an error.
	rec = webReq(t, h, "POST", "/s/api/portal/db/table/bulk?client=acme&table=rows",
		`{"records":[]}`, []*http.Cookie{admin})
	if rec.Code != http.StatusOK {
		t.Fatalf("empty bulk: %d %s", rec.Code, rec.Body.String())
	}
	mustDecode(t, rec, &out)
	if out.Inserted != 0 {
		t.Fatalf("empty batch inserted %d", out.Inserted)
	}

	// Bulk insert must not accept a namespace escape either.
	rec = webReq(t, h, "POST", "/s/api/portal/db/table/bulk?client=acme&table=../x",
		`{"records":[{"a":"1"}]}`, []*http.Cookie{admin})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bulk traversal accepted: %d %s", rec.Code, rec.Body.String())
	}
}

// sites/meta reports a file *count*; the portal renders it as a number.
func TestSitesMetaReportsCount(t *testing.T) {
	s, _ := newTestWeb(t)
	h := s.Handler()
	admin := setupACLPortal(t, h, "acme")

	if err := s.atp.CreateSongFile("acme", atpclient.SongFileOp{
		Path: "index.html", Content: "<h1>hi</h1>", Overwrite: true,
	}); err != nil {
		t.Fatalf("create song file: %v", err)
	}

	rec := webReq(t, h, "GET", "/s/api/portal/sites/meta?client=acme", "", []*http.Cookie{admin})
	if rec.Code != http.StatusOK {
		t.Fatalf("sites meta: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"files":1`) {
		t.Fatalf("sites meta did not report a count: %s", rec.Body.String())
	}
}
