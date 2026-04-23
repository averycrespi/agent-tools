package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveDir_OverrideWins(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom")
	got, err := ResolveDir(override)
	require.NoError(t, err)
	assert.Equal(t, override, got)
}

func TestResolveDir_XDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got, err := ResolveDir("")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(os.Getenv("XDG_STATE_HOME"), "local-gomod-proxy"), got)
}

func TestResolveDir_HomeFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", t.TempDir())
	got, err := ResolveDir("")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(os.Getenv("HOME"), ".local/state/local-gomod-proxy"), got)
}

func TestEnsureDir_Creates0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, EnsureDir(dir))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestEnsureDir_Idempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, EnsureDir(dir))
	require.NoError(t, EnsureDir(dir)) // second call must not fail
}
