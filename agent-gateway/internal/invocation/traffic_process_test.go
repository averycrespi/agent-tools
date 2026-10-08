package invocation

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestTrafficForcedProcessTransactionRecovery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "installation")
	t.Setenv("AGENT_GATEWAY_TRAFFIC_CRASH_ROOT", root)
	executable, err := os.Executable()
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(20*time.Second, 64<<10)
	require.NoError(t, err)
	process, err := runner.Start(t.Context(), executable, "-test.run=^TestTrafficCrashProcessFixture$", "-test.timeout=15s")
	require.NoError(t, err)
	defer func() { _ = process.Stop() }()
	select {
	case <-process.StdoutReady():
	case <-time.After(10 * time.Second):
		t.Fatal("child did not reach uncommitted transaction")
	}
	require.NoError(t, process.Signal(syscall.SIGKILL))
	result, waitErr := process.Wait()
	require.Error(t, waitErr)
	require.Contains(t, string(result.Stdout), "traffic statement entered after acknowledged record")
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
	require.False(t, result.StdoutTruncated)
	require.False(t, result.StderrTruncated)
	path := filepath.Join(root, "traffic-"+invocationID(90)+".db")
	info, err := os.Stat(path + "-wal")
	require.NoError(t, err)
	require.Positive(t, info.Size())
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	s, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), DefaultTrafficConfig())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	history, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.Equal(t, invocationID(1), history.Records[0].InvocationID)
	recordMCP(t, s, trafficPrepared(3))
	require.EqualValues(t, 1, s.Status(t.Context()).Delivery.Acknowledged)
	history, err = s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 2)
	require.Equal(t, invocationID(3), history.Records[1].InvocationID)
}

func TestTrafficCrashProcessFixture(t *testing.T) {
	root := os.Getenv("AGENT_GATEWAY_TRAFFIC_CRASH_ROOT")
	if root == "" {
		t.Skip("owned subprocess fixture")
	}
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	s, err := CreateTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), DefaultTrafficConfig())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	// Hold a real snapshot before the acknowledged commit, ensuring committed WAL
	// survives even though normal writer handles have operation-scoped lifetimes.
	tx, err := s.readerDB.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var count int
	require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT count(*) FROM invocations`).Scan(&count))
	recordMCP(t, s, trafficPrepared(1))
	require.EqualValues(t, 1, s.Status(t.Context()).Delivery.Acknowledged)
	s.fault = func(point string) error {
		if point == "statement" {
			fmt.Println("traffic statement entered after acknowledged record")
			<-time.After(12 * time.Second)
		}
		return nil
	}
	s.ObserveMCP(trafficPrepared(2))
	waitTraffic(t, s)
	t.Fatal("uncommitted fixture unexpectedly settled")
}
