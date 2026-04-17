package daemon_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAcquire_ReleaseCycle verifies:
//  1. Acquire creates the PID file and returns a handle.
//  2. A second Acquire against the same path fails (live daemon).
//  3. Release removes the file; a fresh Acquire then succeeds.
func TestAcquire_ReleaseCycle(t *testing.T) {
	// Inject a verifier that reports any live process as agent-gateway so the
	// cycle test does not depend on the test binary's comm name.
	restore := daemon.SetVerifyCommForTest(func(_ int) (bool, error) {
		return true, nil
	})
	defer restore()

	path := filepath.Join(t.TempDir(), "pidfile")
	h, err := daemon.Acquire(path)
	require.NoError(t, err)

	_, err = daemon.Acquire(path)
	require.Error(t, err) // second acquire fails while first holds it
	assert.ErrorIs(t, err, daemon.ErrAlreadyRunning)

	require.NoError(t, h.Release())

	h2, err := daemon.Acquire(path)
	require.NoError(t, err)
	require.NoError(t, h2.Release())
}

// TestAcquire_StaleFileIsOverwritten verifies that a PID file holding a PID
// that is either dead or belongs to a non-agent-gateway process is silently
// overwritten.
func TestAcquire_StaleFileIsOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pidfile")

	// Write a PID that definitely isn't us and isn't our binary.
	require.NoError(t, os.WriteFile(path, []byte("999999"), 0o600))

	h, err := daemon.Acquire(path)
	require.NoError(t, err)
	defer h.Release() //nolint:errcheck

	// Verify the file now holds our PID.
	got, _ := os.ReadFile(path)
	assert.Equal(t, strconv.Itoa(os.Getpid()), strings.TrimSpace(string(got)))
}

// TestAcquire_WrongComm verifies that a PID file pointing at a live process
// whose comm is not "agent-gateway" is treated as stale and overwritten.
func TestAcquire_WrongComm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pidfile")

	// Inject a verifier that reports our own PID as having the wrong comm name
	// so we can test the stale-overwrite path without needing a second process.
	restore := daemon.SetVerifyCommForTest(func(_ int) (bool, error) {
		return false, nil // wrong comm — treat as stale
	})
	defer restore()

	// Write our own PID so the liveness check passes.
	require.NoError(t, os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600))

	h, err := daemon.Acquire(path)
	require.NoError(t, err)
	defer h.Release() //nolint:errcheck
}

// TestAcquire_LiveGateway verifies that a PID file pointing at a live process
// whose comm IS "agent-gateway" blocks a second Acquire.
func TestAcquire_LiveGateway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pidfile")

	// Inject a verifier that reports our own PID as agent-gateway.
	restore := daemon.SetVerifyCommForTest(func(_ int) (bool, error) {
		return true, nil
	})
	defer restore()

	// Write our own PID so the liveness check passes.
	require.NoError(t, os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600))

	_, err := daemon.Acquire(path)
	require.ErrorIs(t, err, daemon.ErrAlreadyRunning)
}

// TestSignalDaemon_SendsHUP verifies that SignalDaemonWithVerifier sends SIGHUP
// to the PID in the file when the comm check passes.
func TestSignalDaemon_SendsHUP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pidfile")
	ownPID := os.Getpid()

	require.NoError(t, os.WriteFile(path, []byte(strconv.Itoa(ownPID)), 0o600))

	// verifier always says yes
	verifier := func(_ int) (bool, error) { return true, nil }

	// capture signal sent; actual sending is a no-op for signal(0) substitute
	var sentPID int
	var sentSig os.Signal
	sender := func(pid int, sig os.Signal) error {
		sentPID = pid
		sentSig = sig
		return nil
	}

	err := daemon.SignalDaemonWithVerifier(path, verifier, sender)
	require.NoError(t, err)
	assert.Equal(t, ownPID, sentPID)
	assert.Equal(t, daemon.HUPSignal, sentSig)
}

// TestSignalDaemon_WrongComm verifies that SignalDaemonWithVerifier returns an
// error if the running process is not agent-gateway.
func TestSignalDaemon_WrongComm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pidfile")
	require.NoError(t, os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600))

	verifier := func(_ int) (bool, error) { return false, nil }
	sender := func(_ int, _ os.Signal) error { return nil }

	err := daemon.SignalDaemonWithVerifier(path, verifier, sender)
	require.Error(t, err)
}

// TestSignalDaemon_NoFile verifies that SignalDaemonWithVerifier returns an
// error when the PID file does not exist.
func TestSignalDaemon_NoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pidfile")
	verifier := func(_ int) (bool, error) { return true, nil }
	sender := func(_ int, _ os.Signal) error { return nil }

	err := daemon.SignalDaemonWithVerifier(path, verifier, sender)
	require.Error(t, err)
}
