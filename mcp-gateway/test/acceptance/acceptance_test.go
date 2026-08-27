package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeExecutor struct {
	calls  int
	failAt int
	native keyringnative.Result
	onRun  func(int)
}

func (executor *fakeExecutor) Run(_ context.Context, _ string, command Command) ([]byte, error) {
	executor.calls++
	if executor.onRun != nil {
		executor.onRun(executor.calls)
	}
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

func TestS3ProfileMapsTheClosedEvidenceManifestToCommandsAndArtifacts(t *testing.T) {
	commands, err := ProfileCommands(ProfileS3)
	require.NoError(t, err)
	require.Len(t, commands, 10)
	expectedCommands := []string{
		"go -C mcp-gateway test -race ./internal/contract ./internal/strictjson ./internal/storage ./internal/servers",
		"go -C mcp-gateway test -race -count=10 ./internal/authorization ./internal/discovery ./internal/mcpingress",
		"go -C mcp-gateway test -race ./internal/api ./internal/catalog ./internal/composition ./internal/backup ./cmd/mcp-gateway ./internal/downstream ./internal/oauth ./internal/keyring ./test/material ./test/keyringnative",
		"make -C mcp-gateway test-integration",
		"make -C mcp-gateway test-e2e",
		"make -C mcp-gateway audit",
		"go -C mcp-gateway tool govulncheck ./...",
		"mcp-gateway/test/keyring-native.sh",
		"make check",
		"git diff --check",
	}
	actualCommands := make([]string, len(commands))
	for index, command := range commands {
		actualCommands[index] = commandString(command)
	}
	require.Equal(t, expectedCommands, actualCommands)

	root := repositoryRoot(t)
	mapped := map[string]bool{}
	covered := map[string]bool{}
	checkNames := map[string]bool{}
	for _, command := range commands {
		require.NotEmpty(t, command.CheckName)
		require.False(t, checkNames[command.CheckName], command.CheckName)
		checkNames[command.CheckName] = true
		require.NotEmpty(t, command.Criteria, command.CheckName)
		for _, criterion := range command.Criteria {
			covered[criterion] = true
		}
		require.NotEmpty(t, command.Evidence, command.CheckName)
		require.NotEmpty(t, command.Artifacts, command.CheckName)
		for _, evidence := range command.Evidence {
			mapped[evidence] = true
		}
		for _, artifact := range command.Artifacts {
			_, statErr := os.Stat(root + "/" + artifact)
			require.NoError(t, statErr, "%s: %s", command.CheckName, artifact)
		}
	}
	for _, criterion := range contract.AcceptanceEvidenceManifest() {
		require.True(t, covered[criterion.Criterion], criterion.Criterion)
		for _, evidence := range criterion.Evidence {
			require.True(t, mapped[evidence], "%s: %s", criterion.Criterion, evidence)
		}
	}
	makefile, err := os.ReadFile(filepath.Join(root, "mcp-gateway", "Makefile"))
	require.NoError(t, err)
	require.Contains(t, string(makefile), "accept-s3:\n\t@go run ./test/acceptance/cmd --profile s3")
}

func TestS4ProfileMapsClosedCriterionAndClauseEvidenceToCommandsAndArtifacts(t *testing.T) {
	commands, err := ProfileCommands(Profile("s4"))
	require.NoError(t, err)
	require.Len(t, commands, 11)
	require.Equal(t, []string{
		"go -C mcp-gateway test -race ./internal/contract ./internal/invocation ./internal/storage ./internal/composition ./test/material ./test/acceptance",
		"go -C mcp-gateway test -race -count=20 -timeout=18m ./internal/invocation ./internal/authorization",
		"go -C mcp-gateway test -race -count=20 -timeout=18m ./internal/catalog ./internal/downstream ./internal/mcpingress",
		"go -C mcp-gateway test -race -count=20 -timeout=18m ./internal/composition",
		"go -C mcp-gateway test -race -tags=integration ./internal/storage ./internal/backup ./internal/invocation ./internal/authorization ./internal/catalog ./internal/mcpingress ./internal/composition",
		"make -C mcp-gateway test-e2e",
		"make -C mcp-gateway audit",
		"go -C mcp-gateway tool govulncheck ./...",
		"mcp-gateway/test/keyring-native.sh",
		"make check",
		"git diff --check",
	}, commandStrings(commands))

	root := repositoryRoot(t)
	mapped := map[string]bool{}
	covered := map[string]bool{}
	for _, command := range commands {
		require.NotEmpty(t, command.Criteria, command.CheckName)
		require.NotEmpty(t, command.Artifacts, command.CheckName)
		for _, criterion := range command.Criteria {
			covered[criterion] = true
		}
		for _, evidence := range command.Evidence {
			mapped[evidence] = true
		}
		for _, artifact := range command.Artifacts {
			_, statErr := os.Stat(root + "/" + artifact)
			require.NoError(t, statErr, "%s: %s", command.CheckName, artifact)
		}
	}
	for _, criterion := range contract.S4AcceptanceEvidenceManifest() {
		require.True(t, covered[criterion.Criterion], criterion.Criterion)
		for _, evidence := range criterion.Evidence {
			require.True(t, mapped[evidence], "%s: %s", criterion.Criterion, evidence)
		}
	}
	for _, clause := range contract.S4ClauseEvidenceManifest() {
		for _, evidence := range clause.Evidence {
			require.True(t, mapped[evidence], "%s: %s", clause.Clause, evidence)
		}
	}
	makefile, err := os.ReadFile(filepath.Join(root, "mcp-gateway", "Makefile"))
	require.NoError(t, err)
	require.Contains(t, string(makefile), "accept-s4:\n\t@go run ./test/acceptance/cmd --profile s4")
}

func commandStrings(commands []Command) []string {
	result := make([]string, len(commands))
	for index, command := range commands {
		result[index] = commandString(command)
	}
	return result
}

func TestS4RunBindsRevisionProfileManifestsAndAllChecks(t *testing.T) {
	native := keyringnative.NewResult(keyringnative.ResultSkipped, "linux", "linux_prerequisites_unavailable", keyringnative.ResultPassed, keyringnative.ResultSkipped)
	executor := &fakeExecutor{native: native}
	report := RunProfile(context.Background(), repositoryRoot(t), executor, ProfileS4, true)

	assert.Equal(t, ResultPassed, report.Result)
	assert.Equal(t, ProfileS4, report.Profile)
	assert.Regexp(t, `^[0-9a-f]{40}$`, report.Revision)
	assert.Equal(t, contract.S4AcceptanceEvidenceManifest(), report.EvidenceManifest)
	assert.Equal(t, contract.S4ClauseEvidenceManifest(), report.ClauseManifest)
	require.Len(t, report.Checks, 11)
	assert.Equal(t, 11, executor.calls)
	assert.Equal(t, keyringnative.ResultSkipped, report.Native.Result)
	assertReportValid(t, report)
}

func TestS4RunStopsAtFirstFailureAndRejectsDirtyWorkspace(t *testing.T) {
	executor := &fakeExecutor{failAt: 2}
	report := RunProfile(context.Background(), repositoryRoot(t), executor, ProfileS4, true)
	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "repeated_admission_races_failed", report.Reason)
	require.Len(t, report.Checks, 2)
	assert.Equal(t, ResultFailed, report.Checks[1].Status)
	assert.Equal(t, 2, executor.calls)
	assertReportValid(t, report)

	dirtyRoot := initializedRepository(t)
	require.NoError(t, os.WriteFile(filepath.Join(dirtyRoot, "tracked"), []byte("private-canary"), 0o600))
	executor = &fakeExecutor{}
	report = RunProfile(context.Background(), dirtyRoot, executor, ProfileS4, false)
	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "dirty_workspace", report.Reason)
	assert.Zero(t, executor.calls)
	contents, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(contents), "private-canary")
	assertReportValid(t, report)
}

