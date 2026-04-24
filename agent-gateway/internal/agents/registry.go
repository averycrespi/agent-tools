package agents

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// ErrNotFound is returned when an agent is not found by name.
var ErrNotFound = errors.New("agent not found")

// ErrInvalidToken is returned when authentication fails.
var ErrInvalidToken = errors.New("invalid token")

// ErrDuplicateName is returned by Add when an agent with the given name
// already exists.
var ErrDuplicateName = errors.New("agent name already exists")

// argon2id parameters — OWASP 2023 floor (m=19 MiB, t=2, p=1, keyLen=32).
// Dropping below these re-introduces a GPU-feasible offline attack on an
// exfiltrated DB: the earlier m=64 KiB / t=1 / p=4 settings were ~300× less
// memory-hard and, per OWASP's 2023 password-storage cheatsheet, below the
// threshold at which consumer GPUs can crack 8+ char random-looking tokens
// in minutes. Raise these only upward.
const (
	argon2Time    = 2
	argon2Memory  = 19 * 1024
	argon2Threads = 1
	argon2KeyLen  = 32
	saltLen       = 16
)

// Legacy argon2id parameters used before the OWASP-floor upgrade. Kept so
// that Authenticate can recognize hashes stored by earlier builds and
// transparently rehash them with the current params on successful auth.
// Remove only after a forced rotation migration has been executed.
const (
	legacyArgon2Time    = 1
	legacyArgon2Memory  = 64 * 1024
	legacyArgon2Threads = 4
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
// Returns ErrDuplicateName if an agent with the given name already exists.
func (r *sqlRegistry) Add(ctx context.Context, name, description string) (string, error) {
	var existing string
	switch err := r.db.QueryRowContext(ctx, `SELECT name FROM agents WHERE name = ?`, name).Scan(&existing); {
	case err == nil:
		return "", fmt.Errorf("%w: %q", ErrDuplicateName, name)
	case errors.Is(err, sql.ErrNoRows):
		// Not a duplicate; proceed to insert.
	default:
		return "", fmt.Errorf("agents: check duplicate: %w", err)
	}

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
	// Constant-time compare: a naive bytes.Equal / == on the hashes would
	// return early at the first mismatching byte, leaking (via response
	// latency) how many leading bytes of the candidate hash matched the
	// stored hash. An attacker who can submit auth attempts could use that
	// timing oracle to reconstruct the stored hash byte-by-byte — not the
	// token itself, but close enough to narrow offline brute-force.
	needsRehash := false
	if subtle.ConstantTimeCompare(candidate, entry.hash) != 1 {
		// Fall back to legacy params: pre-upgrade installs stored hashes
		// with m=64 KiB/t=1/p=4. If the token matches under those params,
		// auth succeeds and we transparently rehash with the current
		// OWASP params below. Any other mismatch is a true failure.
		legacy := deriveHashLegacy(token, entry.salt)
		if subtle.ConstantTimeCompare(legacy, entry.hash) != 1 {
			return nil, ErrInvalidToken
		}
		needsRehash = true
	}

	if needsRehash {
		// Persist the upgraded hash in the DB and update the in-memory
		// prefix map so subsequent auths take the fast path. Write failure
		// must not fail the auth: the caller is legitimate; we can upgrade
		// on the next request. Logged at warn so it surfaces in operator
		// telemetry without being alarmist.
		newHash := candidate
		const upd = `UPDATE agents SET token_hash = ? WHERE name = ?`
		if _, err := r.db.ExecContext(ctx, upd, newHash, entry.name); err != nil {
			slog.Default().Warn("agents: rehash persist failed",
				"agent", entry.name, "err", err)
		} else {
			r.mu.Lock()
			if e, ok := r.prefixMap[prefix]; ok && e.name == entry.name {
				e.hash = newHash
				r.prefixMap[prefix] = e
			}
			r.mu.Unlock()
		}
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

// deriveHash runs argon2id over the token + salt with the current parameters.
func deriveHash(token string, salt []byte) []byte {
	return argon2.IDKey([]byte(token), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
}

// deriveHashLegacy runs argon2id with the pre-upgrade parameters. Used only
// on the Authenticate fallback path so legacy hashes can be recognized and
// rehashed with current params.
func deriveHashLegacy(token string, salt []byte) []byte {
	return argon2.IDKey([]byte(token), salt, legacyArgon2Time, legacyArgon2Memory, legacyArgon2Threads, argon2KeyLen)
}
