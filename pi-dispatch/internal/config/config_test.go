package config

import (
	"encoding/json"
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
}

func TestDefaultConfigDoesNotMarshalTemplateDirs(t *testing.T) {
	data, err := json.Marshal(Default())
	require.NoError(t, err)
	assert.NotContains(t, string(data), "template_dirs")
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
