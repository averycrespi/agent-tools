//go:build darwin || linux

package runtimes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func fixtureProcessGroup() int {
	group, _ := unix.Getpgid(0)
	return group
}

func TestStdioHardCrashOwner(t *testing.T) {
	if len(os.Args) < 3 {
		return
	}
	mode := os.Args[len(os.Args)-2]
	if mode != "hard-crash-owner" && mode != "hard-crash-owner-exit" {
		return
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	definition := fixtureDefinition(executable, "ignore-term")
	if mode == "hard-crash-owner-exit" {
		definition = fixtureDefinition(executable, "exit", "23")
	}
	runtime, err := NewStdioSupervisor(nil).Start(ctx, definition)
	require.NoError(t, err)
	defer runtime.Stop(context.Background())
	require.NoError(t, awaitHardCrashReady(ctx, runtime))
	require.NoError(t, runtime.command.Process.Signal(syscall.Signal(0)))
	ready, err := json.Marshal(runtime.command.Process.Pid)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Args[len(os.Args)-1], ready, 0o600))
	require.NoError(t, json.NewEncoder(os.Stdout).Encode(runtime.command.Process.Pid))
	select {
	case exit := <-runtime.Done():
		t.Fatalf("fixture exited after readiness: %s; stderr=%q", exit.Reason, hardCrashDiagnostics(runtime))
	case <-ctx.Done():
		t.Fatal("owner was not hard-killed before its fixture deadline")
	}
}

func TestHardCrashRestartDoesNotActOnOrClaimOrphanedPID(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(20*time.Second, 4096)
	require.NoError(t, err)
	readyPath := filepath.Join(t.TempDir(), "ready.json")
	owner, err := runner.Start(t.Context(), executable, "-test.run=^TestStdioHardCrashOwner$", "--", "hard-crash-owner", readyPath)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, owner.Stop()) })

	// Wait is bounded by the runner even if the fixture never reports readiness.
	type ownerExit struct {
		result testutil.ProcessResult
		err    error
	}
	exited := make(chan ownerExit, 1)
	go func() {
		result, waitErr := owner.Wait()
		exited <- ownerExit{result, waitErr}
	}()
	var signalErr, readinessErr error
	var childPID int
	var exit ownerExit
	select {
	case <-owner.StdoutReady():
		// A test failure also writes stdout. Only the private readiness record
		// permits a hard kill; otherwise let the owner finish its cleanup.
		var ready []byte
		ready, readinessErr = os.ReadFile(readyPath)
		if readinessErr == nil {
			readinessErr = json.Unmarshal(ready, &childPID)
		}
		if readinessErr == nil && childPID > 0 {
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				assert.NoError(t, cleanupHardCrashOrphan(ctx, childPID))
			})
			signalErr = owner.Signal(syscall.SIGKILL)
		}
		exit = <-exited
	case exit = <-exited:
	}
	require.NoError(t, readinessErr, "owner stdout=%q stderr=%q exit=%v", exit.result.Stdout, exit.result.Stderr, exit.err)
	require.Positive(t, childPID, "owner stdout=%q stderr=%q exit=%v", exit.result.Stdout, exit.result.Stderr, exit.err)
	require.False(t, exit.result.StdoutTruncated)
	require.False(t, exit.result.StderrTruncated)
	require.NoError(t, signalErr, "owner stdout=%q stderr=%q exit=%v", exit.result.Stdout, exit.result.Stderr, exit.err)
	var exitError *exec.ExitError
	require.ErrorAs(t, exit.err, &exitError)
	status, ok := exitError.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	require.Equal(t, syscall.SIGKILL, status.Signal(), "owner must die by the deliberate hard kill; stdout=%q stderr=%q", exit.result.Stdout, exit.result.Stderr)
	assert.NoError(t, syscall.Kill(childPID, 0), "orphan %d must survive its owner's hard kill; stdout=%q stderr=%q", childPID, exit.result.Stdout, exit.result.Stderr)
	assert.Zero(t, NewStdioSupervisor(nil).Status().InUse)
	assert.NoError(t, syscall.Kill(childPID, 0), "a new supervisor must not act on the orphan")
}