func TestS4ParseRejectsCriterionClauseAndCheckMutation(t *testing.T) {
	native := keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed)
	report := RunProfile(context.Background(), repositoryRoot(t), &fakeExecutor{native: native}, ProfileS4, true)

	report.EvidenceManifest[0].Evidence[0] = "s4-schema"
	contents, err := json.Marshal(report)
	require.NoError(t, err)
	_, err = Parse(contents)
	require.ErrorContains(t, err, "S4 evidence manifest")
	report.EvidenceManifest = contract.S4AcceptanceEvidenceManifest()

	report.ClauseManifest[0].Evidence[0] = "s4-docs"
	contents, err = json.Marshal(report)
	require.NoError(t, err)
	_, err = Parse(contents)
	require.ErrorContains(t, err, "S4 clause manifest")
	report.ClauseManifest = contract.S4ClauseEvidenceManifest()

	report.Checks[0].Evidence[0] = "s4-admission-race"
	contents, err = json.Marshal(report)
	require.NoError(t, err)
	_, err = Parse(contents)
	require.ErrorContains(t, err, "closed profile")
}

func TestS3RunBindsRevisionProfileManifestAndAllChecks(t *testing.T) {
	native := keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed)
	executor := &fakeExecutor{native: native}
	report := RunProfile(context.Background(), repositoryRoot(t), executor, ProfileS3, true)

	assert.Equal(t, ResultPassed, report.Result)
	assert.Equal(t, ProfileS3, report.Profile)
	assert.Regexp(t, `^[0-9a-f]{40}$`, report.Revision)
	assert.Equal(t, contract.AcceptanceEvidenceManifest(), report.EvidenceManifest)
	require.Len(t, report.Checks, 10)
	assert.Equal(t, 10, executor.calls)
	assertReportValid(t, report)
}

