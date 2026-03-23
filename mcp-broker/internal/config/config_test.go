package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_CreatesDefaultOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 8200, cfg.Port)
	require.Equal(t, "info", cfg.Log.Level)
	require.FileExists(t, path)
}

func TestLoad_ReadsExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"port": 9000}`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 9000, cfg.Port)
}

func TestRefresh_BackfillsNewDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"port": 9000}`), 0o600)
	require.NoError(t, err)

	written, err := Refresh(path)
	require.NoError(t, err)
	require.Equal(t, path, written)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 9000, cfg.Port)
	require.Equal(t, "info", cfg.Log.Level)
}

func TestConfig_ServerTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": {
			"echo": {"command": "echo", "args": ["hello"]},
			"remote": {"type": "streamable-http", "url": "http://localhost:3000/mcp"}
		}
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 2)
	require.Equal(t, "echo", cfg.Servers["echo"].Command)
	require.Equal(t, "streamable-http", cfg.Servers["remote"].Type)
	require.Equal(t, "http://localhost:3000/mcp", cfg.Servers["remote"].URL)
}

func TestDefaultConfig_OpenBrowserDefaultsTrue(t *testing.T) {
	cfg := DefaultConfig()
	require.True(t, cfg.OpenBrowser)
}

func TestLoad_OpenBrowserFromJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"open_browser": false}`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.False(t, cfg.OpenBrowser)
}


func TestConfigPath_ReturnsXDGPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path := ConfigPath()
	require.Equal(t, filepath.Join(dir, "mcp-broker", "config.json"), path)
}
