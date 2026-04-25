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

// writeEditorScript writes a shell script to scriptPath that uses sed to
// replace oldVal with newVal in the first argument (the config file path).
// The script is written with mode 0755.
func writeEditorScript(t *testing.T, scriptPath, oldVal, newVal string) {
	t.Helper()
	content := "#!/bin/sh\nsed -i 's/" + oldVal + "/" + newVal + "/g' \"$1\"\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(content), 0o755))
}

// setupConfigEditFixture creates a temp XDG_CONFIG_HOME and writes an initial
// config.hcl (via config.Refresh). Returns the path to config.hcl.
func setupConfigEditFixture(t *testing.T) string {
	t.Helper()
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	cfgPath := filepath.Join(cfgDir, "agent-gateway", "config.hcl")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o700))
	// Use the CLI to write defaults.
	cmd := newRootCmd()
	cmd.SetArgs([]string{"config", "refresh"})
	require.NoError(t, cmd.Execute())
	return cfgPath
}

// TestConfigEdit_WarnsOnFieldChange verifies that execConfigEdit prints a
// restart-required warning when the editor changes a restart-required field.
func TestConfigEdit_WarnsOnFieldChange(t *testing.T) {
	cfgPath := setupConfigEditFixture(t)

	// Write an editor script that changes approval.timeout from "5m" to "30m".
	scriptPath := filepath.Join(t.TempDir(), "edit.sh")
	writeEditorScript(t, scriptPath, `5m`, `30m`)
	t.Setenv("EDITOR", scriptPath)

	buf := &bytes.Buffer{}
	err := execConfigEdit(cfgPath, buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "config.hcl has changed")
	assert.Contains(t, out, "approval.timeout")
}

// TestConfigEdit_NoChange_NoWarning verifies that no warning is printed when
// the editor does not modify the file.
func TestConfigEdit_NoChange_NoWarning(t *testing.T) {
	cfgPath := setupConfigEditFixture(t)

	// Write an editor script that makes no changes (cat does nothing).
	scriptPath := filepath.Join(t.TempDir(), "noop.sh")
	content := "#!/bin/sh\n# no-op editor\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(content), 0o755))
	t.Setenv("EDITOR", scriptPath)

	buf := &bytes.Buffer{}
	err := execConfigEdit(cfgPath, buf)
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), "config.hcl has changed")
}
