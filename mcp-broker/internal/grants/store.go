package grants

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// Store persists grants in SQLite.
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewStore opens or creates the grants table in db.
func NewStore(ctx context.Context, db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.init(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

const grantsSchema = `
CREATE TABLE IF NOT EXISTS grants (
	id          TEXT PRIMARY KEY,
	token_hash  TEXT NOT NULL UNIQUE,
	description TEXT,
	entries     TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL,
	revoked_at  INTEGER
);
CREATE INDEX IF NOT EXISTS idx_grants_token_hash ON grants(token_hash);
CREATE INDEX IF NOT EXISTS idx_grants_expires_at ON grants(expires_at);
`

func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, grantsSchema); err != nil {
		return fmt.Errorf("initializing grants schema: %w", err)
	}
	return nil
}
