// Package secrets provides a SQLite-backed AES-256-GCM secret store with
// master-key resolution via OS keychain (file fallback).
package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/hostmatch"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/hostnorm"
)

// ErrNotFound is returned by Get when no matching secret exists.
var ErrNotFound = errors.New("secret not found")

// ErrNoAllowedHosts is returned by Set/Bind/Unbind when the operation would
// leave a secret with an empty allowed_hosts list. Every secret must be
// explicitly bound to at least one host glob (use "**" for the explicit
// all-hosts opt-in).
var ErrNoAllowedHosts = errors.New("secret requires at least one allowed host")

// ErrDuplicate is returned by Set when a secret with the given (name, scope)
// already exists.
var ErrDuplicate = errors.New("secret already exists")

// Metadata holds non-secret metadata about a stored secret.
type Metadata struct {
	ID           int64
	Name         string
	Scope        string
	AllowedHosts []string
	CreatedAt    time.Time
	RotatedAt    time.Time
	LastUsedAt   *time.Time
	Description  string
}

// Store manages encrypted secrets.
//
// The agent parameter is the agent name (e.g. "mybot") or empty string for the
// global scope. The store internally maps "" → "global" and "x" → "agent:x".
//
// Every row carries a non-empty allowed_hosts list of normalised host globs
// that gate where the secret may be injected. Callers (the inject layer) are
// responsible for enforcing the host check — the store surfaces the list on
// Get so the caller can assert "this request's host matches one of these
// globs" before expanding a ${secrets.X} reference.
type Store interface {
	Get(ctx context.Context, name, agent string) (value string, scope string, allowedHosts []string, err error)
	Set(ctx context.Context, name, agent, value, description string, allowedHosts []string) error
	List(ctx context.Context) ([]Metadata, error)
	Rotate(ctx context.Context, name, agent, newValue string) error
	Delete(ctx context.Context, name, agent string) error
	Bind(ctx context.Context, name, agent string, hosts []string) error
	Unbind(ctx context.Context, name, agent string, hosts []string) error
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
//
// The store holds three pieces of key material in memory:
//
//   - key:     the master key resolved from the OS keychain or file fallback.
//     Used only to derive the KEK (on open and on master rotate); never
//     directly encrypts or decrypts a row.
//   - kekSalt: the per-DB salt for HKDF over the master key. Stable for the
//     lifetime of a DEK; rewritten only on master rotate.
//   - dek:     the 32-byte data-encryption key that actually encrypts and
//     decrypts every secrets row (AES-256-GCM with per-row AAD). Unwrapped
//     from meta.dek_wrapped using the KEK derived from (key, kekSalt).
type sqlStore struct {
	db      *sql.DB
	key     []byte
	kekSalt []byte
	dek     []byte
	keyID   int
	logger  *slog.Logger
}

// NewStore creates a Store, reading the active master-key id from the meta
// table and resolving the corresponding key via keychain / file fallback.
// On first open after the schema-8 migration lands, NewStore also runs the
// one-shot migrateToDEK pass that re-encrypts every row under the DEK+AAD
// format.
func NewStore(db *sql.DB, logger *slog.Logger) (Store, error) {
	ctx := context.Background()
	id, err := readActiveKeyID(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("secrets: read active key id: %w", err)
	}
	key, _, err := ResolveID(id, logger)
	if err != nil {
		return nil, err
	}
	s := &sqlStore{db: db, key: key, keyID: id, logger: logger}
	if err := s.ensureDEK(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// NewStoreWithKey creates a Store using the provided key (for testing). The
// store assumes id=1 — tests using this helper must not depend on the key
// being persisted under a particular keychain account.
func NewStoreWithKey(db *sql.DB, logger *slog.Logger, key []byte) (Store, error) {
	if len(key) != 32 {
		return nil, errors.New("key must be 32 bytes")
	}
	cp := make([]byte, 32)
	copy(cp, key)
	s := &sqlStore{db: db, key: cp, keyID: 1, logger: logger}
	if err := s.ensureDEK(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// ensureDEK loads the DEK into the store, migrating from the pre-DEK on-disk
// format if necessary. On return, s.dek and s.kekSalt are populated.
//
// Sentinel: meta.dek_wrapped IS NULL means the DB has never had a DEK and
// existing rows (if any) are encrypted directly under the master key (no
// AAD). We run migrateToDEK to convert them in a single transaction.
// meta.dek_wrapped IS NOT NULL means the DEK has already been established;
// we unwrap it and return.
func (s *sqlStore) ensureDEK(ctx context.Context) error {
	var dekWrapped, dekNonce, kekSalt []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT dek_wrapped, dek_nonce, kek_kdf_salt FROM meta WHERE key = 'active_key_id'`,
	).Scan(&dekWrapped, &dekNonce, &kekSalt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("secrets: read meta dek material: %w", err)
	}

	if len(dekWrapped) > 0 {
		// Already established: unwrap the DEK into memory.
		kek, err := deriveKEK(s.key, kekSalt)
		if err != nil {
			return fmt.Errorf("secrets: derive kek: %w", err)
		}
		dek, err := unwrapDEK(kek, dekWrapped, dekNonce)
		if err != nil {
			return fmt.Errorf("secrets: unwrap dek: %w", err)
		}
		s.kekSalt = kekSalt
		s.dek = dek
		return nil
	}

	// dek_wrapped IS NULL: first open under the new schema. Generate a DEK,
	// re-encrypt every existing row under it with AAD, and write the new
	// meta material — all in a single transaction so a crash mid-migration
	// leaves the pre-migration state intact (dek_wrapped NULL → next start
	// retries).
	return s.migrateToDEK(ctx)
}

// migrateToDEK generates fresh DEK + KEK-salt, re-encrypts every secrets row
// from the old (master-key-direct, no-AAD) format into the new (DEK, AAD =
// name||0x00||scope) format, and persists the wrapped DEK + salt on the
// active_key_id meta row. The whole sequence runs in one transaction.
//
// Invariant: on any error, the transaction rolls back and meta.dek_wrapped
// stays NULL, so the next NewStore call retries from the old format — no
// half-migrated state is ever observable. The single transaction is what
// makes this safe; splitting it would leave a window where meta says "DEK
// present" but some rows still hold old-format ciphertext.
func (s *sqlStore) migrateToDEK(ctx context.Context) error {
	dek, err := generateKey()
	if err != nil {
		return fmt.Errorf("secrets: migrate: generate dek: %w", err)
	}
	kekSalt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, kekSalt); err != nil {
		return fmt.Errorf("secrets: migrate: generate kek salt: %w", err)
	}
	kek, err := deriveKEK(s.key, kekSalt)
	if err != nil {
		return fmt.Errorf("secrets: migrate: derive kek: %w", err)
	}
	dekWrapped, dekNonce, err := wrapDEK(kek, dek)
	if err != nil {
		return fmt.Errorf("secrets: migrate: wrap dek: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("secrets: migrate: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT id, name, scope, ciphertext, nonce FROM secrets`)
	if err != nil {
		return fmt.Errorf("secrets: migrate: query secrets: %w", err)
	}
	type row struct {
		id         int64
		name       string
		scope      string
		ciphertext []byte
		nonce      []byte
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name, &r.scope, &r.ciphertext, &r.nonce); err != nil {
			_ = rows.Close()
			return fmt.Errorf("secrets: migrate: scan row: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("secrets: migrate: close rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("secrets: migrate: rows err: %w", err)
	}

	for _, r := range all {
		plaintext, err := decrypt(s.key, r.nonce, r.ciphertext)
		if err != nil {
			return fmt.Errorf("secrets: migrate: decrypt row %d (%s/%s): %w", r.id, r.name, r.scope, err)
		}
		newCT, newNonce, err := encryptRow(dek, r.name, r.scope, plaintext)
		if err != nil {
			return fmt.Errorf("secrets: migrate: encrypt row %d: %w", r.id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE secrets SET ciphertext = ?, nonce = ? WHERE id = ?`,
			newCT, newNonce, r.id,
		); err != nil {
			return fmt.Errorf("secrets: migrate: update row %d: %w", r.id, err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE meta SET dek_wrapped = ?, dek_nonce = ?, kek_kdf_salt = ? WHERE key = 'active_key_id'`,
		dekWrapped, dekNonce, kekSalt,
	); err != nil {
		return fmt.Errorf("secrets: migrate: update meta: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("secrets: migrate: commit: %w", err)
	}

	s.kekSalt = kekSalt
	s.dek = dek
	s.logger.Info("secrets: migrated to dek+aad format", "rows", len(all))
	return nil
}

// readActiveKeyID reads meta.active_key_id. Returns 1 (the seed value) if the
// row is missing — defensive against migrations being out-of-sync with code.
func readActiveKeyID(ctx context.Context, db *sql.DB) (int, error) {
	var s string
	err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'active_key_id'`).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	var id int
	if _, err := fmt.Sscanf(s, "%d", &id); err != nil {
		return 0, fmt.Errorf("parse active_key_id %q: %w", s, err)
	}
	if id < 1 {
		return 0, fmt.Errorf("invalid active_key_id %d", id)
	}
	return id, nil
}

// Get retrieves the plaintext value of a secret using scope resolution:
// agent:<agent> wins over global; ErrNotFound if neither exists. The
// allowedHosts slice contains the normalised host globs the caller must
// check the request host against before injecting.
func (s *sqlStore) Get(ctx context.Context, name, agent string) (string, string, []string, error) {
	const q = `
SELECT ciphertext, nonce, scope, allowed_hosts FROM secrets
WHERE name = ?1 AND scope IN ('global', 'agent:' || ?2)
ORDER BY scope = 'global' ASC
LIMIT 1`

	var ciphertext, nonce []byte
	var scope, allowedHostsJSON string
	err := s.db.QueryRowContext(ctx, q, name, agent).Scan(&ciphertext, &nonce, &scope, &allowedHostsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil, ErrNotFound
	}
	if err != nil {
		return "", "", nil, err
	}

	// Decrypt under the DEK, binding AAD to this row's (name, scope). A
	// tampered-or-swapped ciphertext (e.g. a DB-write-capable attacker copying
	// bytes from another row) surfaces as a decrypt error here, not as wrong
	// plaintext silently returned to the caller.
	plaintext, err := decryptRow(s.dek, name, scope, ciphertext, nonce)
	if err != nil {
		return "", "", nil, err
	}
	allowedHosts, err := decodeAllowedHosts(allowedHostsJSON)
	if err != nil {
		return "", "", nil, fmt.Errorf("decode allowed_hosts for %q: %w", name, err)
	}
	return string(plaintext), scope, allowedHosts, nil
}

// Set stores a new secret. agent is the agent name (empty → global scope).
// allowedHosts must contain at least one glob; patterns are normalised via
// hostnorm.NormalizeGlob and de-duplicated in insertion order.
//
// Returns ErrDuplicate if a secret with the same (name, scope) already exists.
func (s *sqlStore) Set(ctx context.Context, name, agent, value, description string, allowedHosts []string) error {
	cleaned, err := sanitizeAllowedHosts(allowedHosts)
	if err != nil {
		return err
	}
	scope := agentToScope(agent)

	var existing string
	switch err := s.db.QueryRowContext(ctx, `SELECT name FROM secrets WHERE name=? AND scope=?`, name, scope).Scan(&existing); {
	case err == nil:
		return ErrDuplicate
	case errors.Is(err, sql.ErrNoRows):
		// Not a duplicate; proceed to insert.
	default:
		return fmt.Errorf("secrets: check duplicate: %w", err)
	}

	ciphertext, nonce, err := encryptRow(s.dek, name, scope, []byte(value))
	if err != nil {
		return err
	}
	encoded, err := encodeAllowedHosts(cleaned)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	const q = `
INSERT INTO secrets (name, scope, ciphertext, nonce, created_at, rotated_at, description, allowed_hosts)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, q, name, scope, ciphertext, nonce, now, now, description, encoded)
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
SELECT id, name, scope, created_at, rotated_at, last_used_at, description, allowed_hosts
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
		var allowedHostsJSON string
		if err := rows.Scan(&m.ID, &m.Name, &m.Scope, &createdUnix, &rotatedUnix, &lastUsedUnix, &desc, &allowedHostsJSON); err != nil {
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
		hosts, err := decodeAllowedHosts(allowedHostsJSON)
		if err != nil {
			return nil, fmt.Errorf("decode allowed_hosts for %q: %w", m.Name, err)
		}
		m.AllowedHosts = hosts
		out = append(out, m)
	}
	return out, rows.Err()
}

// Bind adds hosts to the secret's allowed_hosts list. Duplicates (post-
// normalization) are silently ignored. ErrNotFound is returned when no
// (name, scope) row matches.
func (s *sqlStore) Bind(ctx context.Context, name, agent string, hosts []string) error {
	if len(hosts) == 0 {
		return fmt.Errorf("bind: %w", ErrNoAllowedHosts)
	}
	additions, err := sanitizeAllowedHosts(hosts)
	if err != nil {
		return err
	}
	return s.mutateAllowedHosts(ctx, name, agent, func(existing []string) ([]string, error) {
		return mergeHosts(existing, additions), nil
	})
}

// Unbind removes hosts from the secret's allowed_hosts list. Hosts are
// normalised before comparison. Returns ErrNoAllowedHosts when the removal
// would leave the list empty — callers must rebind or rm the secret.
func (s *sqlStore) Unbind(ctx context.Context, name, agent string, hosts []string) error {
	if len(hosts) == 0 {
		return fmt.Errorf("unbind: at least one host required")
	}
	removals, err := sanitizeAllowedHosts(hosts)
	if err != nil {
		return err
	}
	return s.mutateAllowedHosts(ctx, name, agent, func(existing []string) ([]string, error) {
		next := subtractHosts(existing, removals)
		if len(next) == 0 {
			return nil, ErrNoAllowedHosts
		}
		return next, nil
	})
}

// mutateAllowedHosts reads the current allowed_hosts for (name, scope),
// applies fn, and writes the result back. Runs in a single transaction so
// concurrent bind/unbind cannot race.
func (s *sqlStore) mutateAllowedHosts(ctx context.Context, name, agent string, fn func([]string) ([]string, error)) error {
	scope := agentToScope(agent)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var encoded string
	err = tx.QueryRowContext(ctx,
		`SELECT allowed_hosts FROM secrets WHERE name=? AND scope=?`,
		name, scope,
	).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	existing, err := decodeAllowedHosts(encoded)
	if err != nil {
		return err
	}
	next, err := fn(existing)
	if err != nil {
		return err
	}
	nextEncoded, err := encodeAllowedHosts(next)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE secrets SET allowed_hosts=? WHERE name=? AND scope=?`,
		nextEncoded, name, scope,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// sanitizeAllowedHosts normalises each glob via hostnorm.NormalizeGlob,
// rejects empty input and wildcard-only patterns (matching the policy used
// for config no_intercept_hosts), and returns a de-duplicated slice in
// first-seen order.
func sanitizeAllowedHosts(hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		return nil, ErrNoAllowedHosts
	}
	seen := make(map[string]struct{}, len(hosts))
	out := make([]string, 0, len(hosts))
	for i, h := range hosts {
		trimmed := h
		if trimmed == "" {
			return nil, fmt.Errorf("allowed_hosts[%d]: pattern is empty", i)
		}
		// Reject wildcard-only entries with the same rule as no_intercept_hosts:
		// a pattern of only '*' and '.' characters is a "match everything"
		// footgun. To bind a secret to every host, pass "**" explicitly.
		// "**" has a literal character count > 0 under this rule because we
		// count non-{*,.} runes; we special-case it below.
		if trimmed == "**" {
			out = append(out, "**")
			seen["**"] = struct{}{}
			continue
		}
		literalCount := 0
		for _, r := range trimmed {
			if r != '*' && r != '.' {
				literalCount++
			}
		}
		if literalCount == 0 {
			return nil, fmt.Errorf(
				"allowed_hosts[%d]: pattern %q matches every (or nearly every) host; use \"**\" for explicit all-hosts binding",
				i, h,
			)
		}
		normalized, err := hostnorm.NormalizeGlob(trimmed)
		if err != nil {
			return nil, fmt.Errorf("allowed_hosts[%d]: %w", i, err)
		}
		// Reject patterns whose stripped form is an ICANN public suffix
		// (e.g. "*.com", "*.co", "*.io"). allowed_hosts is the credential-
		// scoping layer — unlike no_intercept_hosts where an over-broad
		// pattern means "more MITM" (a warning is enough), a too-broad
		// allowed_hosts would route real secrets to every host under a
		// registry-controlled TLD. That's a security bug, not a config
		// convenience, so we reject outright and name the offending
		// pattern so the operator can fix it. Operators who genuinely
		// want a secret usable everywhere can pass "**" explicitly.
		if ok, suffix := hostmatch.MatchesPublicSuffix(normalized); ok {
			return nil, fmt.Errorf(
				"allowed_hosts[%d]: pattern %q strips to public suffix %q; refusing to bind secret to every host under %q (use \"**\" for explicit all-hosts binding, or narrow to a specific domain)",
				i, h, suffix, suffix,
			)
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

// mergeHosts returns the union of existing and additions, preserving
// existing order and appending new entries in addition order.
func mergeHosts(existing, additions []string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, h := range existing {
		seen[h] = struct{}{}
	}
	out := append([]string(nil), existing...)
	for _, h := range additions {
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

// subtractHosts returns existing with every entry in removals deleted.
func subtractHosts(existing, removals []string) []string {
	remove := make(map[string]struct{}, len(removals))
	for _, h := range removals {
		remove[h] = struct{}{}
	}
	out := make([]string, 0, len(existing))
	for _, h := range existing {
		if _, drop := remove[h]; drop {
			continue
		}
		out = append(out, h)
	}
	return out
}

// encodeAllowedHosts marshals hosts to a stable JSON array, sorted for
// stable disk representation. Empty hosts is rejected so the physical
// default of '[]' can never slip into a valid row.
func encodeAllowedHosts(hosts []string) (string, error) {
	if len(hosts) == 0 {
		return "", ErrNoAllowedHosts
	}
	// Preserve caller order — callers deduplicate and sort when they mean to.
	b, err := json.Marshal(hosts)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeAllowedHosts unmarshals the JSON array stored in the allowed_hosts
// column. An empty or absent list produces ErrNoAllowedHosts so callers can
// distinguish legitimate rows from the migration-only DEFAULT '[]'.
func decodeAllowedHosts(encoded string) ([]string, error) {
	if encoded == "" {
		return nil, ErrNoAllowedHosts
	}
	var hosts []string
	if err := json.Unmarshal([]byte(encoded), &hosts); err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, ErrNoAllowedHosts
	}
	return hosts, nil
}

// HostScopeAllows reports whether host (already hostnorm-normalised) matches
// any pattern in allowedHosts. Callers that need to enforce the host scope
// of an injection should call this before expanding a ${secrets.X} value.
//
// This helper lives here so that every caller uses one matching
// implementation. The pattern set is small per secret so linear scan is fine.
func HostScopeAllows(allowedHosts []string, host string) bool {
	for _, pat := range allowedHosts {
		if hostnorm.MatchHostGlob(pat, host) {
			return true
		}
	}
	return false
}

// Rotate updates the encrypted value for an existing secret and bumps rotated_at.
// agent is the agent name (empty → global scope).
func (s *sqlStore) Rotate(ctx context.Context, name, agent, newValue string) error {
	scope := agentToScope(agent)
	ciphertext, nonce, err := encryptRow(s.dek, name, scope, []byte(newValue))
	if err != nil {
		return err
	}
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

// MasterRotate generates a new master key, persists it under a fresh id, and
// rewraps the DEK under the new master key (via a freshly-salted KEK) inside
// a single transaction that also bumps meta.active_key_id. Row ciphertexts
// are NOT touched — the DEK is unchanged, so every encryptRow output from
// before the rotation still decrypts afterwards. Rotation cost is O(1) in
// the number of rows.
//
// Crash safety: the new key is persisted to storage BEFORE the meta update
// commits (see PersistID). A crash between persist and commit leaves an
// orphan key on disk (harmless; best-effort cleaned up below), but
// meta.active_key_id still names a key that can derive the KEK that unwraps
// the DEK. A crash after commit has the new key persisted and meta pointing
// at it — the old DEK wrap blob is gone, which is fine because the new one
// is what decrypts the (unchanged) rows. At no point is the on-disk state
// unrecoverable.
func (s *sqlStore) MasterRotate(ctx context.Context) error {
	oldID := s.keyID
	newID := oldID + 1

	newKey, err := generateKey()
	if err != nil {
		return fmt.Errorf("secrets: master-rotate: generate key: %w", err)
	}

	if err := PersistID(newKey, newID, s.logger); err != nil {
		return fmt.Errorf("secrets: master-rotate: persist new key: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			DeleteID(newID, s.logger)
		}
	}()

	// Fresh KEK salt on every rotation: even if the operator rotated back to
	// a previous master key value (implausible, but cheap to defend against),
	// the derived KEK would differ from the previous rotation's.
	newKEKSalt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, newKEKSalt); err != nil {
		return fmt.Errorf("secrets: master-rotate: generate kek salt: %w", err)
	}
	newKEK, err := deriveKEK(newKey, newKEKSalt)
	if err != nil {
		return fmt.Errorf("secrets: master-rotate: derive kek: %w", err)
	}
	newDEKWrapped, newDEKNonce, err := wrapDEK(newKEK, s.dek)
	if err != nil {
		return fmt.Errorf("secrets: master-rotate: wrap dek: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("secrets: master-rotate: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = ?, dek_wrapped = ?, dek_nonce = ?, kek_kdf_salt = ? WHERE key = 'active_key_id'`,
		fmt.Sprintf("%d", newID), newDEKWrapped, newDEKNonce, newKEKSalt,
	); err != nil {
		return fmt.Errorf("secrets: master-rotate: update meta: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("secrets: master-rotate: commit: %w", err)
	}
	committed = true

	s.key = newKey
	s.keyID = newID
	s.kekSalt = newKEKSalt
	// s.dek is unchanged — that is the whole point of rewrap-only rotation.

	// Best-effort cleanup of the previous key. Failures are logged inside
	// DeleteID and never propagated — an orphan is harmless and we never want
	// a successful rotation to surface an error to the caller.
	DeleteID(oldID, s.logger)
	return nil
}

// InvalidateCache is a no-op: the sqlStore holds no in-memory cache.
// The decrypted-secret cache lives on the injector, which invalidates itself
// on SIGHUP; this method exists so sqlStore satisfies interfaces that pair
// the store with that cache.
func (s *sqlStore) InvalidateCache() {}
