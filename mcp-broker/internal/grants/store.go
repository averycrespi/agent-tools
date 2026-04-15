package grants

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
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

// Create persists g. tokenHash must be the SHA-256 of the raw token the
// caller will hand to the operator.
func (s *Store) Create(ctx context.Context, g Grant, tokenHash string) error {
	entriesJSON, err := json.Marshal(g.Entries)
	if err != nil {
		return fmt.Errorf("marshalling entries: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO grants (id, token_hash, description, entries, created_at, expires_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL)
	`, g.ID, tokenHash, g.Description, string(entriesJSON),
		g.CreatedAt.UnixMilli(), g.ExpiresAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("inserting grant: %w", err)
	}
	return nil
}

// LookupByTokenHash returns the grant with the given token_hash, or
// (nil, nil) if none exists.
func (s *Store) LookupByTokenHash(ctx context.Context, tokenHash string) (*Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRowContext(ctx, `
		SELECT id, description, entries, created_at, expires_at, revoked_at
		FROM grants WHERE token_hash = ?
	`, tokenHash)
	g, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

// LookupByID returns the grant with the given id, or (nil, nil) if none exists.
func (s *Store) LookupByID(ctx context.Context, id string) (*Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRowContext(ctx, `
		SELECT id, description, entries, created_at, expires_at, revoked_at
		FROM grants WHERE id = ?
	`, id)
	g, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

// List returns grants ordered by created_at DESC. When includeInactive is
// false, expired and revoked grants are omitted.
func (s *Store) List(ctx context.Context, includeInactive bool) ([]Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var (
		rows *sql.Rows
		err  error
	)
	if includeInactive {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, description, entries, created_at, expires_at, revoked_at
			FROM grants ORDER BY created_at DESC
		`)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, description, entries, created_at, expires_at, revoked_at
			FROM grants
			WHERE revoked_at IS NULL AND expires_at > ?
			ORDER BY created_at DESC
		`, time.Now().UnixMilli())
	}
	if err != nil {
		return nil, fmt.Errorf("querying grants: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// Revoke sets revoked_at for the given grant id. Idempotent: revoking an
// already-revoked grant is a no-op.
func (s *Store) Revoke(ctx context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		UPDATE grants SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL
	`, at.UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("revoking grant: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanGrant(r rowScanner) (*Grant, error) {
	var (
		g          Grant
		entriesRaw string
		createdMs  int64
		expiresMs  int64
		revokedMs  sql.NullInt64
	)
	if err := r.Scan(&g.ID, &g.Description, &entriesRaw, &createdMs, &expiresMs, &revokedMs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(entriesRaw), &g.Entries); err != nil {
		return nil, fmt.Errorf("unmarshalling entries: %w", err)
	}
	g.CreatedAt = time.UnixMilli(createdMs).UTC()
	g.ExpiresAt = time.UnixMilli(expiresMs).UTC()
	if revokedMs.Valid {
		t := time.UnixMilli(revokedMs.Int64).UTC()
		g.RevokedAt = &t
	}
	return &g, nil
}
