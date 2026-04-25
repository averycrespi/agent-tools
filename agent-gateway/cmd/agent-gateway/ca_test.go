package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/ca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func TestCARotateCmd_HasLongHelp(t *testing.T) {
	cmd := newCARotateCmd()
	require.NotEmpty(t, cmd.Long)
	require.Contains(t, cmd.Long, "Immediate consequences")
	require.Contains(t, cmd.Long, "Recovery")
	require.Contains(t, cmd.Long, "re-trust")
}

func TestCmdCAExport(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	require.NoError(t, os.MkdirAll(paths.DataDir(), 0o750))

	// Generate an initial CA so the cert file exists.
	_, err := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())
	require.NoError(t, err)

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetArgs([]string{"ca", "export"})
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	// Output should be a PEM certificate.
	assert.Contains(t, out.String(), "-----BEGIN CERTIFICATE-----")
}

func TestCmdCAExportMissing(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cmd := newRootCmd()
	cmd.SetArgs([]string{"ca", "export"})
	err := cmd.Execute()
	require.Error(t, err)
}

func TestCmdCARotate(t *testing.T) {
	dataDir := t.TempDir()
	cfgDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	require.NoError(t, os.MkdirAll(paths.DataDir(), 0o750))

	// Generate an initial CA.
	_, err := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())
	require.NoError(t, err)

	// Record the original cert bytes.
	original, err := os.ReadFile(paths.CACert())
	require.NoError(t, err)

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetArgs([]string{"ca", "rotate", "--force"})
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	// The cert on disk must differ from the original.
	rotated, err := os.ReadFile(paths.CACert())
	require.NoError(t, err)
	assert.NotEqual(t, original, rotated)

	// Output must reference the cert path and the re-trust reminder.
	certPath := filepath.Join(dataDir, "agent-gateway", "ca.pem")
	assert.Contains(t, out.String(), certPath)
	assert.Contains(t, out.String(), "every sandbox must re-trust")
}

func TestCmdCARotateNoDaemon(t *testing.T) {
	dataDir := t.TempDir()
	cfgDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	require.NoError(t, os.MkdirAll(paths.DataDir(), 0o750))

	// No PID file exists; rotate must still succeed.
	_, err := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())
	require.NoError(t, err)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"ca", "rotate", "--force"})
	require.NoError(t, cmd.Execute())
}

func TestCmdCARotateOutputFormat(t *testing.T) {
	dataDir := t.TempDir()
	cfgDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	require.NoError(t, os.MkdirAll(paths.DataDir(), 0o750))

	_, err := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())
	require.NoError(t, err)

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetArgs([]string{"ca", "rotate", "--force"})
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	line := strings.TrimSpace(out.String())
	// Expected: "rotated: <abs path> — every sandbox must re-trust."
	assert.True(t, strings.HasPrefix(line, "rotated: "), "output should start with 'rotated: ', got: %q", line)
	assert.True(t, strings.HasSuffix(line, "every sandbox must re-trust."), "output should end with reminder, got: %q", line)
}
