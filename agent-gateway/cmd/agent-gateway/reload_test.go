package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

// alwaysVerify is an injectable comm verifier that always reports the process
// as agent-gateway, used to skip the real /proc/<pid>/comm check in tests.
func alwaysVerify(_ int) (bool, error) { return true, nil }

// failSend is an injectable signal sender that fails the test if called. It is
// used in tests where no signal should be sent (e.g., missing PID file cases).
func failSend(t *testing.T) func(int, os.Signal) error {
	return func(_ int, _ os.Signal) error {
		t.Helper()
		t.Fatal("failSend: send was called unexpectedly")
		return nil
	}
}

// validPIDFile writes a PID file containing the current process's PID inside
// the XDG config dir for the test and returns its path.
func validPIDFile(t *testing.T) string {
	t.Helper()
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o750))
	pidPath := paths.PIDFile()
	require.NoError(t, os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600))
	return pidPath
}

// TestReload_NoDaemon_Errors verifies that execReload returns a non-nil error
// containing "no daemon running" when the PID file does not exist.
func TestReload_NoDaemon_Errors(t *testing.T) {
	buf := &bytes.Buffer{}
	err := execReload(nil, filepath.Join(t.TempDir(), "missing.pid"),
		alwaysVerify, failSend(t), buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no daemon running")
}

// TestReload_RunningDaemon_SendsSIGHUP verifies that execReload sends SIGHUP to
// the PID stored in the PID file and prints "reloaded" on success.
func TestReload_RunningDaemon_SendsSIGHUP(t *testing.T) {
	var sent os.Signal
	sendSpy := func(_ int, sig os.Signal) error { sent = sig; return nil }

	buf := &bytes.Buffer{}
	err := execReload(nil, validPIDFile(t), alwaysVerify, sendSpy, buf)
	require.NoError(t, err)
	assert.Equal(t, syscall.SIGHUP, sent)
	assert.Contains(t, buf.String(), "reloaded")
}

// TestReload_WrongComm verifies that execReload returns an error when the PID
// file refers to a process whose comm name is not agent-gateway.
func TestReload_WrongComm_Errors(t *testing.T) {
	wrongComm := func(_ int) (bool, error) { return false, nil }
	noopSend := func(_ int, _ os.Signal) error { return nil }

	buf := &bytes.Buffer{}
	err := execReload(nil, validPIDFile(t), wrongComm, noopSend, buf)
	require.Error(t, err)
}

// TestReloadCLI verifies the top-level "reload" subcommand is wired into the
// command tree and exits non-zero when no daemon is running.
func TestReloadCLI_NoDaemon_Errors(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o750))

	var outBuf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&outBuf)
	cmd.SetErr(&outBuf)
	cmd.SetArgs([]string{"reload"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "no daemon running")
}
