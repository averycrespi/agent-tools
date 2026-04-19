package secrets_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openTestDB opens a temporary SQLite database with migrations applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newTestStore creates a Store with a freshly-generated in-memory key.
func newTestStore(t *testing.T) secrets.Store {
	t.Helper()
	db := openTestDB(t)
	key := make([]byte, 32)
	// Use a fixed key for deterministic tests.
	for i := range key {
		key[i] = byte(i + 1)
	}
	s, err := secrets.NewStoreWithKey(db, slog.Default(), key)
	require.NoError(t, err)
	return s
}

func TestStore_SetThenGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.Set(ctx, "gh_bot", "", "token-value", "GitHub bot token")
	require.NoError(t, err)

	val, scope, err := s.Get(ctx, "gh_bot", "any-agent")
	require.NoError(t, err)
	assert.Equal(t, "token-value", val)
	assert.Equal(t, "global", scope)
}

func TestStore_ScopeResolution(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Set a global secret.
	require.NoError(t, s.Set(ctx, "gh_bot", "", "global-value", "global"))
	// Set an agent-scoped secret for "foo".
	require.NoError(t, s.Set(ctx, "gh_bot", "foo", "foo-value", "foo scoped"))

	// agent "foo" gets the agent-scoped value.
	val, scope, err := s.Get(ctx, "gh_bot", "foo")
	require.NoError(t, err)
	assert.Equal(t, "foo-value", val)
	assert.Equal(t, "agent:foo", scope)

	// agent "bar" falls back to the global value.
	val, scope, err = s.Get(ctx, "gh_bot", "bar")
	require.NoError(t, err)
	assert.Equal(t, "global-value", val)
	assert.Equal(t, "global", scope)

	// Delete the global secret; now agent "baz" gets ErrNotFound.
	require.NoError(t, s.Delete(ctx, "gh_bot", ""))
	_, _, err = s.Get(ctx, "gh_bot", "baz")
	assert.True(t, errors.Is(err, secrets.ErrNotFound), "expected ErrNotFound, got %v", err)
}

func TestStore_EncryptionAtRest(t *testing.T) {
	db := openTestDB(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	s, err := secrets.NewStoreWithKey(db, slog.Default(), key)
	require.NoError(t, err)

	ctx := context.Background()
	plaintext := "super-secret-token-value-12345"
	require.NoError(t, s.Set(ctx, "mykey", "", plaintext, ""))

	// Read the raw ciphertext column directly.
	var ciphertext []byte
	err = db.QueryRowContext(ctx, "SELECT ciphertext FROM secrets WHERE name = 'mykey'").Scan(&ciphertext)
	require.NoError(t, err)

	// The raw column must NOT contain the plaintext.
	assert.False(t, bytes.Contains(ciphertext, []byte(plaintext)),
		"ciphertext column contains plaintext — encryption is broken")

	// Also check the nonce column exists and is non-empty.
	var nonce []byte
	err = db.QueryRowContext(ctx, "SELECT nonce FROM secrets WHERE name = 'mykey'").Scan(&nonce)
	require.NoError(t, err)
	assert.Len(t, nonce, 12, "nonce must be 12 bytes")
}

func TestStore_MasterRotate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db := openTestDB(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	s, err := secrets.NewStoreWithKey(db, slog.Default(), key)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, s.Set(ctx, "secret1", "", "value-one", ""))
	require.NoError(t, s.Set(ctx, "secret2", "bar", "value-two", ""))

	// Rotate master key.
	require.NoError(t, s.MasterRotate(ctx))

	// Both secrets should still decrypt correctly.
	val1, _, err := s.Get(ctx, "secret1", "any")
	require.NoError(t, err)
	assert.Equal(t, "value-one", val1)

	val2, _, err := s.Get(ctx, "secret2", "bar")
	require.NoError(t, err)
	assert.Equal(t, "value-two", val2)

	// meta.active_key_id should advance to 2.
	var idStr string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='active_key_id'`).Scan(&idStr))
	assert.Equal(t, "2", idStr)
}

// TestStore_RotateThenReopen verifies the full crash-safe rotation path: after
// rotating, closing, and re-opening the database, NewStore must read the new
// active key id from meta and decrypt rows under the new key. This is the
// recovery-from-restart scenario that the old "commit then persist" ordering
// could permanently brick.
func TestStore_RotateThenReopen(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)

	// First boot: fresh store, set secrets, rotate.
	s1, err := secrets.NewStore(db, slog.Default())
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, s1.Set(ctx, "alpha", "", "alpha-value", ""))
	require.NoError(t, s1.Set(ctx, "beta", "agent1", "beta-value", ""))
	require.NoError(t, s1.MasterRotate(ctx))
	require.NoError(t, db.Close())

	// Second boot: reopen, NewStore must resolve active_key_id=2 and decrypt.
	db2, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = db2.Close() }()
	s2, err := secrets.NewStore(db2, slog.Default())
	require.NoError(t, err)

	val, _, err := s2.Get(ctx, "alpha", "")
	require.NoError(t, err)
	assert.Equal(t, "alpha-value", val)

	val, _, err = s2.Get(ctx, "beta", "agent1")
	require.NoError(t, err)
	assert.Equal(t, "beta-value", val)

	// A second rotation should advance to id=3 and still decrypt.
	require.NoError(t, s2.MasterRotate(ctx))
	val, _, err = s2.Get(ctx, "alpha", "")
	require.NoError(t, err)
	assert.Equal(t, "alpha-value", val)
}

// TestStore_RotateOrphanCleanupOnFailure verifies that if the re-encryption
// transaction fails (here: a row whose ciphertext cannot be decrypted), the
// new key persisted under the next id is removed from disk so it doesn't
// accumulate as an orphan. The previous active key remains usable.
func TestStore_RotateOrphanCleanupOnFailure(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	db := openTestDB(t)
	ctx := context.Background()

	s, err := secrets.NewStore(db, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Set(ctx, "good", "", "good-value", ""))

	// Corrupt one row so MasterRotate's decrypt fails.
	_, err = db.ExecContext(ctx,
		`UPDATE secrets SET ciphertext = X'00' WHERE name = 'good'`)
	require.NoError(t, err)

	require.Error(t, s.MasterRotate(ctx))

	// meta.active_key_id must still be 1 (rollback).
	var idStr string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='active_key_id'`).Scan(&idStr))
	assert.Equal(t, "1", idStr)

	// On filesystems where the new key fell back to a file (no keychain),
	// the orphan must be removed. Where the keychain accepted it, the file
	// path won't exist anyway — so this assertion is meaningful in the
	// failure-mode-of-interest path and trivially true otherwise.
	orphan := filepath.Join(xdg, "agent-gateway", "master-key-2")
	_, statErr := os.Stat(orphan)
	assert.True(t, os.IsNotExist(statErr),
		"orphan key file %q must be cleaned up after failed rotation", orphan)
}

