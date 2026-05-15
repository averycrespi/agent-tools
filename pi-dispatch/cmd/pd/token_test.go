package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestTokenRotateReplacesTokenWithoutPrintingSecret(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	path := auth.TokenPath()
	oldToken, err := auth.EnsureToken(path)
	require.NoError(t, err)

	var stdout bytes.Buffer
	tokenRotateCmd.SetOut(&stdout)
	t.Cleanup(func() { tokenRotateCmd.SetOut(os.Stdout) })

	require.NoError(t, tokenRotateCmd.RunE(tokenRotateCmd, nil))

	newToken, err := auth.LoadToken(path)
	require.NoError(t, err)
	require.NotEqual(t, oldToken, newToken)
	require.NotContains(t, stdout.String(), oldToken)
	require.NotContains(t, stdout.String(), newToken)
	require.Contains(t, stdout.String(), "New auth token written to "+path)
	require.Contains(t, stdout.String(), "Restart running pd dashboard servers to apply")
}
