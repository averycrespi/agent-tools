//go:build integration

package secrets

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_KeychainFallback verifies that key resolution writes to the file
// fallback path when no keychain daemon is available (CI / headless Linux).
func TestStore_KeychainFallback(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")

	key, fromFile, err := ResolveTestKey(keyFile, slog.Default())
	require.NoError(t, err)
	assert.True(t, fromFile, "expected file fallback")
	assert.Len(t, key, 32)

	// File must exist with mode 0600.
	info, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Second resolution returns the same key.
	key2, fromFile2, err := ResolveTestKey(keyFile, slog.Default())
	require.NoError(t, err)
	assert.True(t, fromFile2)
	assert.Equal(t, key, key2)
}

// TestStore_MigrateToDEK_EndToEnd seeds a database in the pre-migration format
// (rows encrypted directly under the master key, no AAD, no DEK in meta), then
// opens a Store and asserts every row decrypts correctly under the post-
// migration DEK+AAD format. This is the upgrade-path test: existing installs
// must never lose access to their secrets when the crypto overhaul lands.
func TestStore_MigrateToDEK_EndToEnd(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// Fixed master key so the test seeds pre-migration rows with the exact
	// key the store will resolve on open.
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}

	ctx := context.Background()

	// Seed pre-migration rows: encrypt each plaintext directly under the
	// master key with the old (no-AAD) helper, and INSERT the row with an
	// empty/default allowed_hosts list mirroring what migration 7 would have
	// produced on a real upgrade.
	type seed struct {
		name      string
		scope     string
		plaintext string
		hosts     string
	}
	seeds := []seed{
		{name: "gh_bot", scope: "global", plaintext: "global-token", hosts: `["**"]`},
		{name: "api_key", scope: "agent:mybot", plaintext: "mybot-key", hosts: `["api.github.com"]`},
		{name: "db_password", scope: "agent:dbagent", plaintext: "p@ssw0rd-with-symbols", hosts: `["db.internal"]`},
	}
	for _, s := range seeds {
		nonce, ciphertext, err := encrypt(masterKey, []byte(s.plaintext))
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `
INSERT INTO secrets (name, scope, ciphertext, nonce, created_at, rotated_at, description, allowed_hosts)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			s.name, s.scope, ciphertext, nonce, 1234567890, 1234567890, "", s.hosts)
		require.NoError(t, err)
	}

	// Sanity: meta.dek_wrapped is NULL pre-migration (sentinel for migrateToDEK).
	var dekWrapped []byte
	err = db.QueryRowContext(ctx, `SELECT dek_wrapped FROM meta WHERE key = 'active_key_id'`).Scan(&dekWrapped)
	require.NoError(t, err)
	require.Nil(t, dekWrapped, "dek_wrapped must be NULL before migration")

	// Open the store — this must run migrateToDEK and re-encrypt every row.
	s, err := NewStoreWithKey(db, slog.Default(), masterKey)
	require.NoError(t, err)

	// Every seeded plaintext must now come back under the new format.
	for _, seed := range seeds {
		agent := ""
		if seed.scope != "global" {
			// scope "agent:foo" → agent "foo"
			agent = seed.scope[len("agent:"):]
		}
		val, scope, _, err := s.Get(ctx, seed.name, agent)
		require.NoError(t, err, "get %q", seed.name)
		assert.Equal(t, seed.plaintext, val, "plaintext mismatch for %q", seed.name)
		assert.Equal(t, seed.scope, scope, "scope mismatch for %q", seed.name)
	}

	// Post-migration invariants on meta: DEK material populated, nonce and salt non-empty.
	var dekNonce, kekSalt []byte
	err = db.QueryRowContext(ctx,
		`SELECT dek_wrapped, dek_nonce, kek_kdf_salt FROM meta WHERE key = 'active_key_id'`,
	).Scan(&dekWrapped, &dekNonce, &kekSalt)
	require.NoError(t, err)
	assert.NotEmpty(t, dekWrapped, "dek_wrapped must be populated after migration")
	assert.Len(t, dekNonce, 12, "dek_nonce must be 12 bytes")
	assert.GreaterOrEqual(t, len(kekSalt), 16, "kek_kdf_salt must be at least 16 bytes")

	// Post-migration invariant on rows: ciphertext must NOT be decryptable
	// under the master key with the old helper — it is now under the DEK with
	// AAD. (Finding the old-format plaintext in the column would mean migration
	// did not run.)
	var ct, nonce []byte
	err = db.QueryRowContext(ctx,
		`SELECT ciphertext, nonce FROM secrets WHERE name = 'gh_bot' AND scope = 'global'`,
	).Scan(&ct, &nonce)
	require.NoError(t, err)
	_, oldErr := decrypt(masterKey, nonce, ct)
	assert.Error(t, oldErr, "post-migration ciphertext must not decrypt under master key without AAD")

	// Re-opening the store must be a no-op (migration is idempotent on a
	// populated meta) and secrets still decrypt.
	s2, err := NewStoreWithKey(db, slog.Default(), masterKey)
	require.NoError(t, err)
	val, _, _, err := s2.Get(ctx, "gh_bot", "")
	require.NoError(t, err)
	assert.Equal(t, "global-token", val)

	// meta.dek_wrapped blob must be unchanged across the reopen (no re-migration).
	var dekWrapped2 []byte
	err = db.QueryRowContext(ctx,
		`SELECT dek_wrapped FROM meta WHERE key = 'active_key_id'`).Scan(&dekWrapped2)
	require.NoError(t, err)
	assert.Equal(t, dekWrapped, dekWrapped2, "re-open must not rewrap DEK")
}

// TestStore_MigrateToDEK_FreshDB verifies that opening a Store against a fresh
// database (no pre-migration rows) still populates the DEK material so
// subsequent Sets use the new format.
func TestStore_MigrateToDEK_FreshDB(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}
	s, err := NewStoreWithKey(db, slog.Default(), masterKey)
	require.NoError(t, err)

	// Meta must have DEK material populated even with no rows to migrate.
	var dekWrapped, dekNonce, kekSalt []byte
	err = db.QueryRowContext(context.Background(),
		`SELECT dek_wrapped, dek_nonce, kek_kdf_salt FROM meta WHERE key = 'active_key_id'`,
	).Scan(&dekWrapped, &dekNonce, &kekSalt)
	require.NoError(t, err)
	assert.NotEmpty(t, dekWrapped)
	assert.Len(t, dekNonce, 12)
	assert.GreaterOrEqual(t, len(kekSalt), 16)

	// Set/Get roundtrip under the new format.
	ctx := context.Background()
	require.NoError(t, s.Set(ctx, "tok", "", "v", "", []string{"**"}))
	val, _, _, err := s.Get(ctx, "tok", "")
	require.NoError(t, err)
	assert.Equal(t, "v", val)
}
