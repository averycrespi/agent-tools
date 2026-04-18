//go:build integration

package secrets_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_KeychainFallback verifies that key resolution writes to the file
// fallback path when no keychain daemon is available (CI / headless Linux).
func TestStore_KeychainFallback(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")

	key, fromFile, err := secrets.ResolveTestKey(keyFile, slog.Default())
	require.NoError(t, err)
	assert.True(t, fromFile, "expected file fallback")
	assert.Len(t, key, 32)

	// File must exist with mode 0600.
	info, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Second resolution returns the same key.
	key2, fromFile2, err := secrets.ResolveTestKey(keyFile, slog.Default())
	require.NoError(t, err)
	assert.True(t, fromFile2)
	assert.Equal(t, key, key2)
}
