//go:build darwin || linux

package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCommandRunnerUsesDirectArgvAndWritesStdin(t *testing.T) {
	dir := t.TempDir()
	stdinPath := filepath.Join(dir, "stdin.json")
	argvPath := filepath.Join(dir, "argv.txt")
	script := filepath.Join(dir, "capture.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat > \"$1\"\nprintf '%s' \"$2\" > \"$3\"\nprintf 'ignored stdout'\nprintf 'ignored stderr' >&2\n"), 0o700))

	payload := []byte("{\"value\":true}\n")
	status := (commandRunner{}).Run(context.Background(), Handler{
		Command: script,
		Args:    []string{stdinPath, "literal; echo not-a-shell", argvPath},
		Env:     os.Environ(),
	}, payload)
	require.Equal(t, RunStatusSucceeded, status)
	captured, err := os.ReadFile(stdinPath)
	require.NoError(t, err)
	require.Equal(t, payload, captured)
	argv, err := os.ReadFile(argvPath)
	require.NoError(t, err)
	require.Equal(t, "literal; echo not-a-shell", string(argv))
}

func TestCommandRunnerTimeoutTerminatesInheritedProcessGroup(t *testing.T) {
	dir := t.TempDir()
	childPIDPath := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "descendant.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 30 &\necho $! > \"$1\"\nwait\n"), 0o700))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	status := (commandRunner{}).Run(ctx, Handler{Command: script, Args: []string{childPIDPath}, Env: os.Environ()}, []byte("{}\n"))
	require.Equal(t, RunStatusTimedOut, status)

	pidBytes, err := os.ReadFile(childPIDPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		err := syscall.Kill(pid, 0)
		return errors.Is(err, syscall.ESRCH)
	}, time.Second, 10*time.Millisecond, "same-group descendant remained alive")
}
