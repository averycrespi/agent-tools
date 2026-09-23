package acceptance

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/averycrespi/agent-tools/agent-gateway/test/keyringnative"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandDeadlineEnforcesBudgetAndCleansOwnedGroup(t *testing.T) {
	ledger, err := testutil.NewCleanupLedger(t.TempDir())
	require.NoError(t, err)
	t.Setenv(testutil.CleanupLedgerEnvironment, ledger.Path())
	for _, parentDeadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "command", true: "parent"}[parentDeadline], func(t *testing.T) {
			ctx := t.Context()
			budget := 100 * time.Millisecond
			if parentDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, budget)
				defer cancel()
				budget = time.Minute
			}
			_, err := runOSCommand(ctx, t.TempDir(), Command{
				Name: "sh", Arguments: []string{"-c", "sleep 30 & wait"}, Timeout: budget,
			}, false, io.Discard)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			termination, cleanup, _ := executionMetadata(err)
			assert.NotEqual(t, "none", termination)
			assert.Equal(t, "passed", cleanup)
			assert.Empty(t, ledger.Survivors())
		})
	}
}

func TestReleaseCommandCanExceedPackageTimeoutWithinBudget(t *testing.T) {
	// Emit a classifier fixture, not a native-provider operation.
	native, err := json.Marshal(keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed))
	require.NoError(t, err)
	root, definition, external := releaseRunnerFixture(t, func(definition *releaseProfileDefinition) {
		check := definition.Checks[0]
		check.Native = true
		check.Argv = []string{"sh", "-c", "sleep 0.1; printf '%s' \"$1\"", "fixture", string(native)}
		check.Coverage = definition.Coverage
		check.TimeoutMillis = 1
		check.BudgetMillis = 5000
		definition.Checks = []releaseCheckDefinition{check}
	})
	report, err := runReleaseProfile(t.Context(), root, OSExecutor{}, definition, external, passedReleaseCleanup)
	require.NoError(t, err)
	require.Equal(t, ResultPassed, report.Result)
	require.Len(t, report.Checks, 1)
	assert.Greater(t, report.Checks[0].DurationMillis, definition.Checks[0].TimeoutMillis)
}
