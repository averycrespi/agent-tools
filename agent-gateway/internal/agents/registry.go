package agents

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// ErrNotFound is returned when an agent is not found by name.
var ErrNotFound = errors.New("agent not found")

// ErrInvalidToken is returned when authentication fails.
var ErrInvalidToken = errors.New("invalid token")

// argon2id parameters (time=1, memory=64 KiB, threads=4, keyLen=32).
const (
	argon2Time    = 1
	argon2Memory  = 64 * 1024
	argon2Threads = 4
	argon2KeyLen  = 32
	saltLen       = 16
)

// Agent holds the public fields of a registered agent (no token bytes).
type Agent struct {
	Name        string
	CreatedAt   time.Time
	LastSeenAt  time.Time
	Description string
}

// AgentMetadata is a subset of Agent used for listing.
type AgentMetadata struct {
	Name        string
	TokenPrefix string
	CreatedAt   time.Time
	LastSeenAt  *time.Time
	Description string
}

// Registry manages agent identities.
type Registry interface {
	Add(ctx context.Context, name, description string) (token string, err error)
	Authenticate(ctx context.Context, token string) (*Agent, error)
	Rotate(ctx context.Context, name string) (newToken string, err error)
	Rm(ctx context.Context, name string) error
	List(ctx context.Context) ([]AgentMetadata, error)
	ReloadFromDB(ctx context.Context) error
}

// prefixEntry is an entry in the in-memory prefix→(hash,salt,name) map.
type prefixEntry struct {
	name string
	hash []byte
	salt []byte
}

// sqlRegistry is the production implementation of Registry.
type sqlRegistry struct {
	db *sql.DB

	mu        sync.RWMutex
	prefixMap map[string]prefixEntry // token_prefix → entry
}

// NewRegistry creates a Registry backed by the given database.
// The prefix map is loaded from the DB immediately.
func NewRegistry(ctx context.Context, db *sql.DB) (Registry, error) {
	r := &sqlRegistry{db: db}
	if err := r.ReloadFromDB(ctx); err != nil {
		return nil, fmt.Errorf("agents: initial load: %w", err)
	}
	return r, nil
}

// Add registers a new agent, mints a token, and returns it (shown once).
func (r *sqlRegistry) Add(ctx context.Context, name, description string) (string, error) {
	tok := MintToken()
	prefix := Prefix(tok)

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("agents: generate salt: %w", err)
	}

	hash := deriveHash(tok, salt)
	now := time.Now().Unix()

	const q = `
INSERT INTO agents (name, token_hash, token_prefix, argon2_salt, created_at, description)
VALUES (?, ?, ?, ?, ?, ?)`
	if _, err := r.db.ExecContext(ctx, q, name, hash, prefix, salt, now, description); err != nil {
		return "", fmt.Errorf("agents: insert: %w", err)
	}

	r.mu.Lock()
	r.prefixMap[prefix] = prefixEntry{name: name, hash: hash, salt: salt}
	r.mu.Unlock()

	return tok, nil
}

// Authenticate verifies a token by prefix lookup + argon2id comparison.
// On success it updates last_seen_at and returns the Agent.
func (r *sqlRegistry) Authenticate(ctx context.Context, token string) (*Agent, error) {
	prefix := Prefix(token)

	r.mu.RLock()
	entry, ok := r.prefixMap[prefix]
	r.mu.RUnlock()

	if !ok {
		return nil, ErrInvalidToken
	}

	candidate := deriveHash(token, entry.salt)
	if subtle.ConstantTimeCompare(candidate, entry.hash) != 1 {
		return nil, ErrInvalidToken
	}

	now := time.Now().Unix()
	const upd = `UPDATE agents SET last_seen_at = ? WHERE name = ?`
	if _, err := r.db.ExecContext(ctx, upd, now, entry.name); err != nil {
		return nil, fmt.Errorf("agents: update last_seen_at: %w", err)
	}

	const sel = `SELECT name, created_at, last_seen_at, description FROM agents WHERE name = ?`
	var a Agent
	var createdUnix int64
	var lastSeenUnix *int64
	var desc *string
	err := r.db.QueryRowContext(ctx, sel, entry.name).Scan(
		&a.Name, &createdUnix, &lastSeenUnix, &desc,
	)
	if err != nil {
		return nil, fmt.Errorf("agents: fetch agent: %w", err)
	}
	a.CreatedAt = time.Unix(createdUnix, 0)
	if lastSeenUnix != nil {
		a.LastSeenAt = time.Unix(*lastSeenUnix, 0)
	}
	if desc != nil {
		a.Description = *desc
	}

	return &a, nil
}

