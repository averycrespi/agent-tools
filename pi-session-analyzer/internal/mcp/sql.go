package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

const maxSQLRows = 1024
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
	if query == "" || hasStatementSeparator(query) {
		return QueryResult{}, fmt.Errorf("query must contain exactly one statement")
	}
	first := strings.ToLower(strings.Fields(query)[0])
	if first != "select" && first != "with" {
		return QueryResult{}, fmt.Errorf("query must be a SELECT or CTE")
	}
	if forbiddenSQL.MatchString(query) {
		return QueryResult{}, fmt.Errorf("query contains a forbidden operation")
	}
	result, err := executeReadOnly(ctx, path, `SELECT * FROM (`+query+`) LIMIT 1025`)
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
	if _, err = db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return QueryResult{}, fmt.Errorf("enable query-only mode: %w", err)
	}
	queryCtx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()
	rows, err := db.QueryContext(queryCtx, query)
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