func TestS3RunRejectsUnknownProfileAndUnavailableRevision(t *testing.T) {
	report := RunProfile(context.Background(), repositoryRoot(t), &fakeExecutor{}, Profile("unknown"), true)
	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "unknown_profile", report.Reason)

	report = RunProfile(context.Background(), t.TempDir(), &fakeExecutor{}, ProfileS3, true)
	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "git_revision_unavailable", report.Reason)
}

func TestS3RunRejectsDirtyWorkspaceBeforeAndAfterChecks(t *testing.T) {
	dirtyRoot := initializedRepository(t)
	require.NoError(t, os.WriteFile(filepath.Join(dirtyRoot, "tracked"), []byte("dirty"), 0o600))
	executor := &fakeExecutor{}
	report := RunProfile(context.Background(), dirtyRoot, executor, ProfileS3, false)
	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "dirty_workspace", report.Reason)
	assert.Zero(t, executor.calls)
	assertReportValid(t, report)

	changedRoot := initializedRepository(t)
	commands, err := ProfileCommands(ProfileS3)
	require.NoError(t, err)
	executor = &fakeExecutor{
		native: keyringnative.NewResult(keyringnative.ResultSkipped, "linux", "linux_prerequisites_unavailable", keyringnative.ResultPassed, keyringnative.ResultSkipped),
		onRun: func(call int) {
			if call == len(commands) {
				require.NoError(t, os.WriteFile(filepath.Join(changedRoot, "tracked"), []byte("changed"), 0o600))
			}
		},
	}
	report = RunProfile(context.Background(), changedRoot, executor, ProfileS3, false)
	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "acceptance_changed_workspace", report.Reason)
	assert.Equal(t, len(commands), executor.calls)
	assertReportValid(t, report)
}

func TestS3RunClassifiesNativePassedSkippedAndFailed(t *testing.T) {
	for _, test := range []struct {
		name   string
		native keyringnative.Result
		result string
		reason string
	}{
		{name: "passed", native: keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed), result: ResultPassed, reason: "all_checks_passed"},
		{name: "skipped", native: keyringnative.NewResult(keyringnative.ResultSkipped, "linux", "linux_prerequisites_unavailable", keyringnative.ResultPassed, keyringnative.ResultSkipped), result: ResultPassed, reason: "all_checks_passed"},
		{name: "failed", native: keyringnative.NewResult(keyringnative.ResultFailed, "linux", "native_failed", keyringnative.ResultPassed, keyringnative.ResultFailed), result: ResultFailed, reason: "native_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := RunProfile(context.Background(), repositoryRoot(t), &fakeExecutor{native: test.native}, ProfileS3, true)
			assert.Equal(t, test.result, report.Result)
			assert.Equal(t, test.reason, report.Reason)
			assertReportValid(t, report)
		})
	}
}

func TestS3RunStopsAtTheFirstFailedCheck(t *testing.T) {
	executor := &fakeExecutor{failAt: 2}
	report := RunProfile(context.Background(), repositoryRoot(t), executor, ProfileS3, true)

	assert.Equal(t, ResultFailed, report.Result)
	assert.Equal(t, "authority_discovery_ingress_failed", report.Reason)
	require.Len(t, report.Checks, 2)
	assert.Equal(t, ResultFailed, report.Checks[1].Status)
	assert.Equal(t, 2, executor.calls)
	assertReportValid(t, report)
}

func TestParseRejectsSchemaAndManifestMutation(t *testing.T) {
	native := keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed)
	report := RunProfile(context.Background(), repositoryRoot(t), &fakeExecutor{native: native}, ProfileS3, true)
	contents, err := json.Marshal(report)
	require.NoError(t, err)

	var document map[string]any
	require.NoError(t, json.Unmarshal(contents, &document))
	document["unexpected"] = true
	mutated, err := json.Marshal(document)
	require.NoError(t, err)
	_, err = Parse(mutated)
	require.Error(t, err)

	originalArtifact := report.Checks[0].Artifacts[0]
	report.Checks[0].Artifacts[0] = "mcp-gateway/Makefile"
	mutated, err = json.Marshal(report)
	require.NoError(t, err)
	_, err = Parse(mutated)
	require.ErrorContains(t, err, "closed profile")
	report.Checks[0].Artifacts[0] = originalArtifact

	report.EvidenceManifest[0].Evidence[0] = "docs"
	mutated, err = json.Marshal(report)
	require.NoError(t, err)
	_, err = Parse(mutated)
	require.ErrorContains(t, err, "evidence manifest")
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

func initializedRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init")
	require.NoError(t, os.WriteFile(filepath.Join(root, "tracked"), []byte("clean"), 0o600))
	runGit(t, root, "add", "tracked")
	runGit(t, root, "-c", "user.name=Acceptance Test", "-c", "user.email=acceptance@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "initial")
	return root
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
}
