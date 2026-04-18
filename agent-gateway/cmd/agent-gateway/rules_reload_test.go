package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

// TestRulesReloadSendsSIGHUP verifies that "rules reload" sends SIGHUP to the
// PID stored in the PID file when the comm verifier confirms it is the daemon.
// An injected signaller captures the signal so no real signal is delivered.
func TestRulesReloadSendsSIGHUP(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o750))

	// Write a PID file containing our own PID.
	ownPID := os.Getpid()
	require.NoError(t, os.WriteFile(paths.PIDFile(), []byte(strconv.Itoa(ownPID)), 0o600))

	var sentPID int
	var sentSig os.Signal

	// Inject a fake verifier (always OK) and a fake sender that captures args.
	fakeVerifier := func(_ int) (bool, error) { return true, nil }
	fakeSender := func(pid int, sig os.Signal) error {
		sentPID = pid
		sentSig = sig
		return nil
	}

	var outBuf bytes.Buffer
	err := execRulesReload(newRootCmd(), paths.PIDFile(), fakeVerifier, fakeSender, &outBuf)
	require.NoError(t, err)

	assert.Equal(t, ownPID, sentPID)
	assert.Equal(t, syscall.SIGHUP, sentSig)
	assert.Contains(t, outBuf.String(), "reloaded")
}

// TestRulesReloadNoDaemon verifies that "rules reload" exits 0 (no error) and
// prints "no daemon running" when no PID file exists.
func TestRulesReloadNoDaemon(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o750))

	// No PID file — path points to non-existent file.
	pidPath := filepath.Join(cfgDir, "agent-gateway", "no-such.pid")

	fakeVerifier := func(_ int) (bool, error) { return true, nil }
	fakeSender := func(_ int, _ os.Signal) error { return nil }

	var outBuf bytes.Buffer
	err := execRulesReload(newRootCmd(), pidPath, fakeVerifier, fakeSender, &outBuf)
	require.NoError(t, err)
	assert.Contains(t, outBuf.String(), "no daemon running")
}

// TestRulesReloadCLI verifies the "rules reload" subcommand is wired into the
// command tree and reachable via the cobra CLI.
func TestRulesReloadCLI(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o750))

	// No PID file present — should succeed silently (no daemon running path).
	var outBuf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&outBuf)
	cmd.SetErr(&outBuf)
	cmd.SetArgs([]string{"rules", "reload"})
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, outBuf.String(), "no daemon running")
}

// TestRulesReloadWrongComm verifies that reload returns an error when the PID
// file refers to a process that is not an agent-gateway.
func TestRulesReloadWrongComm(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o750))

	ownPID := os.Getpid()
	require.NoError(t, os.WriteFile(paths.PIDFile(), []byte(strconv.Itoa(ownPID)), 0o600))

	fakeVerifier := func(_ int) (bool, error) { return false, nil }
	fakeSender := func(_ int, _ os.Signal) error { return nil }

	var outBuf bytes.Buffer
	err := execRulesReload(newRootCmd(), paths.PIDFile(), fakeVerifier, fakeSender, &outBuf)
	require.Error(t, err)
	assert.True(t, errors.Is(err, err), "error should be non-nil")
}