// TestStore_LegacyKeyMigration verifies that a master.key file written under
// the pre-versioned scheme is migrated to master-key-1 on first NewStore call,
// and that secrets encrypted with that legacy key continue to decrypt.
func TestStore_LegacyKeyMigration(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	// Plant a legacy master.key.
	legacyKey := make([]byte, 32)
	for i := range legacyKey {
		legacyKey[i] = byte(i + 7)
	}
	legacyPath := filepath.Join(xdg, "agent-gateway", "master.key")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyPath), 0o750))
	require.NoError(t, os.WriteFile(legacyPath,
		[]byte(hexEncode(legacyKey)), 0o600))

	// Seed a row encrypted under the legacy key by setting a secret via a
	// store constructed with that exact key. This bypasses the legacy
	// migration and writes the row directly.
	db := openTestDB(t)
	ctx := context.Background()
	pre, err := secrets.NewStoreWithKey(db, slog.Default(), legacyKey)
	require.NoError(t, err)
	require.NoError(t, pre.Set(ctx, "tok", "", "legacy-value", ""))

	// Now boot a real NewStore: it should detect the legacy file and
	// migrate it to master-key-1, then decrypt the seeded row.
	s, err := secrets.NewStore(db, slog.Default())
	require.NoError(t, err)
	got, _, err := s.Get(ctx, "tok", "")
	require.NoError(t, err)
	assert.Equal(t, "legacy-value", got)

	// On systems without a keychain, the migration writes to the new file.
	// On systems with a keychain, the migration removes the legacy file and
	// places the key in the keychain — either way the legacy file is gone.
	_, statErr := os.Stat(legacyPath)
	assert.True(t, os.IsNotExist(statErr),
		"legacy master.key %q must be removed after migration", legacyPath)
}

// hexEncode is a tiny local helper to avoid importing encoding/hex just for
// the legacy-migration test.
func hexEncode(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

func TestStore_List(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "alpha", "", "v1", "desc alpha"))
	require.NoError(t, s.Set(ctx, "beta", "mybot", "v2", "desc beta"))

	list, err := s.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	names := make(map[string]bool)
	for _, m := range list {
		names[m.Name] = true
	}
	assert.True(t, names["alpha"])
	assert.True(t, names["beta"])
}

func TestStore_Rotate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "old-value", ""))

	// Small sleep to ensure rotated_at changes.
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, s.Rotate(ctx, "tok", "", "new-value"))

	val, _, err := s.Get(ctx, "tok", "any")
	require.NoError(t, err)
	assert.Equal(t, "new-value", val)
}

func TestStore_Delete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "value", ""))
	require.NoError(t, s.Delete(ctx, "tok", ""))

	_, _, err := s.Get(ctx, "tok", "any")
	assert.True(t, errors.Is(err, secrets.ErrNotFound))
}

func TestStore_InvalidateCache(t *testing.T) {
	s := newTestStore(t)
	// InvalidateCache must not panic.
	s.InvalidateCache()
}

func TestMasterKey_FileFallbackWhenKeychainUnavailable(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")

	// Resolve with a non-existent keychain service/account so it falls back to file.
	// We pass a nonexistent keychain service name that will fail on Linux without
	// a Secret Service daemon.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	key1, fromFile, err := secrets.ResolveTestKey(keyFile, logger)
	require.NoError(t, err)
	assert.True(t, fromFile, "expected file fallback, got keychain")
	assert.Len(t, key1, 32, "key must be 32 bytes")

	// Key file must exist at mode 0o600.
	info, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Second call returns the same key.
	key2, fromFile2, err := secrets.ResolveTestKey(keyFile, logger)
	require.NoError(t, err)
	assert.True(t, fromFile2)
	assert.Equal(t, key1, key2)
}
