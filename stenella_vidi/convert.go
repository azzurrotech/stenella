package stenella_vidi

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type SQLDialect string

const (
	PostgreSQL SQLDialect = "postgresql"
	MySQL     SQLDialect = "mysql"
	SQLite    SQLDialect = "sqlite"
	SQLServer SQLDialect = "sqlserver"
)

func ToJSON(data interface{}) ([]byte, error) {
	return json.MarshalIndent(data, "", "  ")
}

func ToXML(data interface{}) ([]byte, error) {
	if m, ok := data.(map[string]interface{}); ok {
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
		sb.WriteString("<root>\n")
		for k, v := range m {
			sb.WriteString(fmt.Sprintf("  <%s>%v</%s>\n", k, v, k))
		}
		sb.WriteString("</root>")
		return []byte(sb.String()), nil
	}
	if arr, ok := data.([]interface{}); ok {
		return NewXMLConverter().writeSliceXML(arr)
	}
	return xml.Marshal(data)
}

func (xc *XMLConverter) writeSliceXML(data []interface{}) ([]byte, error) {
	var buf bytes.Buffer
	if xc.IncludeXMLDeclaration {
		buf.WriteString(xml.Header)
	}
	if err := xc.writeSlice(&buf, data, xc.RootElement, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ToSQL(data map[string]interface{}, tableName string) ([]byte, error) {
	if tableName == "" {
		tableName = "form_data"
	}
	var cols, vals []string
	i := 1
	for k := range data {
		cols = append(cols, k)
		vals = append(vals, fmt.Sprintf("$%d", i))
		i++
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);", tableName, strings.Join(cols, ", "), strings.Join(vals, ", "))
	return []byte(query), nil
}

type JSONConverter struct {
	Indent     string
	SortKeys   bool
	EscapeHTML bool
	TimeFormat string
}

func NewJSONConverter() *JSONConverter {
	return &JSONConverter{Indent: "  ", SortKeys: true, EscapeHTML: true, TimeFormat: "2006-01-02T15:04:05Z07:00"}
}

func (jc *JSONConverter) ToJSON(data interface{}) ([]byte, error) {
	if data == nil {
		return []byte("null"), nil
	}
	preparedData := data
	if jc.SortKeys {
		preparedData = jc.sortMapKeysRecursive(data)
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(jc.EscapeHTML)
	if jc.Indent != "" {
		encoder.SetIndent("", jc.Indent)
	}
	if err := encoder.Encode(preparedData); err != nil {
		return nil, fmt.Errorf("failed to encode JSON: %w", err)
	}
	result := buf.Bytes()
	if len(result) > 0 && result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}
	return result, nil
}

func (jc *JSONConverter) ToJSONCompact(data interface{}) ([]byte, error) {
	oldIndent := jc.Indent
	jc.Indent = ""
	defer func() { jc.Indent = oldIndent }()
	return jc.ToJSON(data)
}

func (jc *JSONConverter) ToJSONPretty(data interface{}, indent string) ([]byte, error) {
	oldIndent := jc.Indent
	jc.Indent = indent
	defer func() { jc.Indent = oldIndent }()
	return jc.ToJSON(data)
}

func (jc *JSONConverter) ToJSONWithTimeFormat(data interface{}, format string) ([]byte, error) {
	oldFormat := jc.TimeFormat
	jc.TimeFormat = format
	defer func() { jc.TimeFormat = oldFormat }()
	return jc.ToJSON(data)
}

func (jc *JSONConverter) UnmarshalJSONStrict(data []byte) (map[string]interface{}, error) {
	var raw interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}
	result, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("expected JSON object (map), got %T", raw)
	}
	return result, nil
}

func (jc *JSONConverter) UnmarshalJSONArray(data []byte) ([]map[string]interface{}, error) {
	var raw []interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON array: %w", err)
	}
	result := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]interface{}); ok {
			result = append(result, m)
		} else {
			return nil, fmt.Errorf("array element is not an object: %T", item)
		}
	}
	return result, nil
}

func ToJSONCompactSimple(data interface{}) ([]byte, error) {
	converter := NewJSONConverter()
	converter.Indent = ""
	return converter.ToJSON(data)
}

func (jc *JSONConverter) sortMapKeysRecursive(data interface{}) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		sorted := make(map[string]interface{})
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sorted[k] = jc.sortMapKeysRecursive(v[k])
		}
		return sorted
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = jc.sortMapKeysRecursive(item)
		}
		return result
	default:
		return v
	}
}

func (jc *JSONConverter) DecodeJSON(data []byte) (map[string]interface{}, error) {
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}
	return result, nil
}

