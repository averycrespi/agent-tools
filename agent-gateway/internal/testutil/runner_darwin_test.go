package testutil

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestBinaryRunnerCapturesExitedUnreapedChild(t *testing.T) {
	for _, withLedger := range []bool{false, true} {
		name := "without ledger"
		if withLedger {
			name = "with ledger"
		}
		t.Run(name, func(t *testing.T) {
			ledgerPath := ""
			if withLedger {
				ledger, err := NewCleanupLedger(t.TempDir())
				require.NoError(t, err)
				ledgerPath = ledger.Path()
			}
			t.Setenv(CleanupLedgerEnvironment, ledgerPath)
			runner, err := NewBinaryRunner(2*time.Second, 1024)
			require.NoError(t, err)
			runner.beforeGroupCapture = func(process *os.Process) {
				// Observe the child without reaping it, forcing the fast-exit startup window.
				assert.Eventually(t, func() bool {
					info, inspectErr := unix.SysctlKinfoProc("kern.proc.pid", process.Pid)
					return inspectErr == nil && info.Proc.P_stat == 5
				}, time.Second, time.Millisecond)
			}
			result, runErr := runner.Run(t.Context(), "sh", "-c", "printf stdout; printf stderr >&2; exit 7")
			var exitErr *exec.ExitError
			require.ErrorAs(t, runErr, &exitErr, "runner error: %v", runErr)
			assert.Equal(t, 7, result.ExitCode, "runner error: %v", runErr)
			assert.Equal(t, "stdout", string(result.Stdout))
			assert.Equal(t, "stderr", string(result.Stderr))
			assert.True(t, result.Cleanup.Reaped)
			assert.False(t, result.Cleanup.Survived)
		})
	}
}
