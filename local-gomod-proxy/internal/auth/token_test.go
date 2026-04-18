package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenPath_UsesXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	assert.Equal(t, "/tmp/xdg/local-gomod-proxy/auth-token", TokenPath())
}

func TestEnsureToken_GeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "token")

	token, err := EnsureToken(path)
	require.NoError(t, err)
	assert.Len(t, token, 64) // 32 bytes hex-encoded

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestEnsureToken_ReusesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(path, []byte("deadbeef"), 0o600))

	token, err := EnsureToken(path)
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", token)
}

func TestLoadToken_ErrorsWhenMissing(t *testing.T) {
	_, err := LoadToken(filepath.Join(t.TempDir(), "nope"))
	assert.Error(t, err)
}
