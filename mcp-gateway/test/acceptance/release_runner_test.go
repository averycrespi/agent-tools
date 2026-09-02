package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type releaseFakeExecutor struct {
	calls   []string
	failAt  int
	failure error
	after   func()
}

func (executor *releaseFakeExecutor) Run(_ context.Context, _ string, command Command) ([]byte, error) {
	executor.calls = append(executor.calls, command.CheckName)
	if executor.failAt == len(executor.calls) {
		return nil, executor.failure
	}
	if executor.after != nil {
		executor.after()
		executor.after = nil
	}
	if command.Native {
		result := keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed)
		return json.Marshal(result)
	}
	return nil, nil
}

func TestReleaseRunnerExecutesClosedOrderAndProducesValidReport(t *testing.T) {
	root, definition, external := releaseRunnerFixture(t)
	executor := &releaseFakeExecutor{}
	report, err := runReleaseProfile(context.Background(), root, executor, definition, external, passedReleaseCleanup)
	require.NoError(t, err)
	assert.Equal(t, []string{"unit", "native"}, executor.calls)
	assert.Equal(t, ResultPassed, report.Result)
	assert.Equal(t, "all_checks_passed", report.Reason)
	require.NotNil(t, report.Native)
	assert.Equal(t, keyringnative.ResultPassed, report.Native.Result)
	assert.True(t, report.Cleanup.Processes)
	assert.True(t, report.Cleanup.Listeners)
	assert.True(t, report.Cleanup.TemporaryRoots)
	require.NoError(t, validateReleaseReport(report, definition))
}

func TestReleaseRunnerFailsFastAndRecordsTimeoutTermination(t *testing.T) {
	root, definition, external := releaseRunnerFixture(t)
	executor := &releaseFakeExecutor{failAt: 1, failure: &commandExecutionError{cause: context.DeadlineExceeded, termination: "kill", cleanup: "passed"}}
	report, err := runReleaseProfile(context.Background(), root, executor, definition, external, passedReleaseCleanup)
	require.NoError(t, err)
	assert.Equal(t, []string{"unit"}, executor.calls)
	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "check_failed:unit", report.Reason)
	require.Len(t, report.Checks, 1)
	assert.True(t, report.Checks[0].TimedOut)
	assert.Equal(t, "kill", report.Checks[0].Termination)
}

func TestReleaseRunnerCandidateChangeBlocksPass(t *testing.T) {
	root, definition, external := releaseRunnerFixture(t)
	executor := &releaseFakeExecutor{after: func() {
		require.NoError(t, os.WriteFile(filepath.Join(root, "later"), []byte("later"), 0o600))
		runGit(t, root, "add", "later")
		runGit(t, root, "-c", "user.name=Acceptance Test", "-c", "user.email=acceptance@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "later")
	}}
	report, err := runReleaseProfile(context.Background(), root, executor, definition, external, passedReleaseCleanup)
	require.NoError(t, err)
	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "candidate_state_changed", report.Reason)
}

func TestReleaseExternalPreparationCreatesMissingTemplatesWithoutOverwrite(t *testing.T) {
	root, definition := releaseReportTestRepository(t)
	exclude := filepath.Join(root, ".git", "info", "exclude")
	require.NoError(t, os.WriteFile(exclude, []byte("/.design/\n"), 0o600))
	unavailable := func(releaseExternalEvidenceDefinition) (releaseEnvironmentProbeResult, error) {
		return releaseEnvironmentProbeResult{Availability: "unavailable", UnavailableReason: "platform_mismatch", ObservedOS: "linux", ObservedArch: "amd64"}, nil
	}
	require.NoError(t, prepareAndQualifyReleaseExternalEvidence(context.Background(), root, definition, unavailable))
	path := filepath.Join(root, filepath.FromSlash(releaseExternalEvidencePath(definition.ExternalEvidence[0].BehaviorID)))
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, prepareAndQualifyReleaseExternalEvidence(context.Background(), root, definition, func(releaseExternalEvidenceDefinition) (releaseEnvironmentProbeResult, error) {
		return releaseEnvironmentProbeResult{}, errors.New("existing evidence must not be reprobed")
	}))
	second, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

func TestReleaseRunnerCleanupFailureBlocksPass(t *testing.T) {
	root, definition, external := releaseRunnerFixture(t)
	report, err := runReleaseProfile(context.Background(), root, &releaseFakeExecutor{}, definition, external, func() ReleaseCleanupResult {
		return ReleaseCleanupResult{Processes: false, Listeners: false, TemporaryRoots: true, Err: errors.New("survivor")}
	})
	require.NoError(t, err)
	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "release_cleanup_failed", report.Reason)
	assert.Equal(t, "failed", report.Cleanup.Status)
}

func releaseRunnerFixture(t *testing.T) (string, releaseProfileDefinition, []releaseExternalEvidenceReference) {
	t.Helper()
	root, definition := releaseReportTestRepository(t)
	exclude := filepath.Join(root, ".git", "info", "exclude")
	require.NoError(t, os.WriteFile(exclude, []byte("/.design/\n"), 0o600))
	profileHash, _, manifestHash, err := releaseDefinitionHashes(root, definition)
	require.NoError(t, err)
	revision, err := gitOutput(t.Context(), root, "rev-parse", "HEAD")
	require.NoError(t, err)
	binding := releaseExternalEvidenceBinding{CandidateHead: revision, ProfileHash: profileHash, ManifestHash: manifestHash}
	templates, err := generateReleaseExternalEvidenceTemplates(binding, definition.ExternalEvidence, func(releaseExternalEvidenceDefinition) (releaseEnvironmentProbeResult, error) {
		return releaseEnvironmentProbeResult{Availability: "unavailable", UnavailableReason: "platform_mismatch", ObservedOS: "linux", ObservedArch: "amd64"}, nil
	})
	require.NoError(t, err)
	for _, template := range templates {
		path := filepath.Join(root, filepath.FromSlash(template.Path))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, template.Contents, 0o600))
	}
	external, err := loadReleaseExternalEvidence(root, binding, definition.ExternalEvidence)
	require.NoError(t, err)
	return root, definition, external
}

func passedReleaseCleanup() ReleaseCleanupResult {
	return ReleaseCleanupResult{Processes: true, Listeners: true, TemporaryRoots: true}
}
