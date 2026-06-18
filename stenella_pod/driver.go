package stenella_pod

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

type PODDriver struct{}

func (d *PODDriver) Open(name string) (driver.Conn, error) {
	store := NewStoreWithIndex(name)
	return &PODConn{store: store}, nil
}

type PODConn struct {
	store *PODStoreWithIndex
}

func (c *PODConn) Prepare(query string) (driver.Stmt, error) {
	args := strings.Count(query, "?")
	return &PODStmt{
		conn:    c,
		query:   query,
		numArgs: args,
	}, nil
}

func (c *PODConn) Close() error {
	return nil
}

func (c *PODConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported in POD V1")
}

func (c *PODConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	values := make([]driver.Value, len(args))
	for i, v := range args {
		values[i] = v.Value
	}
	return c.Exec(query, values)
}

func (c *PODConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	values := make([]driver.Value, len(args))
	for i, v := range args {
		values[i] = v.Value
	}
	return c.Query(query, values)
}

func (c *PODConn) Exec(query string, args []driver.Value) (driver.Result, error) {
	query = c.replacePlaceholders(query, args)
	cmd, err := c.parseSQL(query)
	if err != nil {
		return nil, err
	}
	c.store.LockOp()
	defer c.store.UnlockOp()
	db, err := c.store.loadDB()
	if err != nil {
		return nil, err
	}
	switch cmd.Operation {
	case "INSERT":
		return c.executeInsert(db, cmd)
	case "UPDATE":
		return c.executeUpdate(db, cmd)
	case "DELETE":
		return c.executeDelete(db, cmd)
	default:
		return nil, fmt.Errorf("operation %s does not return a result", cmd.Operation)
	}
}

func (c *PODConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	query = c.replacePlaceholders(query, args)
	cmd, err := c.parseSQL(query)
	if err != nil {
		return nil, err
	}
	if cmd.Operation != "SELECT" {
		return nil, fmt.Errorf("expected SELECT, got %s", cmd.Operation)
	}
	c.store.RLockOp()
	defer c.store.RUnlockOp()
	db, err := c.store.loadDB()
	if err != nil {
		return nil, err
	}
	return c.executeSelect(db, cmd)
}

type PODStmt struct {
	conn    *PODConn
	query   string
	numArgs int
}

func (s *PODStmt) Close() error {
	return nil
}

func (s *PODStmt) NumInput() int {
	return s.numArgs
}

func (s *PODStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.conn.Exec(s.query, args)
}

func (s *PODStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.conn.Query(s.query, args)
}

type PODResult struct {
	lastInsertID int64
	rowsAffected int64
}

func (r *PODResult) LastInsertId() (int64, error) {
	return r.lastInsertID, nil
}

func (r *PODResult) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}

type PODRows struct {
	records []Record
	columns []string
	cursor  int
}

func (r *PODRows) Columns() []string {
	return r.columns
}

func (r *PODRows) Next(dest []driver.Value) error {
	if r.cursor >= len(r.records) {
		return io.EOF
	}
	record := r.records[r.cursor]
	r.cursor++
	for i := range dest {
		if i < len(r.columns) {
			colName := r.columns[i]
			found := false
			for _, field := range record.Fields {
				if field.Name == colName {
					dest[i] = field.Value
					found = true
					break
				}
			}
			if !found {
				dest[i] = nil
			}
		} else {
			dest[i] = nil
		}
	}
	return nil
}

func (r *PODRows) Close() error {
	return nil
}

func (c *PODConn) replacePlaceholders(query string, args []driver.Value) string {
	result := query
	for _, arg := range args {
		valStr := fmt.Sprintf("%v", arg)
		valStr = strings.ReplaceAll(valStr, "'", "''")
		result = strings.Replace(result, "?", fmt.Sprintf("'%s'", valStr), 1)
	}
	return result
}

func (c *PODConn) parseSQL(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{}
	upperQuery := strings.ToUpper(query)
	query = strings.TrimSpace(query)

	if strings.HasPrefix(upperQuery, "SELECT") {
		cmd.Operation = "SELECT"
		return c.parseSelect(query)
	} else if strings.HasPrefix(upperQuery, "INSERT") {
		cmd.Operation = "INSERT"
		return c.parseInsert(query)
	} else if strings.HasPrefix(upperQuery, "UPDATE") {
		cmd.Operation = "UPDATE"
		return c.parseUpdate(query)
	} else if strings.HasPrefix(upperQuery, "DELETE") {
		cmd.Operation = "DELETE"
		return c.parseDelete(query)
	}
	return nil, fmt.Errorf("unsupported SQL operation: %s", query)
}