func ToJSONSimple(data interface{}) ([]byte, error) {
	converter := NewJSONConverter()
	return converter.ToJSON(data)
}

type XMLConverter struct {
	RootElement           string
	IncludeXMLDeclaration bool
	Indent                string
	EscapeSpecialChars    bool
}

func NewXMLConverter() *XMLConverter {
	return &XMLConverter{RootElement: "root", IncludeXMLDeclaration: true, Indent: "  ", EscapeSpecialChars: true}
}

func (xc *XMLConverter) ToXML(data interface{}) ([]byte, error) {
	if data == nil {
		return []byte(fmt.Sprintf("<%s/>", xc.RootElement)), nil
	}
	var buf bytes.Buffer
	if xc.IncludeXMLDeclaration {
		buf.WriteString(xml.Header)
	}
	switch v := data.(type) {
	case map[string]interface{}:
		if err := xc.writeMap(&buf, v, xc.RootElement, 0); err != nil {
			return nil, err
		}
	case []map[string]interface{}:
		if err := xc.writeArray(&buf, v, xc.RootElement, 0); err != nil {
			return nil, err
		}
	default:
		if err := xml.NewEncoder(&buf).Encode(data); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (xc *XMLConverter) writeMap(buf *bytes.Buffer, data map[string]interface{}, parentTag string, indentLevel int) error {
	indent := strings.Repeat(xc.Indent, indentLevel)
	buf.WriteString(fmt.Sprintf("%s<%s>", indent, parentTag))
	if len(data) == 0 {
		buf.WriteString("/>\n")
		return nil
	}
	buf.WriteString("\n")
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := data[key]
		safeKey := sanitizeXMLName(key)
		if err := xc.writeValue(buf, safeKey, value, strings.Repeat(xc.Indent, indentLevel+1)); err != nil {
			return err
		}
	}
	buf.WriteString(fmt.Sprintf("%s</%s>\n", indent, parentTag))
	return nil
}

func (xc *XMLConverter) writeArray(buf *bytes.Buffer, data []map[string]interface{}, parentTag string, indentLevel int) error {
	itemTag := strings.TrimSuffix(parentTag, "s")
	if itemTag == parentTag {
		itemTag = "item"
	}
	buf.WriteString(fmt.Sprintf("%s<%s>\n", strings.Repeat(xc.Indent, indentLevel), parentTag))
	for _, item := range data {
		if err := xc.writeMap(buf, item, itemTag, indentLevel+1); err != nil {
			return err
		}
	}
	buf.WriteString(fmt.Sprintf("%s</%s>\n", strings.Repeat(xc.Indent, indentLevel), parentTag))
	return nil
}

func (xc *XMLConverter) writeSlice(buf *bytes.Buffer, data []interface{}, parentTag string, indentLevel int) error {
	indent := strings.Repeat(xc.Indent, indentLevel)
	nextIndent := strings.Repeat(xc.Indent, indentLevel+1)

	itemTag := strings.TrimSuffix(parentTag, "s")
	if itemTag == parentTag {
		itemTag = "item"
	}

	buf.WriteString(fmt.Sprintf("%s<%s>\n", indent, parentTag))

	for _, item := range data {
		if err := xc.writeValue(buf, itemTag, item, nextIndent); err != nil {
			return err
		}
	}

	buf.WriteString(fmt.Sprintf("%s</%s>\n", indent, parentTag))
	return nil
}

func (xc *XMLConverter) writeValue(buf *bytes.Buffer, key string, value interface{}, indent string) error {
	if value == nil {
		buf.WriteString(fmt.Sprintf("%s<%s/>\n", indent, key))
		return nil
	}

	valueStr := xc.valueToString(value)

	if xc.EscapeSpecialChars {
		valueStr = xmlEscape(valueStr)
	}

	if valueStr == "" {
		buf.WriteString(fmt.Sprintf("%s<%s/>\n", indent, key))
	} else {
		buf.WriteString(fmt.Sprintf("%s<%s>%s</%s>\n", indent, key, valueStr, key))
	}
	return nil
}

func (xc *XMLConverter) valueToString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return fmt.Sprintf("%t", v)
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%g", v)
	case []interface{}:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = xc.valueToString(item)
		}
		return strings.Join(parts, ", ")
	case map[string]interface{}:
		jsonBytes, _ := json.MarshalIndent(v, "", "")
		return string(jsonBytes)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func sanitizeXMLName(name string) string {
	if name == "" {
		return "empty_key"
	}
	var sb strings.Builder
	for i, r := range name {
		if i == 0 {
			if !isXMLNameStartChar(r) {
				sb.WriteRune('_')
			} else {
				sb.WriteRune(r)
			}
		} else {
			if isXMLNameChar(r) {
				sb.WriteRune(r)
			} else {
				sb.WriteRune('_')
			}
		}
	}
	return sb.String()
}

