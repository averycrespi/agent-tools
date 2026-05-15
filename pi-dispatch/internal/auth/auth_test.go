package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenPathUsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))

	require.Equal(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pd", "auth-token"), TokenPath())
}

func TestEnsureTokenCreatesAndReusesToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pd", "auth-token")

	first, err := EnsureToken(path)
	require.NoError(t, err)
	require.Len(t, first, 64)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	second, err := EnsureToken(path)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestRotateTokenReplacesExistingToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pd", "auth-token")
	first, err := EnsureToken(path)
	require.NoError(t, err)

	second, err := RotateToken(path)
	require.NoError(t, err)
	require.Len(t, second, 64)
	require.NotEqual(t, first, second)

	loaded, err := LoadToken(path)
	require.NoError(t, err)
	require.Equal(t, second, loaded)
}
