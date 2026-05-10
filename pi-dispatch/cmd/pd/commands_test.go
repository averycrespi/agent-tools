package main

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSteerArgValidationShowsUsage(t *testing.T) {
	err := steerCmd.Args(steerCmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "missing required arguments")
	require.Contains(t, err.Error(), "Usage: pd steer <task-id> <message>")
	require.NotContains(t, err.Error(), "accepts 2 arg(s)")
}

func TestSteerArgValidationExplainsQuotedMessage(t *testing.T) {
	err := steerCmd.Args(steerCmd, []string{"task-123"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "message is required")
	require.Contains(t, err.Error(), `Example: pd steer task-123 "focus on the failing package"`)
}

func TestExecuteDoesNotPrintValidationErrorTwice(t *testing.T) {
	rootCmd.SetArgs([]string{"steer"})
	var cobraErr bytes.Buffer
	rootCmd.SetErr(&cobraErr)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetErr(os.Stderr)
	})

	stderr := captureStderr(t, func() {
		require.Error(t, Execute())
	})

	require.Contains(t, cobraErr.String(), "Error:")
	require.Empty(t, stderr)
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = writePipe
	defer func() { os.Stderr = old }()

	fn()
	require.NoError(t, writePipe.Close())
	out, err := io.ReadAll(readPipe)
	require.NoError(t, err)
	require.NoError(t, readPipe.Close())
	return string(out)
}