func isXMLNameStartChar(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		r == '_' ||
		r == ':'
}

func isXMLNameChar(r rune) bool {
	return isXMLNameStartChar(r) ||
		(r >= '0' && r <= '9') ||
		r == '-' ||
		r == '.'
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		case '"':
			buf.WriteString("&quot;")
		case '\'':
			buf.WriteString("&apos;")
		default:
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

func ToXMLSimple(data interface{}) ([]byte, error) {
	return NewXMLConverter().ToXML(data)
}

func ToXMLWithConfig(data interface{}, rootElement string, includeDeclaration bool) ([]byte, error) {
	converter := &XMLConverter{
		RootElement:           rootElement,
		IncludeXMLDeclaration: includeDeclaration,
		Indent:                "  ",
		EscapeSpecialChars:    true,
	}
	return converter.ToXML(data)
}

func (xc *XMLConverter) ToXMLPretty(data interface{}, indent string) ([]byte, error) {
	oldIndent := xc.Indent
	xc.Indent = indent
	defer func() { xc.Indent = oldIndent }()
	return xc.ToXML(data)
}

func (xc *XMLConverter) ToXMLCompact(data interface{}) ([]byte, error) {
	oldIndent := xc.Indent
	xc.Indent = ""
	defer func() { xc.Indent = oldIndent }()
	return xc.ToXML(data)
}

func (xc *XMLConverter) FromXML(xmlData []byte) (map[string]interface{}, error) {
	decoder := xml.NewDecoder(bytes.NewReader(xmlData))
	result := make(map[string]interface{})
	var stack []map[string]interface{}
	var currentKey string

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse XML: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				stack = append(stack, result)
			} else {
				parent := stack[len(stack)-1]
				child := make(map[string]interface{})
				if existing, ok := parent[t.Name.Local]; ok {
					if arr, ok := existing.([]interface{}); ok {
						parent[t.Name.Local] = append(arr, child)
					} else {
						parent[t.Name.Local] = []interface{}{existing, child}
					}
				} else {
					parent[t.Name.Local] = child
				}
				stack = append(stack, child)
			}
			currentKey = t.Name.Local
			for _, attr := range t.Attr {
				stack[len(stack)-1][attr.Name.Local] = attr.Value
			}

		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text != "" && len(stack) > 0 {
				current := stack[len(stack)-1]
				current[currentKey] = text
			}

		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return result, nil
}

type SQLConverter struct {
	Dialect          SQLDialect
	QuoteIdentifiers bool
	IdentifierQuote  string
	IncludeReturning bool
	BatchSize        int
}

func NewSQLConverter() *SQLConverter {
	return &SQLConverter{Dialect: PostgreSQL, QuoteIdentifiers: true, IdentifierQuote: "\"", IncludeReturning: true, BatchSize: 1}
}

func (sc *SQLConverter) ToSQL(data map[string]interface{}, tableName string) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cannot generate SQL for empty data")
	}
	if tableName == "" {
		tableName = "form_data"
	}
	return sc.GenerateInsert(data, tableName)
}

func (sc *SQLConverter) GenerateInsert(data map[string]interface{}, tableName string) ([]byte, error) {
	var stmt strings.Builder
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	stmt.WriteString("INSERT INTO ")
	stmt.WriteString(sc.quoteIdentifier(tableName))
	stmt.WriteString(" (")
	for i, key := range keys {
		if i > 0 {
			stmt.WriteString(", ")
		}
		stmt.WriteString(sc.quoteIdentifier(key))
	}
	stmt.WriteString(") VALUES (")
	for i := range keys {
		if i > 0 {
			stmt.WriteString(", ")
		}
		stmt.WriteString(sc.placeholder(i + 1))
	}
	stmt.WriteString(")")
	if sc.Dialect == PostgreSQL && sc.IncludeReturning {
		stmt.WriteString(" RETURNING *")
	}
	stmt.WriteString(";")
	return []byte(stmt.String()), nil
}

