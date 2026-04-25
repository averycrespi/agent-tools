package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
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

// noStateDB is a sentinel path that does not exist, used to test the no-state-DB path.
const noStateDB = ""

// TestReload_NoDaemon_Errors verifies that execReload returns a non-nil error
// containing "no daemon running" when the PID file does not exist.
func TestReload_NoDaemon_Errors(t *testing.T) {
	buf := &bytes.Buffer{}
	err := execReload(nil, filepath.Join(t.TempDir(), "missing.pid"),
		noStateDB, "", alwaysVerify, failSend(t), buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no daemon running")
}

// TestReload_RunningDaemon_SendsSIGHUP verifies that execReload sends SIGHUP to
// the PID stored in the PID file and prints "reloaded" on success.
func TestReload_RunningDaemon_SendsSIGHUP(t *testing.T) {
	var sent os.Signal
	sendSpy := func(_ int, sig os.Signal) error { sent = sig; return nil }

	buf := &bytes.Buffer{}
	err := execReload(nil, validPIDFile(t), noStateDB, "", alwaysVerify, sendSpy, buf)
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
	err := execReload(nil, validPIDFile(t), noStateDB, "", wrongComm, noopSend, buf)
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

// setupReloadFixture creates a temp config.hcl and a state DB seeded with the
// given configHash. It returns pidPath, dbPath, and cfgPath.
func setupReloadFixture(t *testing.T, configHash string) (pidPath, dbPath, cfgPath string) {
	t.Helper()

	cfgDir := t.TempDir()
	dataDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_DATA_HOME", dataDir)

	// Create config dir and PID file.
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o750))
	pidPath = paths.PIDFile()
	require.NoError(t, os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600))

	// Write a config.hcl with known content.
	cfgPath = filepath.Join(cfgDir, "agent-gateway", "config.hcl")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o750))
	require.NoError(t, os.WriteFile(cfgPath, []byte("# test config\n"), 0o600))

	// Create a state DB and seed config_hash.
	require.NoError(t, os.MkdirAll(paths.DataDir(), 0o700))
	dbPath = paths.StateDB()
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, store.PutMeta(db, "config_hash", configHash))
	require.NoError(t, db.Close())

	return pidPath, dbPath, cfgPath
}

// hashOf returns the sha256 hex digest of data.
func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestReload_ConfigHashChanged_Warns verifies that execReload prints a warning
// when the on-disk config.hcl differs from the hash recorded in the state DB,
// but still fires SIGHUP and returns no error.
func TestReload_ConfigHashChanged_Warns(t *testing.T) {
	pidPath, dbPath, cfgPath := setupReloadFixture(t, "stale-hash-does-not-match")

	var sent os.Signal
	sendSpy := func(_ int, sig os.Signal) error { sent = sig; return nil }

	buf := &bytes.Buffer{}
	err := execReload(nil, pidPath, dbPath, cfgPath, alwaysVerify, sendSpy, buf)
	require.NoError(t, err)
	assert.Equal(t, syscall.SIGHUP, sent, "SIGHUP must still fire even when warning prints")

	out := buf.String()
	assert.Contains(t, out, "config.hcl has changed")
	assert.Contains(t, out, "restart")
	assert.Contains(t, out, "reloaded")
}

// TestReload_ConfigHashUnchanged_NoWarning verifies that no warning is printed
// when the recorded hash matches the current config.hcl.
func TestReload_ConfigHashUnchanged_NoWarning(t *testing.T) {
	// Seed the DB with the actual hash of the config file we will write.
	actualContent := []byte("# test config\n")
	actualHash := hashOf(actualContent)

	pidPath, dbPath, cfgPath := setupReloadFixture(t, actualHash)

	var sent os.Signal
	sendSpy := func(_ int, sig os.Signal) error { sent = sig; return nil }

	buf := &bytes.Buffer{}
	err := execReload(nil, pidPath, dbPath, cfgPath, alwaysVerify, sendSpy, buf)
	require.NoError(t, err)
	assert.Equal(t, syscall.SIGHUP, sent)

	out := buf.String()
	assert.NotContains(t, out, "config.hcl has changed")
	assert.Contains(t, out, "reloaded")
}

// TestReload_NoStateDB_NoCheck verifies that when the state DB is absent,
// execReload still fires SIGHUP without warning or error.
func TestReload_NoStateDB_NoCheck(t *testing.T) {
	pidPath := validPIDFile(t)
	missingDB := filepath.Join(t.TempDir(), "nonexistent.db")
	missingCfg := filepath.Join(t.TempDir(), "config.hcl")

	var sent os.Signal
	sendSpy := func(_ int, sig os.Signal) error { sent = sig; return nil }

	buf := &bytes.Buffer{}
	err := execReload(nil, pidPath, missingDB, missingCfg, alwaysVerify, sendSpy, buf)
	require.NoError(t, err)
	assert.Equal(t, syscall.SIGHUP, sent)

	out := buf.String()
	assert.NotContains(t, out, "config.hcl has changed")
	assert.Contains(t, out, "reloaded")
}
