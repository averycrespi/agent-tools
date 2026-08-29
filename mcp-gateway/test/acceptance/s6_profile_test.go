package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6AcceptanceProfile(t *testing.T) {
	t.Run("closed direct profile", func(t *testing.T) {
		commands, err := ProfileCommands(ProfileS6)
		require.NoError(t, err)
		require.Len(t, commands, 16)
		assert.Equal(t, S6FinalCheckIDs, func() []string {
			names := make([]string, len(commands))
			for index, command := range commands {
				names[index] = command.CheckName
			}
			return names
		}())
		assert.Equal(t, []string{
			"npm run format:check", "make -C mcp-gateway verify", "make -C mcp-gateway test-frontend-s6", "make -C mcp-gateway test-unit-s6",
			"make -C mcp-gateway test-integration-s6", "make -C mcp-gateway test-browser-s6-workflows", "make -C mcp-gateway test-browser-s6-visual",
			"make -C mcp-gateway test-browser-s6-a11y", "make -C mcp-gateway test-browser-s6-cross", "make -C mcp-gateway test-cli-e2e-s6",
			"make -C mcp-gateway test-security-s6", "go -C mcp-gateway tool govulncheck ./...", "npm audit --audit-level=high",
			"make -C mcp-gateway test-keyring-native", "make check-other-tools", "git diff --check 63618c0c7bac399c872d3b069ddd2e2b923cd12f..HEAD --",
		}, commandStrings(commands))
		assert.Equal(t, 11*time.Minute+20*time.Second, s6ProfileBudget())
		deadline, reserve, ok := ProfileDeadline(ProfileS6)
		require.True(t, ok)
		assert.Equal(t, 15*time.Minute, deadline)
		assert.Equal(t, 15*time.Second, reserve)
		assert.Less(t, s6ProfileBudget(), deadline-reserve)

		owners := make(map[string]int)
		criteria := make(map[string]bool)
		for _, command := range commands {
			for _, evidence := range command.Evidence {
				owners[evidence]++
			}
			for _, criterion := range command.Criteria {
				criteria[criterion] = true
			}
			joined := commandString(command)
			assert.NotContains(t, joined, "accept-s2-1")
			assert.NotContains(t, joined, "accept-s3")
			assert.NotContains(t, joined, "accept-s4")
			assert.NotContains(t, joined, "accept-s5")
			assert.NotContains(t, joined, "mcp-gateway audit")
			assert.NotEqual(t, "make check", joined)
		}
		for _, manifest := range contract.S6AcceptanceEvidenceManifest() {
			assert.True(t, criteria[manifest.Criterion], manifest.Criterion)
			for _, evidence := range manifest.Evidence {
				assert.Equal(t, 1, owners[evidence], evidence)
			}
		}
		for _, clause := range contract.S6ClauseEvidenceManifest() {
			for _, evidence := range clause.Evidence {
				assert.Equal(t, 1, owners[evidence], evidence)
			}
		}
		for _, owner := range S6FinalInventory() {
			assert.Equal(t, "executable", owner.Status, owner.ID)
			assert.Equal(t, s6FinalBudgets[owner.ID], owner.Budget, owner.ID)
			require.Len(t, owner.Leaves, 1)
			assert.Equal(t, s6FinalArgv[owner.ID], owner.Leaves[0].Argv, owner.ID)
		}
	})

	t.Run("report and no-check adoption", func(t *testing.T) {
		root := initializedRepository(t)
		writeUnavailableS6Evidence(t, root)
		native := keyringnative.NewResult(keyringnative.ResultSkipped, "linux", "linux_prerequisites_unavailable", keyringnative.ResultPassed, keyringnative.ResultSkipped)
		executor := &fakeExecutor{native: native}
		report := RunProfile(context.Background(), root, executor, ProfileS6, false)
		require.Equal(t, ResultPassed, report.Result)
		assert.Equal(t, ProfileS6, report.Profile)
		assert.Equal(t, S6InventoryDefinitionHash(), report.ManifestHash)
		assert.Equal(t, contract.S6AcceptanceEvidenceManifest(), report.EvidenceManifest)
		assert.Equal(t, contract.S6ClauseEvidenceManifest(), report.ClauseManifest)
		require.Len(t, report.Checks, 16)
		require.Len(t, report.ExternalEvidence, 2)
		assert.Equal(t, 16, executor.calls)
		assertReportValid(t, report)

		reportPath := filepath.Join(t.TempDir(), "report.json")
		require.NoError(t, WriteReport(reportPath, report))
		before, err := os.ReadFile(reportPath)
		require.NoError(t, err)
		adoptionPath := filepath.Join(t.TempDir(), "adoption.json")
		adoption, err := AdoptReport(root, reportPath, adoptionPath, time.Now)
		require.NoError(t, err)
		assert.Equal(t, 16, executor.calls, "adoption must not execute checks")
		digest := sha256.Sum256(before)
		assert.Equal(t, hex.EncodeToString(digest[:]), adoption.ReportSHA256)
		assert.Equal(t, report.ManifestHash, adoption.ManifestHash)
		assert.Equal(t, report.ExternalEvidence, adoption.ExternalEvidence)
		assert.Len(t, adoption.Criteria, 11)
		assert.Len(t, adoption.Clauses, 90)

		referencePath := filepath.Join(root, report.ExternalEvidence[0].Path)
		externalBefore, err := os.ReadFile(referencePath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(referencePath, []byte("stale"), 0o600))
		_, err = AdoptReport(root, reportPath, filepath.Join(t.TempDir(), "stale-evidence.json"), time.Now)
		assert.ErrorContains(t, err, "external evidence")
		require.NoError(t, os.WriteFile(referencePath, externalBefore, 0o600))

		require.NoError(t, os.WriteFile(filepath.Join(root, "later"), []byte("later"), 0o600))
		runGit(t, root, "add", "later")
		runGit(t, root, "-c", "user.name=Acceptance Test", "-c", "user.email=acceptance@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "later")
		_, err = AdoptReport(root, reportPath, filepath.Join(t.TempDir(), "stale-revision.json"), time.Now)
		assert.ErrorContains(t, err, "revision")
	})

	t.Run("failure and drift rejection", func(t *testing.T) {
		root := initializedRepository(t)
		writeUnavailableS6Evidence(t, root)
		report := RunProfile(context.Background(), root, timeoutExecutor{}, ProfileS6, false)
		assert.Equal(t, ResultFailed, report.Result)
		require.Len(t, report.Checks, 1)
		assert.True(t, report.Checks[0].TimedOut)
		assertReportValid(t, report)

		report = RunProfile(context.Background(), root, cleanupFailureExecutor{}, ProfileS6, false)
		assert.Equal(t, ResultFailed, report.Result)
		require.Len(t, report.Checks, 1)
		assert.Equal(t, "failed", report.Checks[0].Cleanup)
		assertReportValid(t, report)

		require.NoError(t, os.WriteFile(filepath.Join(root, "tracked"), []byte("dirty"), 0o600))
		report = RunProfile(context.Background(), root, &fakeExecutor{}, ProfileS6, false)
		assert.Equal(t, "dirty_workspace", report.Reason)
	})

	t.Run("targets and qualifier are frozen", func(t *testing.T) {
		root := repositoryRoot(t)
		makefile, err := os.ReadFile(filepath.Join(root, "mcp-gateway", "Makefile"))
		require.NoError(t, err)
		text := string(makefile)
		for _, target := range []string{"test-frontend-s6", "test-unit-s6", "test-integration-s6", "test-browser-s6-workflows", "test-browser-s6-visual", "test-browser-s6-a11y", "test-browser-s6-cross", "test-cli-e2e-s6", "test-security-s6", "qualify-external-s6", "accept-s6"} {
			assert.Equal(t, 1, strings.Count(text, "\n"+target+":"), target)
		}
		assert.Contains(t, text, "go run ./test/acceptance/cmd --profile s6 --output \"$(REPORT)\"")
		assert.Contains(t, text, "go run ./test/acceptance/cmd --qualify-external")
		assert.NotContains(t, text[textIndex(t, text, "accept-s6:"):], "accept-s5")
		finalTargets := text[textIndex(t, text, "test-frontend-s6:"):textIndex(t, text, "accept-s2-1:")]
		assert.NotContains(t, finalTargets, "test-unit-s6:\n\t$(MAKE)")
		assert.NotContains(t, finalTargets, "-count=2")
		assert.NotContains(t, finalTargets, "-count=10")
		assert.NotContains(t, finalTargets, "-count=20")

		moduleRoot := filepath.Join(root, "mcp-gateway")
		list := exec.Command("go", "list", "./...")
		list.Dir = moduleRoot
		packages, err := list.Output()
		require.NoError(t, err)
		assert.Len(t, strings.Fields(string(packages)), 36)
		assert.Equal(t, 25, countListedTests(t, moduleRoot, "e2e,browser", `^TestBrowser(Protocol|FragmentStorage|AuthenticationEpoch|ReadGeneration|MutationState|ShellPrimitives|SecretSinks|Overview|Invocations|SystemStatus|ServerCatalogReads|ServerCreateUpdate|ServerOperations|ServerCredentials|AuthFlows|ServerDisconnectDelete|Principals|PrincipalCredentials|GrantReadsCreate|GrantCorrection|RequestReads|RequestAdjudication|AdminCredentials|Backups|CapabilityAudit)$`, "./test/e2e"))
		assert.Equal(t, 1, countListedTests(t, moduleRoot, "e2e,browser", `^TestS6BrowserVisualChanged$`, "./test/e2e"))
		assert.Equal(t, 1, countListedTests(t, moduleRoot, "e2e,browser", `^TestS6BrowserAccessibilityChanged$`, "./test/e2e"))
		assert.Equal(t, 1, countListedTests(t, moduleRoot, "e2e,browser", `^TestS6BrowserCross$`, "./test/e2e"))
		assert.Equal(t, 13, countListedTests(t, moduleRoot, "e2e", `^TestCLI(StatusInvocations|ServerCatalogReads|ServerCreateUpdate|ServerDelete|ServerOperations|ServerCredentials|AuthFlows|AdminCredentials|Backups|Principals|PrincipalCredentials|Grants|GrantRequests)$`, "./test/e2e"))
		assert.Equal(t, 15, countListedTests(t, moduleRoot, "integration", `^(TestInvocationRepositoryReadsIntegration|Test(ApprovalVsPolicyAndCapacity|AtomicApprovalAndPostCommitRecovery|CatalogEvidenceReplacement|CreateVsTargetAndPolicy|CredentialAdmissionOrdering|RequestLifecycle|RequestRetentionAndEvidenceLimits)Integration|TestRequestSchemaMigrationUsesRealSQLite|TestRestoreAcceptedSchemaLineages|TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline|TestBusyBeyondDeadlineLatchesMutationAcrossRestart|TestRepositoryRetainsNewest4096ByMonotonicSequence|TestRepositoryRollsBackEvictionWhenInsertFails|TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss)$`, "./internal/storage", "./internal/backup", "./internal/grantrequests", "./internal/authorization", "./internal/catalog", "./internal/invocation", "./internal/composition"))
	})

	t.Run("definition and report drift are rejected", func(t *testing.T) {
		root := initializedRepository(t)
		writeUnavailableS6Evidence(t, root)
		native := keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed)
		report := RunProfile(context.Background(), root, &fakeExecutor{native: native}, ProfileS6, false)
		require.Equal(t, ResultPassed, report.Result)
		require.NoError(t, ValidateReportDefinitions(root, report))
		report.ManifestHash = "sha256:" + strings.Repeat("0", 64)
		encoded, err := json.Marshal(report)
		require.NoError(t, err)
		_, err = Parse(encoded)
		assert.ErrorContains(t, err, "manifest hash")

		report.ManifestHash = S6InventoryDefinitionHash()
		report.ExternalEvidence[0].Availability = "available"
		report.ExternalEvidence[0].Result = "unavailable"
		encoded, err = json.Marshal(report)
		require.NoError(t, err)
		_, err = Parse(encoded)
		assert.ErrorContains(t, err, "availability")
		report.ExternalEvidence[0].Availability = "unavailable_additive"
		report.ExternalEvidence[0].Result = "unavailable"
		report.ExternalEvidence[0].SHA256 = "sha256:invalid"
		encoded, err = json.Marshal(report)
		require.NoError(t, err)
		_, err = Parse(encoded)
		assert.Error(t, err)

		require.NoError(t, os.WriteFile(filepath.Join(root, "mcp-gateway", "Makefile"), []byte("definition drift"), 0o600))
		assert.ErrorContains(t, ValidateReportDefinitions(root, report), "definition hash")
	})
}

