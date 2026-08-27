package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
	"github.com/stretchr/testify/require"
)

func TestS5AcceptanceReportRecordsTimingHashesAndCleanup(t *testing.T) {
	root := initializedRepository(t)
	native := keyringnative.NewResult(keyringnative.ResultSkipped, "linux", "linux_prerequisites_unavailable", keyringnative.ResultPassed, keyringnative.ResultSkipped)
	report := RunProfile(context.Background(), root, &fakeExecutor{native: native}, ProfileS3, false)

	require.Equal(t, 3, report.SchemaVersion)
	require.True(t, report.CleanBefore)
	require.True(t, report.CleanAfter)
	require.NotEmpty(t, report.StartedAt)
	require.NotEmpty(t, report.EndedAt)
	require.Positive(t, report.DurationMillis)
	require.Regexp(t, `^[0-9a-f]{64}$`, report.ProfileHash)
	require.Regexp(t, `^[0-9a-f]{64}$`, report.CommandDefinitionHash)
	for _, check := range report.Checks {
		require.NotEmpty(t, check.StartedAt)
		require.NotEmpty(t, check.EndedAt)
		require.GreaterOrEqual(t, check.DurationMillis, int64(0))
		require.Positive(t, check.TimeoutMillis)
		require.False(t, check.TimedOut)
		require.Equal(t, "none", check.Termination)
		require.Equal(t, "passed", check.Cleanup)
	}
	assertReportValid(t, report)
}

func TestS5AcceptanceReportClassifiesTimeoutAndCleanupFailure(t *testing.T) {
	root := initializedRepository(t)
	report := RunProfile(context.Background(), root, timeoutExecutor{}, ProfileS3, false)
	require.Equal(t, ResultFailed, report.Result)
	require.Len(t, report.Checks, 1)
	require.True(t, report.Checks[0].TimedOut)
	require.Equal(t, "kill", report.Checks[0].Termination)
	require.Equal(t, "passed", report.Checks[0].Cleanup)
	assertReportValid(t, report)

	report = RunProfile(context.Background(), root, cleanupFailureExecutor{}, ProfileS3, false)
	require.Equal(t, ResultFailed, report.Result)
	require.Len(t, report.Checks, 1)
	require.Equal(t, "failed", report.Checks[0].Cleanup)
	assertReportValid(t, report)
}

type timeoutExecutor struct{}

func (timeoutExecutor) Run(context.Context, string, Command) ([]byte, error) {
	return nil, &commandExecutionError{cause: context.DeadlineExceeded, termination: "kill", cleanup: "passed"}
}

type cleanupFailureExecutor struct{}

func (cleanupFailureExecutor) Run(context.Context, string, Command) ([]byte, error) {
	return nil, &commandExecutionError{cause: context.Canceled, termination: "term", cleanup: "failed"}
}

func TestS5CommandDefinitionHashDetectsIndirectDrift(t *testing.T) {
	root := initializedRepository(t)
	native := keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed)
	report := RunProfile(context.Background(), root, &fakeExecutor{native: native}, ProfileS3, false)
	require.NoError(t, ValidateReportDefinitions(root, report))

	path := filepath.Join(root, "mcp-gateway", "test", "acceptance", "report.schema.json")
	require.NoError(t, os.WriteFile(path, []byte("definition drift"), 0o600))
	require.ErrorContains(t, ValidateReportDefinitions(root, report), "definition hash")
}

func TestS5GenericReportAdoptionWritesSeparateAtomicSidecar(t *testing.T) {
	root := initializedRepository(t)
	native := keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed)
	report := RunProfile(context.Background(), root, &fakeExecutor{native: native}, ProfileS3, false)
	reportPath := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, WriteReport(reportPath, report))
	original, err := os.ReadFile(reportPath)
	require.NoError(t, err)

	adoptionPath := filepath.Join(t.TempDir(), "adoption.json")
	adoption, err := AdoptReport(root, reportPath, adoptionPath, time.Now)
	require.NoError(t, err)
	require.Equal(t, 1, adoption.SchemaVersion)
	hash := sha256.Sum256(original)
	require.Equal(t, hex.EncodeToString(hash[:]), adoption.ReportSHA256)
	require.Equal(t, report.Revision, adoption.Revision)
	require.FileExists(t, adoptionPath)
	after, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.Equal(t, original, after)

	var document map[string]any
	require.NoError(t, json.Unmarshal(original, &document))
	document["command_definition_hash"] = string(make([]byte, 64))
	mutated, err := json.Marshal(document)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(reportPath, mutated, 0o600))
	_, err = AdoptReport(root, reportPath, adoptionPath, time.Now)
	require.Error(t, err)

	require.NoError(t, os.WriteFile(reportPath, original, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "later"), []byte("later"), 0o600))
	runGit(t, root, "add", "later")
	runGit(t, root, "-c", "user.name=Acceptance Test", "-c", "user.email=acceptance@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "later")
	_, err = AdoptReport(root, reportPath, adoptionPath, time.Now)
	require.ErrorContains(t, err, "revision")
}

func TestS5AcceptanceReportAtomicWritePreservesExistingOutputOnValidationFailure(t *testing.T) {
	root := initializedRepository(t)
	native := keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed)
	report := RunProfile(context.Background(), root, &fakeExecutor{native: native}, ProfileS3, false)
	path := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, os.WriteFile(path, []byte("sentinel"), 0o600))
	report.ProfileHash = "invalid"
	require.Error(t, WriteReport(path, report))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, []byte("sentinel"), contents)
}
