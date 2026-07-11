package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlite3driver "github.com/ncruces/go-sqlite3/driver"
)

const maxSQLRows = 1024
const maxSQLValueBytes = 64 * 1024
const sqlTimeout = 5 * time.Second

var forbiddenSQL = regexp.MustCompile(`(?i)\b(?:insert|update|delete|replace|create|drop|alter|vacuum|attach|detach|pragma|reindex|analyze|transaction|begin|commit|rollback)\b`)

type QueryResult struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	Truncated bool             `json:"truncated"`
}

// RunSelect validates and executes one bounded SELECT or CTE.
func RunSelect(ctx context.Context, path, query string) (QueryResult, error) {
	query = strings.TrimSpace(query)
	query = strings.TrimSuffix(query, ";")
	code := sqlCode(query)
	if strings.TrimSpace(code) == "" || hasStatementSeparator(code) {
		return QueryResult{}, fmt.Errorf("query must contain exactly one statement")
	}
	first := strings.ToLower(strings.Fields(code)[0])
	if first != "select" && first != "with" {
		return QueryResult{}, fmt.Errorf("query must be a SELECT or CTE")
	}
	if forbiddenSQL.MatchString(code) {
		return QueryResult{}, fmt.Errorf("query contains a forbidden operation")
	}
	result, err := executeReadOnly(ctx, path, "SELECT * FROM (\n"+query+"\n) LIMIT 1025")
	if err != nil {
		return QueryResult{}, err
	}
	if len(result.Rows) > maxSQLRows {
		result.Rows = result.Rows[:maxSQLRows]
		result.Truncated = true
	}
	return result, nil
}

func executeReadOnly(ctx context.Context, path, query string) (QueryResult, error) {
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return QueryResult{}, fmt.Errorf("open read-only database: %w", err)
	}
	defer func() { _ = db.Close() }()
	queryCtx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	conn, err := db.Conn(queryCtx)
	if err != nil {
		return QueryResult{}, fmt.Errorf("open read-only connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err = conn.Raw(func(driverConn any) error {
		raw, ok := driverConn.(sqlite3driver.Conn)
		if !ok {
			return fmt.Errorf("unexpected SQLite driver connection %T", driverConn)
		}
		raw.Raw().Limit(sqlite3.LIMIT_LENGTH, maxSQLValueBytes)
		return nil
	}); err != nil {
		return QueryResult{}, fmt.Errorf("limit SQLite values: %w", err)
	}
	if _, err = conn.ExecContext(queryCtx, "PRAGMA query_only=ON"); err != nil {
		return QueryResult{}, fmt.Errorf("enable query-only mode: %w", err)
	}
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

func hasStatementSeparator(query string) bool {
	var quote rune
	escaped := false
	for _, r := range query {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quote != 0 {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			quote = r
			continue
		}
		if r == ';' {
			return true
		}
	}
	return false
}
