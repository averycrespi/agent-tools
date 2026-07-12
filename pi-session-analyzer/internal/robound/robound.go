// Package robound provides the analyzer's shared bounded read-only database and JSON boundary.
package robound

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlite3driver "github.com/ncruces/go-sqlite3/driver"
)

const (
	MaxResponseBytes = 50000
	maxSQLValueBytes = 64 * 1024
	maxSQLColumns    = 32
	queryTimeout     = 5 * time.Second
)

type Conn struct {
	db   *sql.DB
	conn *sql.Conn
	mu   sync.Mutex
}

type Rows struct {
	rows    *sql.Rows
	release sync.Once
	unlock  func()
}

func (r *Rows) Next() bool {
	next := r.rows.Next()
	if !next {
		r.release.Do(r.unlock)
	}
	return next
}

func (r *Rows) Scan(dest ...any) error { return r.rows.Scan(dest...) }

func (r *Rows) Columns() ([]string, error) { return r.rows.Columns() }

func (r *Rows) Close() error {
	err := r.rows.Close()
	r.release.Do(r.unlock)
	return err
}

func (r *Rows) Err() error { return r.rows.Err() }

type Row struct {
	row     *sql.Row
	release sync.Once
	unlock  func()
}

func (r *Row) Scan(dest ...any) error {
	defer r.release.Do(r.unlock)
	return r.row.Scan(dest...)
}

func Open(ctx context.Context, path string) (*Conn, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("database does not exist: %s; run pi-session-analyzer ingest first", path)
		}
		return nil, fmt.Errorf("inspect database: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open read-only database: %w", err)
	}
	openCtx, cancel := WithTimeout(ctx)
	defer cancel()
	conn, err := db.Conn(openCtx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open read-only connection: %w", err)
	}
	if err = conn.Raw(func(driverConn any) error {
		raw, ok := driverConn.(sqlite3driver.Conn)
		if !ok {
			return fmt.Errorf("unexpected SQLite driver connection %T", driverConn)
		}
		raw.Raw().Limit(sqlite3.LIMIT_LENGTH, maxSQLValueBytes)
		raw.Raw().Limit(sqlite3.LIMIT_COLUMN, maxSQLColumns)
		return nil
	}); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("limit SQLite values: %w", err)
	}
	if _, err = conn.ExecContext(openCtx, "PRAGMA query_only=ON"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("enable query-only mode: %w", err)
	}
	return &Conn{db: db, conn: conn}, nil
}

func WithTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, queryTimeout)
}

func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return errors.Join(c.conn.Close(), c.db.Close())
}

func (c *Conn) QueryContext(ctx context.Context, query string, args ...any) (*Rows, error) {
	c.mu.Lock()
	rows, err := c.conn.QueryContext(ctx, query, args...)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	return &Rows{rows: rows, unlock: c.mu.Unlock}, nil
}

func (c *Conn) QueryRowContext(ctx context.Context, query string, args ...any) *Row {
	c.mu.Lock()
	return &Row{row: c.conn.QueryRowContext(ctx, query, args...), unlock: c.mu.Unlock}
}

func (c *Conn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.ExecContext(ctx, query, args...)
}

func MarshalCapped(value any) string {
	budget := MaxResponseBytes / 2
	bounded, truncated := boundedJSONValue(reflect.ValueOf(value), &budget)
	if truncated {
		bounded = map[string]any{"truncated": true, "value": bounded}
	}
	data, err := json.Marshal(bounded)
	if err != nil {
		return `{"error":"response serialization failed"}`
	}
	if len(data) > MaxResponseBytes {
		return `{"truncated":true}`
	}
	return string(data)
}

func boundedJSONValue(value reflect.Value, budget *int) (any, bool) {
	if !value.IsValid() || ((value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) && value.IsNil()) {
		return nil, false
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		return boundedJSONValue(value.Elem(), budget)
	}
	if *budget <= 0 {
		return nil, true
	}
	switch value.Kind() {
	case reflect.String:
		text := value.String()
		if len(text) > *budget {
			text = text[:*budget]
			*budget = 0
			return text, true
		}
		*budget -= len(text)
		return text, false
	case reflect.Bool:
		*budget -= 5
		return value.Bool(), false
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		*budget -= 20
		return value.Int(), false
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		*budget -= 20
		return value.Uint(), false
	case reflect.Float32, reflect.Float64:
		*budget -= 24
		return value.Float(), false
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			return boundedJSONValue(reflect.ValueOf(string(value.Bytes())), budget)
		}
		out := make([]any, 0, min(value.Len(), *budget/8+1))
		truncated := false
		for i := 0; i < value.Len(); i++ {
			if *budget <= 0 {
				truncated = true
				break
			}
			*budget -= 2
			item, cut := boundedJSONValue(value.Index(i), budget)
			out = append(out, item)
			truncated = truncated || cut
		}
		return out, truncated
	case reflect.Map:
		out := map[string]any{}
		truncated := false
		for _, key := range value.MapKeys() {
			if key.Kind() != reflect.String || *budget <= 0 {
				truncated = true
				break
			}
			name := key.String()
			*budget -= len(name) + 4
			item, cut := boundedJSONValue(value.MapIndex(key), budget)
			out[name] = item
			truncated = truncated || cut
		}
		return out, truncated
	case reflect.Struct:
		out := map[string]any{}
		truncated := false
		typeInfo := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typeInfo.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if *budget <= 0 {
				truncated = true
				break
			}
			*budget -= len(name) + 4
			item, cut := boundedJSONValue(value.Field(i), budget)
			out[name] = item
			truncated = truncated || cut
		}
		return out, truncated
	default:
		*budget -= 16
		return fmt.Sprint(value.Interface()), false
	}
}