func (c *PODConn) parseSelect(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "SELECT"}
	selectPattern := regexp.MustCompile(`(?i)SELECT\s+(.+?)\s+FROM\s+(\w+)(?:\s+WHERE\s+(.+))?$`)
	matches := selectPattern.FindStringSubmatch(query)
	if len(matches) < 3 {
		return nil, fmt.Errorf("invalid SELECT syntax")
	}
	if matches[1] == "*" {
		cmd.Columns = []string{"*"}
	} else {
		cmd.Columns = strings.Split(matches[1], ",")
		for i := range cmd.Columns {
			cmd.Columns[i] = strings.TrimSpace(cmd.Columns[i])
		}
	}
	cmd.Table = matches[2]
	if len(matches) > 3 && matches[3] != "" {
		cmd.Where = matches[3]
	}
	return cmd, nil
}

func (c *PODConn) parseInsert(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "INSERT"}
	insertPattern := regexp.MustCompile(`(?i)INSERT\s+INTO\s+(\w+)\s*\(([^)]+)\)\s*VALUES\s*\(([^)]+)\)`)
	matches := insertPattern.FindStringSubmatch(query)
	if len(matches) < 4 {
		return nil, fmt.Errorf("invalid INSERT syntax")
	}
	cmd.Table = matches[1]
	cmd.Columns = strings.Split(matches[2], ",")
	for i := range cmd.Columns {
		cmd.Columns[i] = strings.TrimSpace(cmd.Columns[i])
	}
	rawValues := strings.Split(matches[3], ",")
	for _, v := range rawValues {
		v = strings.TrimSpace(v)
		v = strings.Trim(v, "'\"")
		cmd.Values = append(cmd.Values, v)
	}
	return cmd, nil
}

func (c *PODConn) parseUpdate(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "UPDATE"}
	updatePattern := regexp.MustCompile(`(?i)UPDATE\s+(\w+)\s+SET\s+(.+?)\s+WHERE\s+(.+)$`)
	matches := updatePattern.FindStringSubmatch(query)
	if len(matches) < 4 {
		return nil, fmt.Errorf("invalid UPDATE syntax")
	}
	cmd.Table = matches[1]
	cmd.Where = matches[3]
	cmd.SetPairs = make(map[string]string)
	pairs := strings.Split(matches[2], ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			key := strings.TrimSpace(kv[0])
			val := strings.TrimSpace(kv[1])
			val = strings.Trim(val, "'\"")
			cmd.SetPairs[key] = val
		}
	}
	return cmd, nil
}

func (c *PODConn) parseDelete(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "DELETE"}
	deletePattern := regexp.MustCompile(`(?i)DELETE\s+FROM\s+(\w+)\s+WHERE\s+(.+)$`)
	matches := deletePattern.FindStringSubmatch(query)
	if len(matches) < 3 {
		return nil, fmt.Errorf("invalid DELETE syntax")
	}
	cmd.Table = matches[1]
	cmd.Where = matches[2]
	return cmd, nil
}

func (c *PODConn) executeInsert(db *Database, cmd *SQLCommand) (driver.Result, error) {
	newID := c.store.getNextID(db)
	record := Record{ID: newID}
	for i, col := range cmd.Columns {
		var val string
		if i < len(cmd.Values) {
			val = cmd.Values[i]
		}
		record.Fields = append(record.Fields, Field{Name: col, Value: val})
	}
	db.Records = append(db.Records, record)
	c.store.updateIndexForInsert(record)
	if err := c.store.saveDB(db); err != nil {
		return nil, err
	}
	return &PODResult{lastInsertID: int64(newID), rowsAffected: 1}, nil
}

func (c *PODConn) executeUpdate(db *Database, cmd *SQLCommand) (driver.Result, error) {
	updatedCount := 0
	for i := range db.Records {
		if c.matchesWhere(db.Records[i], cmd.Where) {
			oldFields := make(map[string]string)
			for _, f := range db.Records[i].Fields {
				oldFields[f.Name] = f.Value
			}
			for j := range db.Records[i].Fields {
				if newVal, ok := cmd.SetPairs[db.Records[i].Fields[j].Name]; ok {
					db.Records[i].Fields[j].Value = newVal
					updatedCount++
				}
			}
			c.store.updateIndexForUpdate(db.Records[i], oldFields)
		}
	}
	if err := c.store.saveDB(db); err != nil {
		return nil, err
	}
	return &PODResult{rowsAffected: int64(updatedCount)}, nil
}

