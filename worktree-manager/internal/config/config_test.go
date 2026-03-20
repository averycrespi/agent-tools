package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var nopLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// --- Path function tests (adapted from CCO paths_test.go) ---

func TestDataDir_Default(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	dir := DataDir()
	home, _ := os.UserHomeDir()
	expected := home + "/.local/share/wt"
	assert.Equal(t, expected, dir)
}

func TestDataDir_XDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	assert.Equal(t, "/custom/data/wt", DataDir())
}

func TestWorktreeDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	dir := WorktreeDir("myapp", "feat/thing")
	assert.Equal(t, "/data/wt/worktrees/myapp/myapp-feat-thing", dir)
}

func TestSanitizeBranch(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"feat/my-thing", "feat-my-thing"},
		{"simple", "simple"},
		{"a/b/c", "a-b-c"},
		{"feat_underscore", "feat-underscore"},
		{"UPPER/case", "UPPER-case"},
	}
	for _, tt := range tests {
		got := SanitizeBranch(tt.input)
		assert.Equal(t, tt.want, got, "SanitizeBranch(%q)", tt.input)
	}
}

func TestTmuxSessionName(t *testing.T) {
	assert.Equal(t, "wt-myapp", TmuxSessionName("myapp"))
}

func TestTmuxWindowName(t *testing.T) {
	assert.Equal(t, "feat-thing", TmuxWindowName("feat/thing"))
}

func TestConfigDir_Default(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := ConfigDir()
	home, _ := os.UserHomeDir()
	assert.Equal(t, filepath.Join(home, ".config", "wt"), dir)
}

func TestConfigDir_XDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	assert.Equal(t, "/custom/config/wt", ConfigDir())
}

func TestConfigFilePath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	assert.Equal(t, "/custom/config/wt/config.json", ConfigFilePath())
}

func TestWorktreeBaseDir(t *testing.T) {
	dir := WorktreeBaseDir()
	assert.True(t, strings.HasSuffix(dir, filepath.Join("wt", "worktrees")))
}

// --- Config tests (adapted from CCO config_test.go) ---

func TestLoad_FileNotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()

	require.NoError(t, err)
	assert.Empty(t, cfg.LaunchCommand)
	assert.Empty(t, cfg.SetupScripts)
	assert.Empty(t, cfg.CopyFiles)
}

func TestLoad_EmptyJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	wtDir := filepath.Join(dir, "wt")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "config.json"), []byte("{}"), 0o644))

	cfg, err := Load()

	require.NoError(t, err)
	assert.Empty(t, cfg.LaunchCommand)
	assert.Empty(t, cfg.SetupScripts)
	assert.Empty(t, cfg.CopyFiles)
}

func TestLoad_WithConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	wtDir := filepath.Join(dir, "wt")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))
	data := []byte(`{
		"launch_command": "claude",
		"setup_scripts": ["scripts/setup.sh"],
		"copy_files": [".env.local"]
	}`)
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "config.json"), data, 0o644))

	cfg, err := Load()

	require.NoError(t, err)
	assert.Equal(t, "claude", cfg.LaunchCommand)
	assert.Equal(t, []string{"scripts/setup.sh"}, cfg.SetupScripts)
	assert.Equal(t, []string{".env.local"}, cfg.CopyFiles)
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	wtDir := filepath.Join(dir, "wt")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "config.json"), []byte("not json"), 0o644))

	_, err := Load()

	assert.Error(t, err)
}

func TestDefault(t *testing.T) {
	cfg := Default()

	assert.Empty(t, cfg.LaunchCommand)
	assert.NotNil(t, cfg.SetupScripts)
	assert.Empty(t, cfg.SetupScripts)
	assert.NotNil(t, cfg.CopyFiles)
	assert.Empty(t, cfg.CopyFiles)
}

func TestRefresh_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	err := Refresh(nopLogger)

	require.NoError(t, err)
	path := filepath.Join(dir, "wt", "config.json")
	assert.FileExists(t, path)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.LaunchCommand)
}

func TestRefresh_PreservesExistingValues(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	wtDir := filepath.Join(dir, "wt")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))
	data := []byte(`{"launch_command":"claude","setup_scripts":["s.sh"],"copy_files":[".env"]}`)
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "config.json"), data, 0o644))

	require.NoError(t, Refresh(nopLogger))

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "claude", cfg.LaunchCommand)
	assert.Equal(t, []string{"s.sh"}, cfg.SetupScripts)
	assert.Equal(t, []string{".env"}, cfg.CopyFiles)
}

func TestRefresh_PopulatesNilFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	wtDir := filepath.Join(dir, "wt")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "config.json"), []byte("{}"), 0o644))

	require.NoError(t, Refresh(nopLogger))

	cfg, err := Load()
	require.NoError(t, err)
	assert.NotNil(t, cfg.SetupScripts)
	assert.NotNil(t, cfg.CopyFiles)
}
