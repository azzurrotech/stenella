package stenella_pod

import (
	"database/sql/driver"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type HTTPHandler struct {
	store *PODStoreWithIndex
}

func NewHTTPHandler(store *PODStoreWithIndex) *HTTPHandler {
	return &HTTPHandler{store: store}
}

func (h *HTTPHandler) newConn() *PODConn {
	return &PODConn{store: h.store}
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")

	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r)
	case http.MethodGet:
		h.handleGet(w, r)
	default:
		h.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *HTTPHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.sendError(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	if sql := r.FormValue("sql"); sql != "" {
		h.executeSQLCommand(w, sql, nil)
		return
	}

	if len(r.Form) == 0 {
		h.sendError(w, "No form data provided", http.StatusBadRequest)
		return
	}

	h.store.LockOp()
	defer h.store.UnlockOp()

	columns := make([]string, 0, len(r.Form))
	values := make([]string, 0, len(r.Form))
	for key := range r.Form {
		if key == "sql" {
			continue
		}
		columns = append(columns, key)
		values = append(values, r.FormValue(key))
	}

	db, err := h.store.LoadDB()
	if err != nil {
		h.sendError(w, "Database error", http.StatusInternalServerError)
		return
	}

	record := Record{ID: h.store.GetNextID(db)}
	for i := range columns {
		record.Fields = append(record.Fields, Field{Name: columns[i], Value: values[i]})
	}
	db.Records = append(db.Records, record)
	h.store.UpdateIndexForInsert(record)

	if err := h.store.SaveDB(db); err != nil {
		h.sendError(w, "Save error", http.StatusInternalServerError)
		return
	}

	response := fmt.Sprintf(`<result><status>success</status><id>%d</id></result>`, record.ID)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(response))
}

func (h *HTTPHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	if sql := query.Get("sql"); sql != "" {
		h.executeSQLCommand(w, sql, nil)
		return
	}

	if len(query) == 0 {
		h.returnAllRecords(w)
		return
	}

	h.store.RLockOp()
	defer h.store.RUnlockOp()

	db, err := h.store.LoadDB()
	if err != nil {
		h.sendError(w, "Database error", http.StatusInternalServerError)
		return
	}

	var filtered []Record
	for _, rec := range db.Records {
		match := true
		for key, vals := range query {
			found := false
			for _, f := range rec.Fields {
				if f.Name == key {
					for _, v := range vals {
						if f.Value == v {
							found = true
							break
						}
					}
				}
			}
			if !found {
				match = false
				break
			}
		}
		if match {
			filtered = append(filtered, rec)
		}
	}
	h.recordsToXML(w, filtered)
}

func (h *HTTPHandler) executeSQLCommand(w http.ResponseWriter, sqlCmd string, values []driver.Value) {
	isSelect := strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sqlCmd)), "SELECT")

	if isSelect {
		h.store.RLockOp()
		defer h.store.RUnlockOp()
	} else {
		h.store.LockOp()
		defer h.store.UnlockOp()
	}

	conn := h.newConn()
	stmt, err := conn.Prepare(sqlCmd)
	if err != nil {
		h.sendError(w, fmt.Sprintf("SQL prepare error: %v", err), http.StatusBadRequest)
		return
	}
	defer stmt.Close()

	if isSelect {
		rows, err := stmt.Query(values)
		if err != nil {
			h.sendError(w, fmt.Sprintf("SQL query error: %v", err), http.StatusBadRequest)
			return
		}
		defer rows.Close()
		h.rowsToXML(w, rows)
	} else {
		result, err := stmt.Exec(values)
		if err != nil {
			h.sendError(w, fmt.Sprintf("SQL execution error: %v", err), http.StatusBadRequest)
			return
		}

		var response string
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sqlCmd)), "INSERT") {
			id, _ := result.LastInsertId()
			response = fmt.Sprintf(`<result><status>success</status><id>%d</id></result>`, id)
		} else {
			affected, _ := result.RowsAffected()
			response = fmt.Sprintf(`<result><status>success</status><affected>%d</affected></result>`, affected)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	}
}

func (h *HTTPHandler) returnAllRecords(w http.ResponseWriter) {
	h.store.RLockOp()
	defer h.store.RUnlockOp()

	db, err := h.store.LoadDB()
	if err != nil {
		h.sendError(w, "Database error", http.StatusInternalServerError)
		return
	}

	h.recordsToXML(w, db.Records)
}

func (h *HTTPHandler) rowsToXML(w http.ResponseWriter, rows driver.Rows) {
	columns := rows.Columns()

	var xmlBuilder strings.Builder
	xmlBuilder.WriteString(xml.Header)
	xmlBuilder.WriteString("<records>\n")

	for {
		dest := make([]driver.Value, len(columns))
		for i := range dest {
			dest[i] = new(string)
		}

		err := rows.Next(dest)
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}

		xmlBuilder.WriteString("  <record>\n")
		for i, col := range columns {
			var val string
			switch v := dest[i].(type) {
			case *string:
				if v != nil {
					val = *v
				}
			case string:
				val = v
			case nil:
				val = "NULL"
			default:
				val = fmt.Sprintf("%v", v)
			}

			if val == "" {
				val = "NULL"
			}
			val = escapeXML(val)
			xmlBuilder.WriteString(fmt.Sprintf("    <field name=\"%s\">%s</field>\n", escapeXML(col), val))
		}
		xmlBuilder.WriteString("  </record>\n")
	}

	xmlBuilder.WriteString("</records>")

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(xmlBuilder.String()))
}

func (h *HTTPHandler) recordsToXML(w http.ResponseWriter, records []Record) {
	var sb strings.Builder
	sb.WriteString(xml.Header)
	sb.WriteString("<records>\n")
	for _, rec := range records {
		sb.WriteString(fmt.Sprintf("  <record id=\"%d\">\n", rec.ID))
		for _, f := range rec.Fields {
			sb.WriteString(fmt.Sprintf("    <field name=\"%s\">%s</field>\n",
				escapeXML(f.Name), escapeXML(f.Value)))
		}
		sb.WriteString("  </record>\n")
	}
	sb.WriteString("</records>")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(sb.String()))
}

func (h *HTTPHandler) sendError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(code)
	w.Write([]byte(fmt.Sprintf(`<error><message>%s</message></error>`, escapeXML(message))))
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