func countListedTests(t *testing.T, moduleRoot, tags, selector string, packages ...string) int {
	t.Helper()
	arguments := []string{"test", "-tags=" + tags, "-list", selector}
	arguments = append(arguments, packages...)
	command := exec.Command("go", arguments...)
	command.Dir = moduleRoot
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	count := 0
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "Test") {
			count++
		}
	}
	return count
}

func writeUnavailableS6Evidence(t *testing.T, root string) {
	t.Helper()
	runGit(t, root, "config", "core.excludesFile", filepath.Join(root, ".git", "info", "exclude"))
	exclude := filepath.Join(root, ".git", "info", "exclude")
	require.NoError(t, os.WriteFile(exclude, []byte(".design/\n"), 0o600))
	revision := gitValue(t, root, "rev-parse", "HEAD")
	profileHash, err := ProfileDefinitionHash(ProfileS6)
	require.NoError(t, err)
	binding := S6EvidenceBinding{CandidateHead: revision, ManifestHash: S6InventoryDefinitionHash(), ProfileHash: "sha256:" + profileHash}
	directory := filepath.Join(root, s6ExternalEvidenceDirectory)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	for _, required := range s6ExternalEvidenceFiles {
		evidence := S6ExternalEvidence{
			SchemaVersion: S6ExternalEvidenceSchemaVersion, Kind: required.Kind, CellID: required.CellID,
			CandidateHead: binding.CandidateHead, ManifestHash: binding.ManifestHash, ProfileHash: binding.ProfileHash,
			Operator:     S6EvidenceOperator{Name: "Named external operator"},
			Environment:  S6EvidenceEnvironment{OS: "macos", Browser: "safari"},
			Availability: "unavailable_additive", Result: "unavailable",
			Failure: &S6EvidenceFailure{Blocking: false, Summary: "No named macOS environment is available."},
		}
		contents, marshalErr := json.Marshal(evidence)
		require.NoError(t, marshalErr)
		require.NoError(t, os.WriteFile(filepath.Join(directory, required.Name), contents, 0o600))
	}
	references, err := QualifyS6ExternalEvidence(context.Background(), root)
	require.NoError(t, err)
	require.Len(t, references, 2)
}

func gitValue(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	value, err := gitOutput(context.Background(), root, arguments...)
	require.NoError(t, err)
	return value
}

func textIndex(t *testing.T, text, needle string) int {
	t.Helper()
	index := strings.Index(text, needle)
	require.GreaterOrEqual(t, index, 0)
	return index
}
