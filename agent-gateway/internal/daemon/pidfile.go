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
// WHY O_EXCL: using O_CREATE|O_EXCL|O_WRONLY is TOCTOU-safe — the kernel
// atomically creates the file or returns EEXIST, eliminating the window
// between a "file does not exist" check and a subsequent create/write that
// the previous approach (ReadFile + atomicfile.Write) had.
//
// If the file does not exist, create it exclusively and write the current PID.
//
// If the file already exists (EEXIST):
//   - Read and parse the stored PID.
//   - If the PID is alive AND its comm matches "agent-gateway", return
//     ErrAlreadyRunning.
//   - Otherwise (dead process or wrong comm), the file is stale: remove it
//     and retry the exclusive creation atomically so we don't race with
//     another starter that may have just removed it too.
func Acquire(path string) (*Handle, error) {
	for {
		// Step 1: try atomic exclusive creation.
		if err := createExclusive(path); err == nil {
			return &Handle{path: path}, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create pid file: %w", err)
		}

		// Step 2: file exists — read and evaluate.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			// File disappeared between our EEXIST and ReadFile — retry.
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read pid file: %w", readErr)
		}

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

		// Step 3: stale file — remove it and retry the exclusive creation.
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale pid file: %w", rmErr)
		}
		// Loop: retry createExclusive now that the stale file is gone.
	}
}

// createExclusive creates path with O_CREATE|O_EXCL|O_WRONLY and writes the
// current PID. Returns os.ErrExist (wrapped) if the file already exists.
func createExclusive(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = fmt.Fprintf(f, "%d", os.Getpid())
	return err
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

// defaultSendSignal sends sig to the process with the given PID via os.FindProcess.
func defaultSendSignal(pid int, sig os.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

// DefaultVerifyComm is the exported wrapper around defaultVerifyComm for use
// by callers outside this package (e.g. the rules reload CLI command).
func DefaultVerifyComm(pid int) (bool, error) {
	return defaultVerifyComm(pid)
}

// DefaultSendSignal is the exported wrapper around defaultSendSignal for use
// by callers outside this package (e.g. the rules reload CLI command).
func DefaultSendSignal(pid int, sig os.Signal) error {
	return defaultSendSignal(pid, sig)
}
