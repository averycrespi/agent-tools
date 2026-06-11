package config

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_DefaultWhenMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.DatabasePath)
	assert.Equal(t, "never", cfg.DefaultWorktreeCleanupPolicy)
}

func TestDefaultConfigDoesNotMarshalTemplateDirs(t *testing.T) {
	data, err := json.Marshal(Default())
	require.NoError(t, err)
	assert.NotContains(t, string(data), "template_dirs")
}

func TestRefreshWritesResolvedDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	require.NoError(t, Refresh(slog.New(slog.NewTextHandler(io.Discard, nil))))

	data, err := os.ReadFile(ConfigFilePath())
	require.NoError(t, err)
	expected := fmt.Sprintf(`{"database_path":%q,"default_worktree_cleanup_policy":"never"}`, filepath.Join(stateHome, "pd", "pd.db"))
	require.JSONEq(t, expected, string(data))
}

func TestRefreshPreservesExplicitDatabasePath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(ConfigDir(), 0o750))
	require.NoError(t, os.WriteFile(ConfigFilePath(), []byte(`{"database_path":"~/custom/pd.db","default_worktree_cleanup_policy":"never"}`), 0o600))

	require.NoError(t, Refresh(slog.New(slog.NewTextHandler(io.Discard, nil))))

	data, err := os.ReadFile(ConfigFilePath())
	require.NoError(t, err)
	require.Contains(t, string(data), `"database_path": "~/custom/pd.db"`)
}

func TestLoadRejectsInvalidCleanupPolicy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	require.NoError(t, os.MkdirAll(ConfigDir(), 0o750))
	require.NoError(t, os.WriteFile(ConfigFilePath(), []byte(`{"default_worktree_cleanup_policy":"always"}`), 0o600))

	_, err := Load()

	require.ErrorContains(t, err, "default_worktree_cleanup_policy")
}

func TestConfig_DBPath_DefaultsToStateDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := Default()
	assert.Equal(t, filepath.Join(StateDir(), "pd.db"), cfg.DBPath())
}

func TestExpandTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	assert.Equal(t, filepath.Join(home, "x"), ExpandTilde("~/x"))
	assert.Equal(t, "/tmp/x", ExpandTilde("/tmp/x"))
}
