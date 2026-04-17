package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCmdConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetArgs([]string{"config", "path"})
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "config.hcl")
}

func TestCmdConfigRefresh(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"config", "refresh"})
	require.NoError(t, cmd.Execute())
	_, err := os.Stat(filepath.Join(dir, "agent-gateway", "config.hcl"))
	require.NoError(t, err)
}