func cleanupHardCrashOrphan(ctx context.Context, childPID int) error {
	if childPID <= 0 {
		return fmt.Errorf("invalid owned child PID: %d", childPID)
	}
	group, err := syscall.Getpgid(childPID)
	switch {
	case errors.Is(err, syscall.ESRCH):
		// The leader may already be gone; still check for group survivors below.
	case err != nil:
		return fmt.Errorf("inspect orphan group: %w", err)
	case group != childPID:
		return fmt.Errorf("orphan group changed: got %d, want %d", group, childPID)
	default:
		if err := syscall.Kill(-group, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("kill orphan group: %w", err)
		}
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := syscall.Kill(-childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("inspect orphan survivors: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("orphan group %d survived cleanup: %w", childPID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestHardCrashCleanupRejectsUnownedPIDSelectors(t *testing.T) {
	for _, pid := range []int{0, -1} {
		require.ErrorContains(t, cleanupHardCrashOrphan(t.Context(), pid), "invalid owned child PID")
	}
}

func awaitHardCrashReady(ctx context.Context, runtime *StdioRuntime) error {
	select {
	case frame, open := <-runtime.Frames():
		if !open {
			return fmt.Errorf("fixture exited before readiness: %w; stderr=%q", io.ErrUnexpectedEOF, hardCrashDiagnostics(runtime))
		}
		if !bytes.Equal(frame, []byte(`{}`)) {
			return fmt.Errorf("invalid fixture readiness frame: %q", frame[:min(len(frame), 256)])
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func hardCrashDiagnostics(runtime *StdioRuntime) string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return string(runtime.diagnostics[:min(len(runtime.diagnostics), 1024)])
}

func TestHardCrashReadinessRejectsExitedChild(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runtime, err := NewStdioSupervisor(nil).Start(ctx, fixtureDefinition(executable, "exit", "23"))
	require.NoError(t, err)
	defer runtime.Stop(context.Background())
	require.ErrorIs(t, awaitHardCrashReady(ctx, runtime), io.ErrUnexpectedEOF)
}

func TestHardCrashOwnerDoesNotPublishExitedChild(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(20*time.Second, 4096)
	require.NoError(t, err)
	readyPath := filepath.Join(t.TempDir(), "ready.json")
	result, err := runner.Run(t.Context(), executable, "-test.run=^TestStdioHardCrashOwner$", "--", "hard-crash-owner-exit", readyPath)
	require.Error(t, err)
	assert.Equal(t, 1, result.ExitCode)
	assert.Contains(t, string(result.Stdout), "fixture exited before readiness")
	assert.False(t, result.StdoutTruncated)
	assert.False(t, result.StderrTruncated)
	assert.True(t, result.Cleanup.Reaped)
	assert.False(t, result.Cleanup.Survived)
	_, err = os.Stat(readyPath)
	assert.ErrorIs(t, err, os.ErrNotExist, "an exited child must not authorize a hard kill")
}

func TestHardCrashReadinessRejectsUnexpectedFrame(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runtime, err := NewStdioSupervisor(nil).Start(ctx, fixtureDefinition(executable, "frame", "3"))
	require.NoError(t, err)
	defer runtime.Stop(context.Background())
	require.ErrorContains(t, awaitHardCrashReady(ctx, runtime), "invalid fixture readiness frame")
}

func TestStdioSupervisorCreatesNewProcessGroup(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	runtime, err := NewStdioSupervisor(nil).Start(context.Background(), StdioDefinition{
		RuntimeID: "runtime-process-group", Executable: executable,
		Arguments: []string{"-test.run=TestStdioFixtureProcess", "--", "inspect"}, WorkingDirectory: "/",
		Environment: map[string]string{stdioFixtureMarker: "1"}, SecretEnvironment: map[string]string{}, Secrets: map[string]string{},
	})
	require.NoError(t, err)
	var inspection fixtureInspection
	require.NoError(t, json.Unmarshal(receiveFrame(t, runtime.Frames()), &inspection))
	assert.Equal(t, inspection.PID, inspection.ProcessGroup)
	_ = receiveExit(t, runtime.Done())
}
