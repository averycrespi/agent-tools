//go:build linux

package acceptance

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

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestS5AcceptanceExecutorCancellationReapsNestedProcessGroup(t *testing.T) {
	ledger, err := testutil.NewCleanupLedger(t.TempDir())
	require.NoError(t, err)
	t.Setenv(testutil.CleanupLedgerEnvironment, ledger.Path())
	pidPath := t.TempDir() + "/nested.pid"
	t.Setenv("MCP_GATEWAY_ACCEPTANCE_EXECUTOR_FIXTURE", pidPath)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, runErr := (OSExecutor{}).Run(ctx, t.TempDir(), Command{
			Name: os.Args[0], Arguments: []string{"-test.run=^TestS5AcceptanceExecutorNestedFixture$"},
		})
		result <- runErr
	}()

	pid := waitForFixturePID(t, pidPath)
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	require.Eventually(t, func() bool {
		err := syscall.Kill(pid, 0)
		return errors.Is(err, syscall.ESRCH)
	}, 2*time.Second, 10*time.Millisecond)
	require.Empty(t, ledger.Survivors())
}

func TestS5AcceptanceExecutorCLIHandlesTERM(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "acceptance")
	runner, err := testutil.NewBinaryRunner(30*time.Second, 64*1024)
	require.NoError(t, err)
	built, err := runner.Run(context.Background(), "go", "-C", filepath.Join(repositoryRoot(t), "mcp-gateway"), "build", "-o", binary, "./test/acceptance/cmd")
	require.NoError(t, err, "build stderr: %s", built.Stderr)
	readyPath := filepath.Join(t.TempDir(), "ready")
	t.Setenv("MCP_GATEWAY_ACCEPTANCE_SIGNAL_READY", readyPath)
	t.Setenv("MCP_GATEWAY_ACCEPTANCE_WAIT_FOR_SIGNAL", "1")
	process, err := runner.Start(context.Background(), binary, "--profile", "s2_1")
	require.NoError(t, err)
	waitForFixturePIDFile(t, readyPath)
	require.NoError(t, process.Signal(syscall.SIGTERM))
	result, waitErr := process.Wait()
	require.Error(t, waitErr)
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
}

func TestS5AcceptanceExecutorNestedFixture(t *testing.T) {
	pidPath := os.Getenv("MCP_GATEWAY_ACCEPTANCE_EXECUTOR_FIXTURE")
	if pidPath == "" {
		return
	}
	runner, err := testutil.NewBinaryRunner(time.Minute, 1024)
	require.NoError(t, err)
	process, err := runner.Start(context.Background(), "sh", "-c", "trap '' TERM; echo $$ > \"$MCP_GATEWAY_ACCEPTANCE_EXECUTOR_FIXTURE\"; while :; do :; done")
	require.NoError(t, err)
	_, _ = process.Wait()
}

func waitForFixturePID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		contents, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(contents)))
		return err == nil && pid > 0
	}, 2*time.Second, 10*time.Millisecond)
	return pid
}

func waitForFixturePIDFile(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool {
		contents, err := os.ReadFile(path)
		return err == nil && string(contents) == "ready\n"
	}, 2*time.Second, 10*time.Millisecond)
}
