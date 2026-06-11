package main

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootDoesNotExposeTemplateCommand(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"template"})

	require.Error(t, err)
	require.Same(t, rootCmd, cmd)
	require.Contains(t, err.Error(), "unknown command")
}

func TestRootDoesNotExposeEventsCommand(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"events"})

	require.Error(t, err)
	require.Same(t, rootCmd, cmd)
	require.Contains(t, err.Error(), "unknown command")
}

func TestRootDoesNotExposeAttachCommand(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"attach"})

	require.Error(t, err)
	require.Same(t, rootCmd, cmd)
	require.Contains(t, err.Error(), "unknown command")
}

func TestRunDoesNotExposeTemplateFlags(t *testing.T) {
	require.Nil(t, runCmd.Flags().Lookup("template"))
	require.Nil(t, runCmd.Flags().ShorthandLookup("t"))
	require.Nil(t, runCmd.Flags().Lookup("prompt-template"))
	require.Nil(t, runCmd.Flags().Lookup("no-prompt-templates"))
}

func TestStatusArgValidationShowsUsage(t *testing.T) {
	err := statusCmd.Args(statusCmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "task-id is required")
	require.Contains(t, err.Error(), "Usage: pd status <task-id>")
	require.NotContains(t, err.Error(), "accepts 1 arg(s)")
}

func TestExecuteDoesNotPrintValidationErrorTwice(t *testing.T) {
	rootCmd.SetArgs([]string{"status"})
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
