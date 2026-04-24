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

	err := s.Set(ctx, "gh_bot", "", "token-value", "GitHub bot token", []string{"**"})
	require.NoError(t, err)

	val, scope, _, err := s.Get(ctx, "gh_bot", "any-agent")
	require.NoError(t, err)
	assert.Equal(t, "token-value", val)
	assert.Equal(t, "global", scope)
}

func TestStore_ScopeResolution(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Set a global secret.
	require.NoError(t, s.Set(ctx, "gh_bot", "", "global-value", "global", []string{"**"}))
	// Set an agent-scoped secret for "foo".
	require.NoError(t, s.Set(ctx, "gh_bot", "foo", "foo-value", "foo scoped", []string{"**"}))

	// agent "foo" gets the agent-scoped value.
	val, scope, _, err := s.Get(ctx, "gh_bot", "foo")
	require.NoError(t, err)
	assert.Equal(t, "foo-value", val)
	assert.Equal(t, "agent:foo", scope)

	// agent "bar" falls back to the global value.
	val, scope, _, err = s.Get(ctx, "gh_bot", "bar")
	require.NoError(t, err)
	assert.Equal(t, "global-value", val)
	assert.Equal(t, "global", scope)

	// Delete the global secret; now agent "baz" gets ErrNotFound.
	require.NoError(t, s.Delete(ctx, "gh_bot", ""))
	_, _, _, err = s.Get(ctx, "gh_bot", "baz")
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
	require.NoError(t, s.Set(ctx, "mykey", "", plaintext, "", []string{"**"}))

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
	require.NoError(t, s.Set(ctx, "secret1", "", "value-one", "", []string{"**"}))
	require.NoError(t, s.Set(ctx, "secret2", "bar", "value-two", "", []string{"**"}))

	// Rotate master key.
	require.NoError(t, s.MasterRotate(ctx))

	// Both secrets should still decrypt correctly.
	val1, _, _, err := s.Get(ctx, "secret1", "any")
	require.NoError(t, err)
	assert.Equal(t, "value-one", val1)

	val2, _, _, err := s.Get(ctx, "secret2", "bar")
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
	require.NoError(t, s1.Set(ctx, "alpha", "", "alpha-value", "", []string{"**"}))
	require.NoError(t, s1.Set(ctx, "beta", "agent1", "beta-value", "", []string{"**"}))
	require.NoError(t, s1.MasterRotate(ctx))
	require.NoError(t, db.Close())

	// Second boot: reopen, NewStore must resolve active_key_id=2 and decrypt.
	db2, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = db2.Close() }()
	s2, err := secrets.NewStore(db2, slog.Default())
	require.NoError(t, err)

	val, _, _, err := s2.Get(ctx, "alpha", "")
	require.NoError(t, err)
	assert.Equal(t, "alpha-value", val)

	val, _, _, err = s2.Get(ctx, "beta", "agent1")
	require.NoError(t, err)
	assert.Equal(t, "beta-value", val)

	// A second rotation should advance to id=3 and still decrypt.
	require.NoError(t, s2.MasterRotate(ctx))
	val, _, _, err = s2.Get(ctx, "alpha", "")
	require.NoError(t, err)
	assert.Equal(t, "alpha-value", val)
}

// TestStore_RotateOrphanCleanupOnFailure verifies that if the rewrap
// transaction fails after PersistID has written the new master key to disk,
// the new key is cleaned up so the next rotation picks a fresh id without
// accumulating an unusable orphan. We force the failure by closing the DB
// before MasterRotate runs, which causes tx.Begin to fail after the new key
// was persisted.
func TestStore_RotateOrphanCleanupOnFailure(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	db := openTestDB(t)
	ctx := context.Background()

	s, err := secrets.NewStore(db, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Set(ctx, "good", "", "good-value", "", []string{"**"}))

	// Close the DB so the transaction inside MasterRotate fails AFTER
	// PersistID has already written the new key to disk. This exercises
	// the orphan-cleanup code path.
	require.NoError(t, db.Close())

	require.Error(t, s.MasterRotate(ctx))

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
	require.NoError(t, pre.Set(ctx, "tok", "", "legacy-value", "", []string{"**"}))

	// Now boot a real NewStore: it should detect the legacy file and
	// migrate it to master-key-1, then decrypt the seeded row.
	s, err := secrets.NewStore(db, slog.Default())
	require.NoError(t, err)
	got, _, _, err := s.Get(ctx, "tok", "")
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

	require.NoError(t, s.Set(ctx, "alpha", "", "v1", "desc alpha", []string{"**"}))
	require.NoError(t, s.Set(ctx, "beta", "mybot", "v2", "desc beta", []string{"**"}))

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

	require.NoError(t, s.Set(ctx, "tok", "", "old-value", "", []string{"**"}))

	// Small sleep to ensure rotated_at changes.
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, s.Rotate(ctx, "tok", "", "new-value"))

	val, _, _, err := s.Get(ctx, "tok", "any")
	require.NoError(t, err)
	assert.Equal(t, "new-value", val)
}

