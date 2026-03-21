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
		"servers": [
			{"name": "echo", "command": "echo", "args": ["hello"]},
			{"name": "remote", "type": "http", "url": "http://localhost:3000/mcp"}
		]
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 2)
	require.Equal(t, "echo", cfg.Servers[0].Name)
	require.Equal(t, "echo", cfg.Servers[0].Command)
	require.Equal(t, "http", cfg.Servers[1].Type)
	require.Equal(t, "http://localhost:3000/mcp", cfg.Servers[1].URL)
}

func TestConfig_OAuthTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": [
			{"name": "github", "type": "http", "url": "https://api.github.com/mcp", "oauth": true}
		]
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 1)
	require.NotNil(t, cfg.Servers[0].OAuth)
	require.Empty(t, cfg.Servers[0].OAuth.ClientID)
}

func TestConfig_OAuthObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": [
			{"name": "custom", "type": "http", "url": "https://mcp.example.com", "oauth": {
				"client_id": "my-app",
				"scopes": ["read", "write"],
				"auth_server_url": "https://auth.example.com"
			}}
		]
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 1)
	require.NotNil(t, cfg.Servers[0].OAuth)
	require.Equal(t, "my-app", cfg.Servers[0].OAuth.ClientID)
	require.Equal(t, []string{"read", "write"}, cfg.Servers[0].OAuth.Scopes)
	require.Equal(t, "https://auth.example.com", cfg.Servers[0].OAuth.AuthServerURL)
}

func TestConfig_OAuthAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": [
			{"name": "plain", "type": "http", "url": "https://example.com/mcp"}
		]
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 1)
	require.Nil(t, cfg.Servers[0].OAuth)
}

func TestConfigPath_ReturnsXDGPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path := ConfigPath()
	require.Equal(t, filepath.Join(dir, "mcp-broker", "config.json"), path)
}
