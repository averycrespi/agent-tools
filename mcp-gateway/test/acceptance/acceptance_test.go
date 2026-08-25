package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeExecutor struct {
	calls  int
	failAt int
	native keyringnative.Result
}

func (executor *fakeExecutor) Run(_ context.Context, _ string, command Command) ([]byte, error) {
	executor.calls++
	if executor.calls == executor.failAt {
		return nil, errors.New("forced failure")
	}
	if command.Native {
		return json.Marshal(executor.native)
	}
	return nil, nil
}

func TestRunPropagatesCommandFailure(t *testing.T) {
	executor := &fakeExecutor{failAt: 3}
	report := Run(context.Background(), repositoryRoot(t), executor, true)

	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "test_e2e_failed", report.Reason)
	require.Len(t, report.Checks, 3)
	assert.Equal(t, ResultFailed, report.Checks[2].Status)
	assert.Equal(t, 3, executor.calls)
	assert.Nil(t, report.Native)
	assertReportValid(t, report)
}

func TestRunParsesSkippedNativeAsAdditiveEvidence(t *testing.T) {
	native := keyringnative.NewResult(keyringnative.ResultSkipped, "linux", "linux_prerequisites_unavailable", keyringnative.ResultPassed, keyringnative.ResultSkipped)
	executor := &fakeExecutor{native: native}
	report := Run(context.Background(), repositoryRoot(t), executor, true)

	assert.Equal(t, ResultPassed, report.Result)
	assert.Equal(t, "all_checks_passed", report.Reason)
	require.NotNil(t, report.Native)
	assert.Equal(t, keyringnative.ResultSkipped, report.Native.Result)
	assert.Equal(t, len(Commands()), executor.calls)
	assertReportValid(t, report)
}

func TestRunRejectsInvalidNativeEvidence(t *testing.T) {
	executor := &fakeExecutor{native: keyringnative.NewResult(keyringnative.ResultPassed, "linux", "invalid", keyringnative.ResultPassed, keyringnative.ResultSkipped)}
	report := Run(context.Background(), repositoryRoot(t), executor, true)

	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "native_result_invalid", report.Reason)
	assert.Nil(t, report.Native)
	assertReportValid(t, report)
}

func TestCommandsMapEveryAcceptanceCriterion(t *testing.T) {
	covered := map[string]bool{}
	for _, command := range Commands() {
		for _, criterion := range command.Criteria {
			covered[criterion] = true
		}
	}
	for _, criterion := range []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6"} {
		assert.True(t, covered[criterion], criterion)
	}
}

func assertReportValid(t *testing.T, report Report) {
	t.Helper()
	contents, err := json.Marshal(report)
	require.NoError(t, err)
	_, err = Parse(contents)
	require.NoError(t, err)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	require.NoError(t, err)
	return root + "/../../.."
}
