package stenella_pod

import (
	"fmt"
	"regexp"
	"strings"
)

type SQLCommand struct {
	Operation string
	Table     string
	Columns   []string
	Values    []string
	SetPairs  map[string]string
	Where     string
}

func ParseSQL(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{}
	upperQuery := strings.ToUpper(query)
	query = strings.TrimSpace(query)

	switch {
	case strings.HasPrefix(upperQuery, "SELECT"):
		cmd.Operation = "SELECT"
		return parseSelect(query)
	case strings.HasPrefix(upperQuery, "INSERT"):
		cmd.Operation = "INSERT"
		return parseInsert(query)
	case strings.HasPrefix(upperQuery, "UPDATE"):
		cmd.Operation = "UPDATE"
		return parseUpdate(query)
	case strings.HasPrefix(upperQuery, "DELETE"):
		cmd.Operation = "DELETE"
		return parseDelete(query)
	}
	return nil, fmt.Errorf("unsupported SQL operation: %s", query)
}

func parseSelect(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "SELECT"}
	re := regexp.MustCompile(`(?i)SELECT\s+(.+?)\s+FROM\s+(\w+)(?:\s+WHERE\s+(.+))?$`)
	matches := re.FindStringSubmatch(query)
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

func parseInsert(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "INSERT"}
	re := regexp.MustCompile(`(?i)INSERT\s+INTO\s+(\w+)\s*\(([^)]+)\)\s*VALUES\s*\(([^)]+)\)`)
	matches := re.FindStringSubmatch(query)
	if len(matches) < 4 {
		return nil, fmt.Errorf("invalid INSERT syntax")
	}
	cmd.Table = matches[1]
	cmd.Columns = strings.Split(matches[2], ",")
	for i := range cmd.Columns {
		cmd.Columns[i] = strings.TrimSpace(cmd.Columns[i])
	}
	cmd.Values = strings.Split(matches[3], ",")
	for i := range cmd.Values {
		cmd.Values[i] = strings.TrimSpace(cmd.Values[i])
		cmd.Values[i] = strings.Trim(cmd.Values[i], "'\"")
	}
	return cmd, nil
}

func parseUpdate(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "UPDATE"}
	re := regexp.MustCompile(`(?i)UPDATE\s+(\w+)\s+SET\s+(.+?)\s+WHERE\s+(.+)$`)
	matches := re.FindStringSubmatch(query)
	if len(matches) < 4 {
		return nil, fmt.Errorf("invalid UPDATE syntax: missing WHERE clause")
	}
	cmd.Table = matches[1]
	cmd.Where = matches[3]
	cmd.SetPairs = make(map[string]string)
	pairs := strings.Split(matches[2], ",")
	for _, p := range pairs {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, "'\"")
			cmd.SetPairs[key] = val
		}
	}
	return cmd, nil
}

func parseDelete(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "DELETE"}
	re := regexp.MustCompile(`(?i)DELETE\s+FROM\s+(\w+)\s+WHERE\s+(.+)$`)
	matches := re.FindStringSubmatch(query)
	if len(matches) < 3 {
		return nil, fmt.Errorf("invalid DELETE syntax: missing WHERE clause")
	}
	cmd.Table = matches[1]
	cmd.Where = matches[2]
	return cmd, nil
}

func ReplacePlaceholders(query string, args []interface{}) string {
	result := query
	for _, arg := range args {
		valStr := fmt.Sprintf("%v", arg)
		valStr = strings.ReplaceAll(valStr, "'", "''")
		result = strings.Replace(result, "?", fmt.Sprintf("'%s'", valStr), 1)
	}
	return result
}
