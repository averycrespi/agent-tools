package grants

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
)

const (
	tokenBytes       = 32
	idBytes          = 16
	fingerprintChars = 12
)

var (
	ErrUnknown = errors.New("unknown grant")
	ErrExpired = errors.New("expired grant")
	ErrRevoked = errors.New("revoked grant")
)

// Grant is persisted grant metadata. It never contains the raw token.
type Grant struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Fingerprint string              `json:"fingerprint"`
	Rules       []config.RuleConfig `json:"rules"`
	CreatedAt   time.Time           `json:"created_at"`
	ExpiresAt   time.Time           `json:"expires_at"`
	RevokedAt   *time.Time          `json:"revoked_at,omitempty"`
	Status      string              `json:"status"`
}

// MintOptions controls grant creation.
type MintOptions struct {
	Name        string
	Description string
	TTL         time.Duration
	Rules       []config.RuleConfig
	MaxTTL      time.Duration
	Now         time.Time
}

// MintedGrant returns the one-time token alongside hash-only grant metadata.
type MintedGrant struct {
	Grant Grant  `json:"grant"`
	Token string `json:"token"`
}

// Store is a durable SQLite grant store.
type Store struct {
	mu sync.Mutex
	db *sql.DB
}

const createSQL = `
CREATE TABLE IF NOT EXISTS grants (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    token_hash   TEXT NOT NULL UNIQUE,
    fingerprint  TEXT NOT NULL,
    rules_json   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    revoked_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_grants_fingerprint ON grants(fingerprint);
CREATE INDEX IF NOT EXISTS idx_grants_expires_at ON grants(expires_at);
`

