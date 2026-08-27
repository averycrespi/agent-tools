//go:build darwin || linux

package testutil

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestS5ProcessSupervisorKillsTermIgnoringProcessGroup(t *testing.T) {
	runner, err := NewBinaryRunner(150*time.Millisecond, 1024)
	require.NoError(t, err)

	result, runErr := runner.Run(context.Background(), "sh", "-c", `
		trap '' TERM
		sh -c 'trap "" TERM; sh -c '\''trap "" TERM; while :; do :; done'\'' & while :; do :; done' &
		printf ready
		while :; do :; done
	`)
	require.ErrorIs(t, runErr, context.DeadlineExceeded)
	require.True(t, result.Cleanup.TermSent)
	require.True(t, result.Cleanup.KillSent)
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
	require.Positive(t, result.Cleanup.ProcessGroupID)
	require.ErrorIs(t, syscall.Kill(-result.Cleanup.ProcessGroupID, 0), syscall.ESRCH)
}

func TestS5BinaryRunnerCancellationCleansProcessGroup(t *testing.T) {
	runner, err := NewBinaryRunner(2*time.Second, 1024)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	process, err := runner.Start(ctx, "sh", "-c", `trap '' TERM; sh -c 'trap "" TERM; while :; do :; done' & printf ready; while :; do :; done`)
	require.NoError(t, err)
	<-process.StdoutReady()
	cancel()

	result, runErr := process.Wait()
	require.ErrorIs(t, runErr, context.Canceled)
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
	require.ErrorIs(t, syscall.Kill(-result.Cleanup.ProcessGroupID, 0), syscall.ESRCH)
}

func TestS5BinaryRunnerStartupOutputFailureCanStopOwnedGroup(t *testing.T) {
	runner, err := NewBinaryRunner(2*time.Second, 1024)
	require.NoError(t, err)
	process, err := runner.Start(context.Background(), "sh", "-c", `trap '' TERM; sh -c 'trap "" TERM; while :; do :; done' & printf malformed; while :; do :; done`)
	require.NoError(t, err)
	<-process.StdoutReady()

	require.NoError(t, process.Stop())
	result, runErr := process.Wait()
	require.Error(t, runErr)
	require.False(t, errors.Is(runErr, context.DeadlineExceeded))
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
	require.ErrorIs(t, syscall.Kill(-result.Cleanup.ProcessGroupID, 0), syscall.ESRCH)
}

func TestS5BinaryRunnerReportsCommandFailureAfterReap(t *testing.T) {
	runner, err := NewBinaryRunner(2*time.Second, 1024)
	require.NoError(t, err)

	result, runErr := runner.Run(context.Background(), "sh", "-c", "printf failed >&2; exit 7")
	require.Error(t, runErr)
	require.Equal(t, 7, result.ExitCode)
	require.Equal(t, []byte("failed"), result.Stderr)
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
}
