package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureToken_CreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-token")

	token, err := EnsureToken(path)
	require.NoError(t, err)
	require.Len(t, token, 64) // 32 bytes hex-encoded

	// File should exist with 0600 permissions.
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// File contents should match returned token.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, token, string(data))
}

func TestEnsureToken_ReusesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-token")

	token1, err := EnsureToken(path)
	require.NoError(t, err)

	token2, err := EnsureToken(path)
	require.NoError(t, err)
	require.Equal(t, token1, token2)
}

func TestEnsureToken_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "auth-token")

	token, err := EnsureToken(path)
	require.NoError(t, err)
	require.Len(t, token, 64)
}

func TestLoadToken_FailsIfFileMissing(t *testing.T) {
	_, err := LoadToken(filepath.Join(t.TempDir(), "nonexistent"))
	require.Error(t, err)
}