func (sc *SQLConverter) GenerateSelect(tableName string, conditions map[string]interface{}) ([]byte, error) {
	if tableName == "" {
		tableName = "form_data"
	}
	var stmt strings.Builder
	stmt.WriteString("SELECT * FROM ")
	stmt.WriteString(sc.quoteIdentifier(tableName))
	if len(conditions) > 0 {
		stmt.WriteString(" WHERE ")
		keys := make([]string, 0, len(conditions))
		for k := range conditions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, key := range keys {
			if i > 0 {
				stmt.WriteString(" AND ")
			}
			stmt.WriteString(sc.quoteIdentifier(key))
			stmt.WriteString(" = ")
			stmt.WriteString(sc.placeholder(i + 1))
		}
	}
	stmt.WriteString(";")
	return []byte(stmt.String()), nil
}

func (sc *SQLConverter) placeholder(index int) string {
	switch sc.Dialect {
	case PostgreSQL:
		return fmt.Sprintf("$%d", index)
	case MySQL, SQLite:
		return "?"
	case SQLServer:
		return fmt.Sprintf("@p%d", index)
	default:
		return fmt.Sprintf("$%d", index)
	}
}

func (sc *SQLConverter) quoteIdentifier(name string) string {
	if !sc.QuoteIdentifiers {
		return name
	}
	name = sanitizeIdentifier(name)
	if sc.Dialect == MySQL {
		return fmt.Sprintf("`%s`", name)
	}
	if sc.Dialect == SQLServer {
		return fmt.Sprintf("[%s]", name)
	}
	return fmt.Sprintf("%s%s%s", sc.IdentifierQuote, name, sc.IdentifierQuote)
}

func sanitizeIdentifier(name string) string {
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "'", "")
	name = strings.ReplaceAll(name, "`", "")
	name = strings.ReplaceAll(name, ";", "")
	name = strings.ReplaceAll(name, "--", "")
	name = strings.TrimSpace(name)
	return name
}

func (sc *SQLConverter) formatValue(value interface{}) string {
	if value == nil {
		return "NULL"
	}
	switch v := value.(type) {
	case string:
		escaped := strings.ReplaceAll(v, "'", "''")
		return fmt.Sprintf("'%s'", escaped)
	case bool:
		if v {
			return "TRUE"
		}
		return "FALSE"
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%g", v)
	case time.Time:
		return fmt.Sprintf("'%s'", v.Format("2006-01-02 15:04:05"))
	default:
		escaped := strings.ReplaceAll(fmt.Sprintf("%v", v), "'", "''")
		return fmt.Sprintf("'%s'", escaped)
	}
}

func (sc *SQLConverter) inferColumnType(value interface{}) string {
	if value == nil {
		return "TEXT"
	}
	switch value.(type) {
	case int, int8, int16, int32, int64:
		return "BIGINT"
	case uint, uint8, uint16, uint32, uint64:
		return "BIGINT"
	case float32:
		return "REAL"
	case float64:
		return "DOUBLE PRECISION"
	case bool:
		return "BOOLEAN"
	case time.Time:
		return "TIMESTAMP"
	case string:
		return "TEXT"
	default:
		return "TEXT"
	}
}

func (sc *SQLConverter) ExtractValues(data map[string]interface{}) []interface{} {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]interface{}, len(keys))
	for i, key := range keys {
		values[i] = data[key]
	}
	return values
}

func (sc *SQLConverter) ExtractValuesForUpdate(data map[string]interface{}, idField string) []interface{} {
	if idField == "" {
		idField = "id"
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		if k != idField {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	values := make([]interface{}, 0, len(keys)+1)
	for _, key := range keys {
		values = append(values, data[key])
	}
	if idValue, ok := data[idField]; ok {
		values = append(values, idValue)
	}
	return values
}

func (sc *SQLConverter) GenerateInsertWithValues(data map[string]interface{}, tableName string) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cannot generate SQL for empty data")
	}
	if tableName == "" {
		tableName = "form_data"
	}
	var stmt strings.Builder
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	stmt.WriteString("INSERT INTO ")
	stmt.WriteString(sc.quoteIdentifier(tableName))
	stmt.WriteString(" (")
	for i, key := range keys {
		if i > 0 {
			stmt.WriteString(", ")
		}
		stmt.WriteString(sc.quoteIdentifier(key))
	}
	stmt.WriteString(") VALUES (")
	for i, key := range keys {
		if i > 0 {
			stmt.WriteString(", ")
		}
		stmt.WriteString(sc.formatValue(data[key]))
	}
	stmt.WriteString(")")
	if sc.Dialect == PostgreSQL && sc.IncludeReturning {
		stmt.WriteString(" RETURNING *")
	}
	stmt.WriteString(";")
	return []byte(stmt.String()), nil
}

