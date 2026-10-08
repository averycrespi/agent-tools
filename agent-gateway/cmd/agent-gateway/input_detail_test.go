package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIInputFileFailureHasLocalCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	for _, mode := range []string{"human", "json"} {
		command := newRootCmd()
		var stdout, stderr bytes.Buffer
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		command.SetArgs([]string{"mcp", "server", "create", "--file", path, "--output", mode})
		err := command.Execute()
		require.Error(t, err)
		require.Equal(t, 2, commandExitCode(err))
		require.Empty(t, stdout.String())
		if mode == "human" {
			require.Contains(t, stderr.String(), "no such file")
			require.Contains(t, stderr.String(), path)
		} else {
			require.NotContains(t, stderr.String(), path)
			require.NotContains(t, stderr.String(), "no such file")
		}
	}
}
