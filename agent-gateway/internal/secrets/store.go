// Package secrets provides a SQLite-backed AES-256-GCM secret store with
// master-key resolution via OS keychain (file fallback).
package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrNotFound is returned by Get when no matching secret exists.
var ErrNotFound = errors.New("secret not found")

// Metadata holds non-secret metadata about a stored secret.
type Metadata struct {
	ID          int64
	Name        string
	Scope       string
	CreatedAt   time.Time
	RotatedAt   time.Time
	LastUsedAt  *time.Time
	Description string
}

// Store manages encrypted secrets.
//
// The agent parameter is the agent name (e.g. "mybot") or empty string for the
// global scope. The store internally maps "" → "global" and "x" → "agent:x".
type Store interface {
	Get(ctx context.Context, name, agent string) (value string, scope string, err error)
	Set(ctx context.Context, name, agent, value, description string) error
	List(ctx context.Context) ([]Metadata, error)
	Rotate(ctx context.Context, name, agent, newValue string) error
	Delete(ctx context.Context, name, agent string) error
	MasterRotate(ctx context.Context) error
	InvalidateCache()
}

// agentToScope converts an agent name to a scope string.
// Empty agent means global scope; non-empty means "agent:<name>".
func agentToScope(agent string) string {
	if agent == "" {
		return "global"
	}
	return "agent:" + agent
}

// sqlStore is the production implementation of Store.
type sqlStore struct {
	db     *sql.DB
	key    []byte
	logger *slog.Logger
}

// NewStore creates a Store, resolving the master key via keychain / file fallback.
func NewStore(db *sql.DB, logger *slog.Logger) (Store, error) {
	key, _, err := resolveKey(logger)
	if err != nil {
		return nil, err
	}
	return &sqlStore{db: db, key: key, logger: logger}, nil
}

// NewStoreWithKey creates a Store using the provided key (for testing).
func NewStoreWithKey(db *sql.DB, logger *slog.Logger, key []byte) (Store, error) {
	if len(key) != 32 {
		return nil, errors.New("key must be 32 bytes")
	}
	cp := make([]byte, 32)
	copy(cp, key)
	return &sqlStore{db: db, key: cp, logger: logger}, nil
}

// Get retrieves the plaintext value of a secret using scope resolution:
// agent:<agent> wins over global; ErrNotFound if neither exists.
func (s *sqlStore) Get(ctx context.Context, name, agent string) (string, string, error) {
	const q = `
SELECT ciphertext, nonce, scope FROM secrets
WHERE name = ?1 AND scope IN ('global', 'agent:' || ?2)
ORDER BY scope = 'global' ASC
LIMIT 1`

	var ciphertext, nonce []byte
	var scope string
	err := s.db.QueryRowContext(ctx, q, name, agent).Scan(&ciphertext, &nonce, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}

	plaintext, err := decrypt(s.key, nonce, ciphertext)
	if err != nil {
		return "", "", err
	}
	return string(plaintext), scope, nil
}

// Set stores a new secret. agent is the agent name (empty → global scope).
func (s *sqlStore) Set(ctx context.Context, name, agent, value, description string) error {
	nonce, ciphertext, err := encrypt(s.key, []byte(value))
	if err != nil {
		return err
	}
	scope := agentToScope(agent)
	now := time.Now().Unix()
	const q = `
INSERT INTO secrets (name, scope, ciphertext, nonce, created_at, rotated_at, description)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, q, name, scope, ciphertext, nonce, now, now, description)
	return err
}

// ListNames returns the distinct set of secret names in db, in lexical
// order. It reads only the name column so it does not require the master
// key — callers that want to enumerate names without triggering keychain
// access (e.g. `rules check`) can use this directly against an open
// *sql.DB without constructing a Store.
func ListNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT name FROM secrets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// List returns metadata for all secrets (no plaintext).
func (s *sqlStore) List(ctx context.Context) ([]Metadata, error) {
	const q = `
SELECT id, name, scope, created_at, rotated_at, last_used_at, description
FROM secrets ORDER BY name, scope`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Metadata
	for rows.Next() {
		var m Metadata
		var createdUnix, rotatedUnix int64
		var lastUsedUnix *int64
		var desc *string
		if err := rows.Scan(&m.ID, &m.Name, &m.Scope, &createdUnix, &rotatedUnix, &lastUsedUnix, &desc); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(createdUnix, 0)
		m.RotatedAt = time.Unix(rotatedUnix, 0)
		if lastUsedUnix != nil {
			t := time.Unix(*lastUsedUnix, 0)
			m.LastUsedAt = &t
		}
		if desc != nil {
			m.Description = *desc
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Rotate updates the encrypted value for an existing secret and bumps rotated_at.
// agent is the agent name (empty → global scope).
func (s *sqlStore) Rotate(ctx context.Context, name, agent, newValue string) error {
	nonce, ciphertext, err := encrypt(s.key, []byte(newValue))
	if err != nil {
		return err
	}
	scope := agentToScope(agent)
	now := time.Now().Unix()
	const q = `UPDATE secrets SET ciphertext=?, nonce=?, rotated_at=? WHERE name=? AND scope=?`
	res, err := s.db.ExecContext(ctx, q, ciphertext, nonce, now, name, scope)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a secret by name and agent (empty → global scope).
func (s *sqlStore) Delete(ctx context.Context, name, agent string) error {
	scope := agentToScope(agent)
	const q = `DELETE FROM secrets WHERE name=? AND scope=?`
	res, err := s.db.ExecContext(ctx, q, name, scope)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MasterRotate generates a new key, re-encrypts every row in one transaction,
// then replaces the in-memory key. The new key is written to storage only after
// the transaction commits.
func (s *sqlStore) MasterRotate(ctx context.Context) error {
	newKey, err := generateKey()
	if err != nil {
		return fmt.Errorf("secrets: master-rotate: generate key: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("secrets: master-rotate: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT id, ciphertext, nonce FROM secrets`)
	if err != nil {
		return fmt.Errorf("secrets: master-rotate: query secrets: %w", err)
	}

	type row struct {
		id         int64
		ciphertext []byte
		nonce      []byte
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.ciphertext, &r.nonce); err != nil {
			_ = rows.Close()
			return fmt.Errorf("secrets: master-rotate: scan row: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("secrets: master-rotate: close rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("secrets: master-rotate: rows error: %w", err)
	}

	for _, r := range all {
		plain, err := decrypt(s.key, r.nonce, r.ciphertext)
		if err != nil {
			return fmt.Errorf("secrets: master-rotate: decrypt: %w", err)
		}
		newNonce, newCipher, err := encrypt(newKey, plain)
		if err != nil {
			return fmt.Errorf("secrets: master-rotate: encrypt: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE secrets SET ciphertext=?, nonce=? WHERE id=?`,
			newCipher, newNonce, r.id); err != nil {
			return fmt.Errorf("secrets: master-rotate: update row: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("secrets: master-rotate: commit: %w", err)
	}

	// Only after the TX commits do we update the in-memory key and persist it.
	s.key = newKey
	if err := persistKey(newKey, s.logger); err != nil {
		s.logger.Warn("master-rotate: failed to persist new key; in-memory key updated but storage not updated",
			"error", err)
	}
	return nil
}

// InvalidateCache is a no-op: the sqlStore holds no in-memory cache.
// The decrypted-secret cache lives on the injector, which invalidates itself
// on SIGHUP; this method exists so sqlStore satisfies interfaces that pair
// the store with that cache.
func (s *sqlStore) InvalidateCache() {}
