//go:build darwin || linux

package hooks

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

func TestCommandRunnerClassifiesNonzeroAndMissingCommands(t *testing.T) {
	status := (commandRunner{}).Run(context.Background(), Handler{
		Command: "/bin/sh", Args: []string{"-c", "exit 7"}, Env: os.Environ(),
	}, []byte("{}\n"))
	require.Equal(t, RunStatusFailed, status)

	status = (commandRunner{}).Run(context.Background(), Handler{
		Command: filepath.Join(t.TempDir(), "missing-command"), Env: os.Environ(),
	}, []byte("{}\n"))
	require.Equal(t, RunStatusFailed, status)
}

func TestCommandRunnerDoesNotWaitForEscapedDescendantHoldingDescriptors(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "escaped.pid")
	env := append(os.Environ(), "HOOK_HELPER_MODE=direct", "HOOK_HELPER_PID_PATH="+pidPath)
	result := make(chan RunStatus, 1)
	go func() {
		result <- (commandRunner{}).Run(context.Background(), Handler{
			Command: os.Args[0], Args: []string{"-test.run=^TestHookEscapedDescendantHelper$"}, Env: env,
		}, make([]byte, 2*1024*1024))
	}()

	select {
	case status := <-result:
		require.Equal(t, RunStatusSucceeded, status)
	case <-time.After(2 * time.Second):
		t.Fatal("runner waited for an escaped descendant holding inherited descriptors")
	}
	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	process, err := os.FindProcess(pid)
	require.NoError(t, err)
	_ = process.Kill()
}

func TestHookEscapedDescendantHelper(t *testing.T) {
	switch os.Getenv("HOOK_HELPER_MODE") {
	case "direct":
		cmd := exec.Command(os.Args[0], "-test.run=^TestHookEscapedDescendantHelper$")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "HOOK_HELPER_MODE=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "HOOK_HELPER_MODE=escaped")
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		require.NoError(t, cmd.Start())
		require.NoError(t, os.WriteFile(os.Getenv("HOOK_HELPER_PID_PATH"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600))
	case "escaped":
		time.Sleep(30 * time.Second)
	}
}

func TestCommandRunnerTimeoutTerminatesInheritedProcessGroup(t *testing.T) {
	dir := t.TempDir()
	childPIDPath := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "descendant.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nsh -c 'trap \"\" TERM; sleep 30' &\necho $! > \"$1\"\nwait\n"), 0o700))

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
