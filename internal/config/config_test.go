package config_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var nopLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestLoad_MissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, config.Default(), cfg)
}

func TestLoad_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "sb")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	data := `{"image":"ubuntu-22.04","cpus":2,"memory":"2GiB","disk":"50GiB","mounts":["/tmp/test"],"copy_paths":["~/.zshrc"],"scripts":["setup.sh"]}`
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(data), 0o644))

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "ubuntu-22.04", cfg.Image)
	assert.Equal(t, 2, cfg.CPUs)
	assert.Equal(t, "2GiB", cfg.Memory)
	assert.Equal(t, "50GiB", cfg.Disk)
	assert.Equal(t, []string{"/tmp/test"}, cfg.Mounts)
	assert.Equal(t, []string{"~/.zshrc"}, cfg.CopyPaths)
	assert.Equal(t, []string{"setup.sh"}, cfg.Scripts)
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "sb")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte("{invalid"), 0o644))

	_, err := config.Load()
	assert.Error(t, err)
}

func TestRefresh_CreatesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	require.NoError(t, config.Refresh(nopLogger))

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, config.Default(), cfg)
}

func TestParseCopyPath_PlainPath(t *testing.T) {
	src, dst := config.ParseCopyPath("/home/user/.zshrc")
	assert.Equal(t, "/home/user/.zshrc", src)
	assert.Equal(t, "/home/user/.zshrc", dst)
}

func TestParseCopyPath_MappedPath(t *testing.T) {
	src, dst := config.ParseCopyPath("/host/settings.json:/guest/settings.json")
	assert.Equal(t, "/host/settings.json", src)
	assert.Equal(t, "/guest/settings.json", dst)
}

func TestConfigFilePath_XDGOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	assert.Equal(t, "/custom/config/sb/config.json", config.ConfigFilePath())
}

func TestConfigFilePath_Default(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "sb", "config.json")
	assert.Equal(t, expected, config.ConfigFilePath())
}