// Open creates or opens a grants database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create grants dir: %w", err)
	}
	if err := ensurePrivateFile(path); err != nil {
		return nil, fmt.Errorf("create grants db file: %w", err)
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open grants db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	if _, err := db.Exec(createSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create grants table: %w", err)
	}
	if err := ensurePrivateSQLiteFiles(path); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set grants db permissions: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// ParseRulesFile parses grant rule input as either a bare rule array or {"rules":[...]}.
func ParseRulesFile(data []byte) ([]config.RuleConfig, error) {
	var rulesList []config.RuleConfig
	if err := json.Unmarshal(data, &rulesList); err == nil {
		return validateRules(rulesList)
	}

	var wrapped struct {
		Rules []config.RuleConfig `json:"rules"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, fmt.Errorf("parse grant rules: %w", err)
	}
	return validateRules(wrapped.Rules)
}

// Mint creates a durable grant and returns the raw token exactly once to the caller.
func (s *Store) Mint(ctx context.Context, opts MintOptions) (MintedGrant, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return MintedGrant{}, fmt.Errorf("grant name is required")
	}
	if opts.TTL <= 0 {
		return MintedGrant{}, fmt.Errorf("grant ttl must be positive")
	}
	if opts.MaxTTL > 0 && opts.TTL > opts.MaxTTL {
		return MintedGrant{}, fmt.Errorf("grant ttl %s exceeds maximum %s", opts.TTL, opts.MaxTTL)
	}
	checkedRules, err := validateRules(opts.Rules)
	if err != nil {
		return MintedGrant{}, err
	}

	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	token, tokenHash, fingerprint, err := newToken()
	if err != nil {
		return MintedGrant{}, err
	}
	id, err := randomHex(idBytes)
	if err != nil {
		return MintedGrant{}, err
	}
	rulesJSON, err := json.Marshal(checkedRules)
	if err != nil {
		return MintedGrant{}, fmt.Errorf("marshal grant rules: %w", err)
	}

	grant := Grant{
		ID:          id,
		Name:        name,
		Description: strings.TrimSpace(opts.Description),
		Fingerprint: fingerprint,
		Rules:       checkedRules,
		CreatedAt:   now,
		ExpiresAt:   now.Add(opts.TTL),
		Status:      "active",
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `INSERT INTO grants (id, name, description, token_hash, fingerprint, rules_json, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, grant.ID, grant.Name, grant.Description, tokenHash, grant.Fingerprint, string(rulesJSON), formatTime(grant.CreatedAt), formatTime(grant.ExpiresAt))
	if err != nil {
		return MintedGrant{}, fmt.Errorf("insert grant: %w", err)
	}
	return MintedGrant{Grant: grant, Token: token}, nil
}

// List returns retained active, expired, and revoked grants without raw tokens or hashes.
func (s *Store) List(ctx context.Context, now time.Time) ([]Grant, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, fingerprint, rules_json, created_at, expires_at, revoked_at FROM grants ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if out == nil {
		out = []Grant{}
	}
	return out, rows.Err()
}

// Revoke marks a grant revoked by exact ID or unambiguous fingerprint prefix. It is idempotent.
func (s *Store) Revoke(ctx context.Context, idOrFingerprint string, now time.Time) (Grant, error) {
	lookup := strings.TrimSpace(idOrFingerprint)
	if lookup == "" {
		return Grant{}, fmt.Errorf("grant id or fingerprint is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	ids, err := s.matchingIDs(ctx, lookup)
	if err != nil {
		return Grant{}, err
	}
	if len(ids) == 0 {
		return Grant{}, ErrUnknown
	}
	if len(ids) > 1 {
		return Grant{}, fmt.Errorf("ambiguous grant fingerprint %q", lookup)
	}

	_, err = s.db.ExecContext(ctx, `UPDATE grants SET revoked_at = CASE WHEN revoked_at = '' THEN ? ELSE revoked_at END WHERE id = ?`, formatTime(now), ids[0])
	if err != nil {
		return Grant{}, fmt.Errorf("revoke grant: %w", err)
	}
	return s.getByIDLocked(ctx, ids[0], now)
}

// ValidateToken returns grant metadata for an active token and fails closed for invalid states.
func (s *Store) ValidateToken(ctx context.Context, token string, now time.Time) (Grant, error) {
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, " \t\r\n,") {
		return Grant{}, ErrUnknown
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	hash := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRowContext(ctx, `SELECT id, name, description, fingerprint, rules_json, created_at, expires_at, revoked_at FROM grants WHERE token_hash = ?`, hash)
	g, err := scanGrant(row, now)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrUnknown
	}
	if err != nil {
		return Grant{}, err
	}
	if g.RevokedAt != nil {
		return g, ErrRevoked
	}
	if !now.Before(g.ExpiresAt) {
		return g, ErrExpired
	}
	return g, nil
}

// Engine compiles the grant rules for evaluation.
func (g Grant) Engine() (*rules.Engine, error) {
	return rules.New(g.Rules)
}

func validateRules(rs []config.RuleConfig) ([]config.RuleConfig, error) {
	if _, err := rules.New(rs); err != nil {
		return nil, fmt.Errorf("compile grant rules: %w", err)
	}
	data, err := json.Marshal(rs)
	if err != nil {
		return nil, fmt.Errorf("copy grant rules: %w", err)
	}
	var copied []config.RuleConfig
	if err := json.Unmarshal(data, &copied); err != nil {
		return nil, fmt.Errorf("copy grant rules: %w", err)
	}
	if copied == nil {
		copied = []config.RuleConfig{}
	}
	return copied, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanGrant(row scanner, now time.Time) (Grant, error) {
	var g Grant
	var rulesJSON, created, expires, revoked string
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &g.Fingerprint, &rulesJSON, &created, &expires, &revoked); err != nil {
		return Grant{}, err
	}
	if err := json.Unmarshal([]byte(rulesJSON), &g.Rules); err != nil {
		return Grant{}, fmt.Errorf("parse stored grant rules: %w", err)
	}
	var err error
	g.CreatedAt, err = parseTime(created)
	if err != nil {
		return Grant{}, fmt.Errorf("parse grant created_at: %w", err)
	}
	g.ExpiresAt, err = parseTime(expires)
	if err != nil {
		return Grant{}, fmt.Errorf("parse grant expires_at: %w", err)
	}
	if revoked != "" {
		t, err := parseTime(revoked)
		if err != nil {
			return Grant{}, fmt.Errorf("parse grant revoked_at: %w", err)
		}
		g.RevokedAt = &t
	}
	g.Status = statusFor(g, now)
	return g, nil
}

func statusFor(g Grant, now time.Time) string {
	if g.RevokedAt != nil {
		return "revoked"
	}
	if !now.Before(g.ExpiresAt) {
		return "expired"
	}
	return "active"
}

func (s *Store) matchingIDs(ctx context.Context, lookup string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM grants WHERE id = ? OR fingerprint = ? OR fingerprint LIKE ? ORDER BY id`, lookup, lookup, lookup+"%")
	if err != nil {
		return nil, fmt.Errorf("lookup grant: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan grant id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) getByIDLocked(ctx context.Context, id string, now time.Time) (Grant, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, description, fingerprint, rules_json, created_at, expires_at, revoked_at FROM grants WHERE id = ?`, id)
	return scanGrant(row, now)
}

func newToken() (token string, hash string, fingerprint string, err error) {
	token, err = randomHex(tokenBytes)
	if err != nil {
		return "", "", "", err
	}
	hash = hashToken(token)
	return token, hash, hash[:fingerprintChars], nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t.UTC(), nil
	}
	return time.Parse(time.RFC3339, s)
}

func ensurePrivateFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func ensurePrivateSQLiteFiles(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := os.Chmod(sidecar, 0o600); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