// Rotate replaces the token for an existing agent; invalidates the old prefix.
func (r *sqlRegistry) Rotate(ctx context.Context, name string) (string, error) {
	tok := MintToken()
	prefix := Prefix(tok)

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("agents: generate salt: %w", err)
	}
	hash := deriveHash(tok, salt)

	// Fetch the old prefix so we can remove it from the map.
	var oldPrefix string
	err := r.db.QueryRowContext(ctx, `SELECT token_prefix FROM agents WHERE name = ?`, name).Scan(&oldPrefix)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("agents: fetch prefix: %w", err)
	}

	const q = `UPDATE agents SET token_hash=?, token_prefix=?, argon2_salt=? WHERE name=?`
	res, err := r.db.ExecContext(ctx, q, hash, prefix, salt, name)
	if err != nil {
		return "", fmt.Errorf("agents: rotate: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", ErrNotFound
	}

	r.mu.Lock()
	delete(r.prefixMap, oldPrefix)
	r.prefixMap[prefix] = prefixEntry{name: name, hash: hash, salt: salt}
	r.mu.Unlock()

	return tok, nil
}

// Rm removes an agent and cascades to its scoped secrets in one transaction.
func (r *sqlRegistry) Rm(ctx context.Context, name string) error {
	// Fetch the prefix before deletion so we can clean up the map.
	var prefix string
	err := r.db.QueryRowContext(ctx, `SELECT token_prefix FROM agents WHERE name = ?`, name).Scan(&prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("agents: fetch prefix: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("agents: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE scope = 'agent:' || ?`, name); err != nil {
		return fmt.Errorf("agents: cascade secrets: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("agents: delete agent: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("agents: commit: %w", err)
	}

	r.mu.Lock()
	delete(r.prefixMap, prefix)
	r.mu.Unlock()

	return nil
}

// List returns metadata for all agents (no token bytes).
func (r *sqlRegistry) List(ctx context.Context) ([]AgentMetadata, error) {
	const q = `
SELECT name, token_prefix, created_at, last_seen_at, description
FROM agents ORDER BY name`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []AgentMetadata
	for rows.Next() {
		var m AgentMetadata
		var createdUnix int64
		var lastSeenUnix *int64
		var desc *string
		if err := rows.Scan(&m.Name, &m.TokenPrefix, &createdUnix, &lastSeenUnix, &desc); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(createdUnix, 0)
		if lastSeenUnix != nil {
			t := time.Unix(*lastSeenUnix, 0)
			m.LastSeenAt = &t
		}
		if desc != nil {
			m.Description = *desc
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReloadFromDB re-populates the in-memory prefix map from the database.
// Called on startup and on SIGHUP.
func (r *sqlRegistry) ReloadFromDB(ctx context.Context) error {
	const q = `SELECT name, token_prefix, token_hash, argon2_salt FROM agents`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("agents: reload: %w", err)
	}
	defer func() { _ = rows.Close() }()

	m := make(map[string]prefixEntry)
	for rows.Next() {
		var name, prefix string
		var hash, salt []byte
		if err := rows.Scan(&name, &prefix, &hash, &salt); err != nil {
			return fmt.Errorf("agents: reload scan: %w", err)
		}
		m[prefix] = prefixEntry{name: name, hash: hash, salt: salt}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("agents: reload rows: %w", err)
	}

	r.mu.Lock()
	r.prefixMap = m
	r.mu.Unlock()
	return nil
}

// deriveHash runs argon2id over the token + salt with the standard parameters.
func deriveHash(token string, salt []byte) []byte {
	return argon2.IDKey([]byte(token), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
}