func (sc *SQLConverter) GenerateUpdate(data map[string]interface{}, tableName, idField string, idValue interface{}) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cannot generate UPDATE for empty data")
	}
	if tableName == "" {
		tableName = "form_data"
	}
	if idField == "" {
		idField = "id"
	}
	var stmt strings.Builder
	paramIndex := 1
	keys := make([]string, 0, len(data))
	for k := range data {
		if k == idField {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	stmt.WriteString("UPDATE ")
	stmt.WriteString(sc.quoteIdentifier(tableName))
	stmt.WriteString(" SET ")
	for i, key := range keys {
		if i > 0 {
			stmt.WriteString(", ")
		}
		stmt.WriteString(sc.quoteIdentifier(key))
		stmt.WriteString(" = ")
		stmt.WriteString(sc.placeholder(paramIndex))
		paramIndex++
	}
	stmt.WriteString(" WHERE ")
	stmt.WriteString(sc.quoteIdentifier(idField))
	stmt.WriteString(" = ")
	stmt.WriteString(sc.placeholder(paramIndex))
	stmt.WriteString(";")
	return []byte(stmt.String()), nil
}

func (sc *SQLConverter) GenerateDelete(tableName, idField string) ([]byte, error) {
	if tableName == "" {
		tableName = "form_data"
	}
	if idField == "" {
		idField = "id"
	}
	var stmt strings.Builder
	stmt.WriteString("DELETE FROM ")
	stmt.WriteString(sc.quoteIdentifier(tableName))
	stmt.WriteString(" WHERE ")
	stmt.WriteString(sc.quoteIdentifier(idField))
	stmt.WriteString(" = ")
	stmt.WriteString(sc.placeholder(1))
	stmt.WriteString(";")
	return []byte(stmt.String()), nil
}

func (sc *SQLConverter) GenerateCreateTable(tableName string, sampleData map[string]interface{}) ([]byte, error) {
	if tableName == "" {
		tableName = "form_data"
	}
	var stmt strings.Builder
	stmt.WriteString("CREATE TABLE IF NOT EXISTS ")
	stmt.WriteString(sc.quoteIdentifier(tableName))
	stmt.WriteString(" (\n")
	keys := make([]string, 0, len(sampleData))
	for k := range sampleData {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, key := range keys {
		stmt.WriteString("    ")
		stmt.WriteString(sc.quoteIdentifier(key))
		stmt.WriteString(" ")
		stmt.WriteString(sc.inferColumnType(sampleData[key]))
		if key == "id" {
			stmt.WriteString(" PRIMARY KEY")
		}
		if i < len(keys)-1 {
			stmt.WriteString(",")
		}
		stmt.WriteString("\n")
	}
	stmt.WriteString(");")
	return []byte(stmt.String()), nil
}

func (sc *SQLConverter) GenerateBatchInsert(data []map[string]interface{}, tableName string) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cannot generate batch INSERT for empty data")
	}
	if tableName == "" {
		tableName = "form_data"
	}
	firstRow := data[0]
	keys := make([]string, 0, len(firstRow))
	for k := range firstRow {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var stmt strings.Builder
	stmt.WriteString("INSERT INTO ")
	stmt.WriteString(sc.quoteIdentifier(tableName))
	stmt.WriteString(" (")
	for i, key := range keys {
		if i > 0 {
			stmt.WriteString(", ")
		}
		stmt.WriteString(sc.quoteIdentifier(key))
	}
	stmt.WriteString(") VALUES ")
	paramIndex := 1
	for rowIdx := range data {
		if rowIdx > 0 {
			stmt.WriteString(", ")
		}
		stmt.WriteString("(")
		for i := range keys {
			if i > 0 {
				stmt.WriteString(", ")
			}
			stmt.WriteString(sc.placeholder(paramIndex))
			paramIndex++
		}
		stmt.WriteString(")")
	}
	if sc.Dialect == PostgreSQL && sc.IncludeReturning {
		stmt.WriteString(" RETURNING *")
	}
	stmt.WriteString(";")
	return []byte(stmt.String()), nil
}

func ToSQLWithDialect(data map[string]interface{}, tableName string, dialect SQLDialect) ([]byte, error) {
	converter := &SQLConverter{
		Dialect:          dialect,
		QuoteIdentifiers: true,
		IdentifierQuote:  "\"",
		IncludeReturning: dialect == PostgreSQL,
		BatchSize:        1,
	}
	return converter.ToSQL(data, tableName)
}

func ToSQLSimple(data map[string]interface{}, tableName string) ([]byte, error) {
	converter := NewSQLConverter()
	return converter.ToSQL(data, tableName)
}
