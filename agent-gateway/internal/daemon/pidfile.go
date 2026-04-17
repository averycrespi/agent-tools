// Package daemon manages a PID file for the agent-gateway process and provides
// helpers for signalling a running daemon.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrAlreadyRunning is returned by Acquire when a live agent-gateway process
// already holds the PID file.
var ErrAlreadyRunning = errors.New("agent-gateway is already running")

// HUPSignal is the signal sent to a daemon by SignalDaemon / SignalDaemonWithVerifier.
// Exported so tests can assert on it.
var HUPSignal os.Signal = syscall.SIGHUP

// verifyComm is the package-level hook for comm verification.
// Tests replace it via export_test.go.
var verifyComm = defaultVerifyComm

// Handle represents ownership of the PID file.
type Handle struct {
	path string
}

// Release removes the PID file, relinquishing ownership.
func (h *Handle) Release() error {
	if err := os.Remove(h.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pid file: %w", err)
	}
	return nil
}

// Acquire attempts to take ownership of the PID file at path.
//
// If the file exists:
//   - If the stored PID is alive AND its comm matches "agent-gateway", return
//     ErrAlreadyRunning.
//   - Otherwise (dead process or wrong comm), treat the file as stale and
//     overwrite it.
//
// If the file does not exist, create it with the current PID.
func Acquire(path string) (*Handle, error) {
	if data, err := os.ReadFile(path); err == nil {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil && isAlive(pid) {
			ok, commErr := verifyComm(pid)
			if commErr != nil {
				return nil, fmt.Errorf("verify comm for pid %d: %w", pid, commErr)
			}
			if ok {
				return nil, ErrAlreadyRunning
			}
		}
		// Stale: fall through to overwrite.
	}

	if err := writePID(path); err != nil {
		return nil, err
	}
	return &Handle{path: path}, nil
}

// SignalDaemon reads the PID file at path, verifies the running process is
// agent-gateway, and sends SIGHUP to it.
func SignalDaemon(path string) error {
	return SignalDaemonWithVerifier(path, defaultVerifyComm, defaultSendSignal)
}

// SignalDaemonWithVerifier is the injectable variant of SignalDaemon used by
// tests to substitute the comm verifier and signal sender.
func SignalDaemonWithVerifier(
	path string,
	verify func(pid int) (bool, error),
	send func(pid int, sig os.Signal) error,
) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pid file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("parse pid: %w", err)
	}

	ok, err := verify(pid)
	if err != nil {
		return fmt.Errorf("verify comm for pid %d: %w", pid, err)
	}
	if !ok {
		return fmt.Errorf("pid %d is not an agent-gateway process", pid)
	}

	if err := send(pid, HUPSignal); err != nil {
		return fmt.Errorf("send signal to pid %d: %w", pid, err)
	}
	return nil
}

// isAlive returns true if the process with the given PID is running.
func isAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence without actually delivering a signal.
	return p.Signal(syscall.Signal(0)) == nil
}

// writePID writes the current process's PID to path (mode 0o600).
func writePID(path string) error {
	data := []byte(strconv.Itoa(os.Getpid()))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	return nil
}

// defaultSendSignal sends sig to the process with the given PID via os.FindProcess.
func defaultSendSignal(pid int, sig os.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}
