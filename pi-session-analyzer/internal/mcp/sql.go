package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/robound"
)

const maxSQLRows = 1024

var forbiddenSQL = regexp.MustCompile(`(?i)\b(?:insert|update|delete|replace|create|drop|alter|vacuum|attach|detach|pragma|reindex|analyze|transaction|begin|commit|rollback)\b`)

type QueryResult struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	Truncated bool             `json:"truncated"`
}

// RunSelect validates and executes one bounded SELECT or CTE.
func RunSelect(ctx context.Context, conn *robound.Conn, query string) (QueryResult, error) {
	query = strings.TrimSpace(query)
	query = strings.TrimSuffix(query, ";")
	code := sqlCode(query)
	if strings.TrimSpace(code) == "" || strings.Contains(code, ";") {
		return QueryResult{}, fmt.Errorf("query must contain exactly one statement")
	}
	first := strings.ToLower(strings.Fields(code)[0])
	if first != "select" && first != "with" {
		return QueryResult{}, fmt.Errorf("query must be a SELECT or CTE")
	}
	if forbiddenSQL.MatchString(code) {
		return QueryResult{}, fmt.Errorf("query contains a forbidden operation")
	}
	result, err := executeReadOnly(ctx, conn, "SELECT * FROM (\n"+query+"\n) LIMIT 1025")
	if err != nil {
		return QueryResult{}, err
	}
	if len(result.Rows) > maxSQLRows {
		result.Rows = result.Rows[:maxSQLRows]
		result.Truncated = true
	}
	return result, nil
}

func executeReadOnly(ctx context.Context, conn *robound.Conn, query string) (QueryResult, error) {
	queryCtx, cancel := robound.WithTimeout(ctx)
	defer cancel()
	rows, err := conn.QueryContext(queryCtx, query)
	if err != nil {
		return QueryResult{}, fmt.Errorf("execute query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return QueryResult{}, err
	}
	result := QueryResult{Columns: columns, Rows: []map[string]any{}}
	resultBytes := 0
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return QueryResult{}, err
		}
		row := map[string]any{}
		for i, column := range columns {
			if data, ok := values[i].([]byte); ok {
				row[column] = string(data)
			} else {
				row[column] = values[i]
			}
		}
		rowBytes := 2
		for key, value := range row {
			rowBytes += len(key) + 4
			switch typed := value.(type) {
			case string:
				rowBytes += len(typed) + 2
			case []byte:
				rowBytes += len(typed) + 2
			default:
				rowBytes += 32
			}
		}
		if resultBytes+rowBytes > robound.MaxResponseBytes {
			result.Truncated = true
			break
		}
		resultBytes += rowBytes
		result.Rows = append(result.Rows, row)
	}
	return result, rows.Err()
}

func sqlCode(query string) string {
	var out strings.Builder
	for i := 0; i < len(query); {
		switch {
		case i+1 < len(query) && query[i:i+2] == "--":
			for i < len(query) && query[i] != '\n' {
				i++
			}
			out.WriteByte(' ')
		case i+1 < len(query) && query[i:i+2] == "/*":
			i += 2
			for i+1 < len(query) && query[i:i+2] != "*/" {
				i++
			}
			if i+1 < len(query) {
				i += 2
			}
			out.WriteByte(' ')
		case query[i] == '\'' || query[i] == '"' || query[i] == '`':
			quote := query[i]
			i++
			for i < len(query) {
				if query[i] == quote {
					if i+1 < len(query) && query[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			out.WriteByte(' ')
		default:
			out.WriteByte(query[i])
			i++
		}
	}
	return out.String()
}
