package store

import (
	"context"
	"database/sql"
	"errors"
)

var (
	ErrSessionNotFound     = errors.New("session not found")
	ErrAmbiguousSession    = errors.New("session prefix is ambiguous")
	ErrInvalidStreamCursor = errors.New("invalid stream cursor")
)

type Rows interface {
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
}

type Row interface {
	Scan(...any) error
}

type QueryFunc func(context.Context, string, ...any) (Rows, error)
type QueryRowFunc func(context.Context, string, ...any) Row

type queryAdapter struct {
	query    QueryFunc
	queryRow QueryRowFunc
}

func (q queryAdapter) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	return q.query(ctx, query, args...)
}

func (q queryAdapter) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return q.queryRow(ctx, query, args...)
}

// Reader exposes analyzer queries over a caller-owned database boundary.
type Reader struct {
	query queryAdapter
}

func NewReader(query QueryFunc, queryRow QueryRowFunc) *Reader {
	return &Reader{query: queryAdapter{query: query, queryRow: queryRow}}
}

func newSQLReader(db *sql.DB) *Reader {
	return NewReader(
		func(ctx context.Context, query string, args ...any) (Rows, error) {
			return db.QueryContext(ctx, query, args...)
		},
		func(ctx context.Context, query string, args ...any) Row {
			return db.QueryRowContext(ctx, query, args...)
		},
	)
}