func TestStore_Delete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "value", "", []string{"**"}))
	require.NoError(t, s.Delete(ctx, "tok", ""))

	_, _, _, err := s.Get(ctx, "tok", "any")
	assert.True(t, errors.Is(err, secrets.ErrNotFound))
}

func TestStore_Set_RejectsEmptyAllowedHosts(t *testing.T) {
	s := newTestStore(t)
	err := s.Set(context.Background(), "tok", "", "v", "", nil)
	assert.ErrorIs(t, err, secrets.ErrNoAllowedHosts)
}

func TestStore_Set_Duplicate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Set(ctx, "dupe", "", "v1", "", []string{"**"}))

	err := s.Set(ctx, "dupe", "", "v2", "", []string{"**"})
	assert.ErrorIs(t, err, secrets.ErrDuplicate)

	// Global and agent-scoped with same name are distinct — not duplicates.
	require.NoError(t, s.Set(ctx, "dupe", "mybot", "v3", "", []string{"**"}))
}

func TestStore_Set_RejectsWildcardOnly(t *testing.T) {
	s := newTestStore(t)
	err := s.Set(context.Background(), "tok", "", "v", "", []string{"*.*"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches every")
}

// TestStore_Set_RejectsPublicSuffix locks in that allowed_hosts refuses any
// glob whose stripped form is an ICANN-managed public suffix (e.g. "*.com",
// "*.co", "*.io"). Unlike no_intercept_hosts — where an over-broad pattern
// means "too much MITM" — allowed_hosts is the credential-scoping layer: a
// too-broad entry would route real credentials to every host under a
// registry-controlled TLD, which is a security bug, not a config convenience.
// So we reject outright instead of warning.
func TestStore_Set_RejectsPublicSuffix(t *testing.T) {
	for _, p := range []string{"*.co", "*.io", "*.com"} {
		t.Run(p, func(t *testing.T) {
			s := newTestStore(t)
			err := s.Set(context.Background(), "tok", "", "v", "", []string{p})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "public suffix",
				"error should mention public suffix, got: %v", err)
			assert.Contains(t, err.Error(), p,
				"error should name the rejected pattern, got: %v", err)
		})
	}
}

// TestStore_Bind_RejectsPublicSuffix locks in that the same rejection applies
// at bind time — an operator can't widen a narrowly-scoped secret to a whole
// public suffix via `secret bind`.
func TestStore_Bind_RejectsPublicSuffix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Set(ctx, "tok", "", "v", "", []string{"api.github.com"}))

	err := s.Bind(ctx, "tok", "", []string{"*.com"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public suffix")
}

func TestStore_Set_NormalizesHosts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Set(ctx, "tok", "", "v", "", []string{"API.GitHub.COM.", "*.Example.com"}))
	_, _, hosts, err := s.Get(ctx, "tok", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"api.github.com", "*.example.com"}, hosts)
}

func TestStore_BindUnbind(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Set(ctx, "tok", "", "v", "", []string{"api.github.com"}))

	// Bind adds a new host.
	require.NoError(t, s.Bind(ctx, "tok", "", []string{"*.github.com"}))
	_, _, hosts, err := s.Get(ctx, "tok", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"api.github.com", "*.github.com"}, hosts)

	// Bind is idempotent — re-binding an existing pattern is a no-op.
	require.NoError(t, s.Bind(ctx, "tok", "", []string{"api.github.com"}))
	_, _, hosts, err = s.Get(ctx, "tok", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"api.github.com", "*.github.com"}, hosts)

	// Unbind one.
	require.NoError(t, s.Unbind(ctx, "tok", "", []string{"api.github.com"}))
	_, _, hosts, err = s.Get(ctx, "tok", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"*.github.com"}, hosts)

	// Unbind the last one errors.
	err = s.Unbind(ctx, "tok", "", []string{"*.github.com"})
	assert.ErrorIs(t, err, secrets.ErrNoAllowedHosts)
}

func TestStore_Bind_NotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.Bind(context.Background(), "missing", "", []string{"example.com"})
	assert.ErrorIs(t, err, secrets.ErrNotFound)
}

func TestHostScopeAllows(t *testing.T) {
	cases := []struct {
		hosts []string
		host  string
		want  bool
	}{
		{[]string{"api.github.com"}, "api.github.com", true},
		{[]string{"api.github.com"}, "evil.com", false},
		{[]string{"*.github.com"}, "api.github.com", true},
		{[]string{"*.github.com"}, "a.b.github.com", false},
		{[]string{"**.github.com"}, "a.b.github.com", true},
		{[]string{"**"}, "anything.example.com", true},
		{[]string{"api.github.com", "*.internal"}, "service.internal", true},
	}
	for _, tc := range cases {
		got := secrets.HostScopeAllows(tc.hosts, tc.host)
		assert.Equal(t, tc.want, got, "HostScopeAllows(%v, %q)", tc.hosts, tc.host)
	}
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