func (c *PODConn) executeDelete(db *Database, cmd *SQLCommand) (driver.Result, error) {
	deletedCount := 0
	for i := len(db.Records) - 1; i >= 0; i-- {
		if c.matchesWhere(db.Records[i], cmd.Where) {
			c.store.updateIndexForDelete(db.Records[i].ID)
			db.Records = append(db.Records[:i], db.Records[i+1:]...)
			deletedCount++
		}
	}
	if err := c.store.saveDB(db); err != nil {
		return nil, err
	}
	return &PODResult{rowsAffected: int64(deletedCount)}, nil
}

func (c *PODConn) executeSelect(db *Database, cmd *SQLCommand) (driver.Rows, error) {
	var filtered []Record
	for _, record := range db.Records {
		if cmd.Where == "" || c.matchesWhere(record, cmd.Where) {
			filtered = append(filtered, record)
		}
	}
	var columns []string
	if len(cmd.Columns) == 1 && cmd.Columns[0] == "*" {
		if len(filtered) > 0 {
			for _, field := range filtered[0].Fields {
				columns = append(columns, field.Name)
			}
			hasID := false
			for _, col := range columns {
				if col == "id" {
					hasID = true
					break
				}
			}
			if !hasID {
				columns = append([]string{"id"}, columns...)
			}
		} else {
			columns = []string{"id"}
		}
	} else {
		columns = cmd.Columns
	}
	return &PODRows{records: filtered, columns: columns, cursor: 0}, nil
}

func (c *PODConn) matchesWhere(record Record, whereClause string) bool {
	if whereClause == "" {
		return true
	}
	conditions := strings.Split(whereClause, "AND")
	for _, cond := range conditions {
		cond = strings.TrimSpace(cond)
		if cond == "" {
			continue
		}

		if strings.Contains(cond, ">=") || strings.Contains(cond, "<=") ||
			strings.Contains(cond, ">") || strings.Contains(cond, "<") {
			if !c.matchesComparison(record, cond) {
				return false
			}
			continue
		}

		parts := strings.SplitN(cond, "=", 2)
		if len(parts) != 2 {
			continue
		}
		fieldName := strings.TrimSpace(parts[0])
		expectedValue := strings.TrimSpace(parts[1])
		expectedValue = strings.Trim(expectedValue, "'\"")

		if ids := c.store.fastLookup(fieldName, expectedValue); ids != nil {
			found := false
			for _, id := range ids {
				if id == record.ID {
					found = true
					break
				}
			}
			if !found {
				return false
			}
			continue
		}

		found := false
		for _, field := range record.Fields {
			if field.Name == fieldName && field.Value == expectedValue {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (c *PODConn) matchesComparison(record Record, condition string) bool {
	var fieldName, operator, value string
	if strings.Contains(condition, ">=") {
		parts := strings.Split(condition, ">=")
		fieldName = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = ">="
	} else if strings.Contains(condition, "<=") {
		parts := strings.Split(condition, "<=")
		fieldName = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = "<="
	} else if strings.Contains(condition, ">") {
		parts := strings.Split(condition, ">")
		fieldName = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = ">"
	} else if strings.Contains(condition, "<") {
		parts := strings.Split(condition, "<")
		fieldName = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = "<"
	} else {
		return false
	}
	value = strings.Trim(value, "'\"")
	var fieldValue string
	for _, field := range record.Fields {
		if field.Name == fieldName {
			fieldValue = field.Value
			break
		}
	}
	if fieldValue == "" {
		return false
	}
	fieldNum, err1 := strconv.Atoi(fieldValue)
	valNum, err2 := strconv.Atoi(value)
	if err1 == nil && err2 == nil {
		switch operator {
		case ">":
			return fieldNum > valNum
		case "<":
			return fieldNum < valNum
		case ">=":
			return fieldNum >= valNum
		case "<=":
			return fieldNum <= valNum
		}
	}
	switch operator {
	case ">":
		return fieldValue > value
	case "<":
		return fieldValue < value
	case ">=":
		return fieldValue >= value
	case "<=":
		return fieldValue <= value
	}
	return false
}
