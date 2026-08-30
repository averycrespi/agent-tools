package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseReportRoundTripPreservesClosedEvidence(t *testing.T) {
	root, definition := releaseReportTestRepository(t)
	report := validReleaseReport(t, root, definition)
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	parsed, err := parseReleaseReport(encoded, definition)
	require.NoError(t, err)
	assert.Equal(t, report, parsed)
	assert.Equal(t, []string{"product.interface.developer_first", "security.browser.storage"}, parsed.Coverage.ProductBehaviors)
	assert.Equal(t, []string{"cleanup.AC-1", "cleanup.AC-5"}, parsed.Coverage.CleanupCriteria)
	require.NoError(t, validateReleaseReportDefinitions(root, parsed, definition))

	path := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, writeReleaseReport(root, path, report, definition))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestReleaseReportRejectsSchemaSemanticsAndLegacyArtifacts(t *testing.T) {
	root, definition := releaseReportTestRepository(t)
	valid := validReleaseReport(t, root, definition)

	legacy, err := json.Marshal(Report{SchemaVersion: 3, Profile: ProfileS6})
	require.NoError(t, err)
	_, err = parseReleaseReport(legacy, definition)
	assert.ErrorIs(t, err, errLegacyAcceptanceReport)

	mutations := map[string]func(*releaseReport){
		"bare cleanup criterion": func(report *releaseReport) { report.Coverage.CleanupCriteria[0] = "AC-1" },
		"namespace collision":    func(report *releaseReport) { report.Coverage.ProductBehaviors[0] = "cleanup.AC-1" },
		"profile drift":          func(report *releaseReport) { report.ProfileHash = "sha256:" + string(make([]byte, 64)) },
		"manifest drift":         func(report *releaseReport) { report.ManifestHash = "sha256:" + string(make([]byte, 64)) },
		"check reordering":       func(report *releaseReport) { report.Checks[0], report.Checks[1] = report.Checks[1], report.Checks[0] },
		"passed timeout":         func(report *releaseReport) { report.Checks[0].TimedOut = true },
		"passed cleanup failure": func(report *releaseReport) { report.Checks[0].Cleanup = "failed" },
		"report cleanup failure": func(report *releaseReport) { report.Cleanup.Status = "failed" },
		"native failure": func(report *releaseReport) {
			failed := keyringnative.NewResult(keyringnative.ResultFailed, "linux", "failed", keyringnative.ResultFailed, keyringnative.ResultSkipped)
			report.Native = &failed
		},
		"blocking external failure": func(report *releaseReport) { report.ExternalEvidence[0].Result = "failed" },
		"escaping artifact":         func(report *releaseReport) { report.Checks[0].Artifacts[0] = "../escape" },
		"timing mismatch":           func(report *releaseReport) { report.Checks[0].DurationMillis++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := cloneReleaseReport(t, valid)
			mutate(&candidate)
			encoded, marshalErr := json.Marshal(candidate)
			require.NoError(t, marshalErr)
			_, parseErr := parseReleaseReport(encoded, definition)
			assert.Error(t, parseErr)
		})
	}

	skipped := cloneReleaseReport(t, valid)
	nativeSkipped := keyringnative.NewResult(keyringnative.ResultSkipped, "linux", "unavailable", keyringnative.ResultPassed, keyringnative.ResultSkipped)
	skipped.Native = &nativeSkipped
	skipped.ExternalEvidence[0].Availability = "unavailable"
	skipped.ExternalEvidence[0].Result = "unavailable"
	skipped.ExternalEvidence[0].Blocking = false
	encoded, err := json.Marshal(skipped)
	require.NoError(t, err)
	_, err = parseReleaseReport(encoded, definition)
	require.NoError(t, err)

	encoded, err = json.Marshal(valid)
	require.NoError(t, err)
	encoded = append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)
	_, err = parseReleaseReport(encoded, definition)
	assert.Error(t, err)
	_, err = parseReleaseReport(append(encoded, []byte(` {}`)...), definition)
	assert.Error(t, err)
}

func TestReleaseReportDefinitionDriftAndAtomicPublication(t *testing.T) {
	root, definition := releaseReportTestRepository(t)
	report := validReleaseReport(t, root, definition)
	require.NoError(t, validateReleaseReportDefinitions(root, report, definition))

	definitionPath := filepath.Join(root, "definitions", "transitive.txt")
	require.NoError(t, os.WriteFile(definitionPath, []byte("drift"), 0o600))
	assert.ErrorContains(t, validateReleaseReportDefinitions(root, report, definition), "definition hash")

	path := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, os.WriteFile(path, []byte("sentinel"), 0o600))
	require.ErrorContains(t, writeReleaseReport(root, path, report, definition), "definition hash")
	require.NoError(t, os.WriteFile(definitionPath, []byte("transitive"), 0o600))
	report.ProfileHash = "sha256:" + string(make([]byte, 64))
	require.Error(t, writeReleaseReport(root, path, report, definition))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, []byte("sentinel"), contents)
}

func TestReleaseReportDefinitionsRejectUnsafeInventory(t *testing.T) {
	root, definition := releaseReportTestRepository(t)
	for name, paths := range map[string][]string{
		"duplicate": {"definitions/direct.txt", "definitions/direct.txt"},
		"escaping":  {"../escape"},
		"missing":   {"definitions/missing.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := definition
			candidate.DefinitionFiles = paths
			_, _, _, err := releaseDefinitionHashes(root, candidate)
			assert.Error(t, err)
		})
	}
	symlink := filepath.Join(root, "definitions", "alias.txt")
	require.NoError(t, os.Symlink("direct.txt", symlink))
	candidate := definition
	candidate.DefinitionFiles = []string{"definitions/alias.txt"}
	_, _, _, err := releaseDefinitionHashes(root, candidate)
	assert.ErrorContains(t, err, "not a regular file")
}

func TestReleaseReportAdoptionIsAtomicAndRunsNoChecks(t *testing.T) {
	root, definition := releaseReportTestRepository(t)
	report := validReleaseReport(t, root, definition)
	reportPath := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, writeReleaseReport(root, reportPath, report, definition))
	original, err := os.ReadFile(reportPath)
	require.NoError(t, err)

	output := filepath.Join(t.TempDir(), "adoption.json")
	adoption, err := adoptReleaseReport(root, reportPath, output, definition, func([]releaseExternalEvidenceReference) error { return nil }, func() time.Time {
		return time.Date(2026, time.August, 30, 2, 0, 0, 0, time.UTC)
	})
	require.NoError(t, err)
	digest := sha256.Sum256(original)
	assert.Equal(t, "sha256:"+hex.EncodeToString(digest[:]), adoption.ReportSHA256)
	assert.Equal(t, report.Coverage, adoption.Coverage)
	assert.Equal(t, keyringnative.ResultPassed, adoption.NativeResult)
	info, err := os.Stat(output)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	after, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	assert.Equal(t, original, after)

	_, err = adoptReleaseReport(root, reportPath, reportPath, definition, nil, time.Now)
	assert.ErrorContains(t, err, "distinct")

	dirtyPath := filepath.Join(root, "dirty")
	require.NoError(t, os.WriteFile(dirtyPath, []byte("dirty"), 0o600))
	_, err = adoptReleaseReport(root, reportPath, filepath.Join(t.TempDir(), "dirty.json"), definition, nil, time.Now)
	assert.ErrorContains(t, err, "clean workspace")
	require.NoError(t, os.Remove(dirtyPath))
	require.NoError(t, os.WriteFile(filepath.Join(root, "later"), []byte("later"), 0o600))
	runGit(t, root, "add", "later")
	runGit(t, root, "-c", "user.name=Acceptance Test", "-c", "user.email=acceptance@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "later")
	_, err = adoptReleaseReport(root, reportPath, filepath.Join(t.TempDir(), "later.json"), definition, nil, time.Now)
	assert.ErrorContains(t, err, "revision")
}

func TestReleaseReportAdoptionPreservesOutputWhenFinalValidationFails(t *testing.T) {
	root, definition := releaseReportTestRepository(t)
	report := validReleaseReport(t, root, definition)
	reportPath := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, writeReleaseReport(root, reportPath, report, definition))
	output := filepath.Join(t.TempDir(), "adoption.json")
	require.NoError(t, os.WriteFile(output, []byte("sentinel"), 0o600))

	calls := 0
	validator := func([]releaseExternalEvidenceReference) error {
		calls++
		if calls > 1 {
			return errors.New("evidence changed during adoption")
		}
		return nil
	}
	_, err := adoptReleaseReport(root, reportPath, output, definition, validator, time.Now)
	require.Error(t, err)
	contents, readErr := os.ReadFile(output)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("sentinel"), contents)
	temporary, globErr := filepath.Glob(filepath.Join(filepath.Dir(output), ".release-*"))
	require.NoError(t, globErr)
	assert.Empty(t, temporary)
}

func releaseReportTestRepository(t *testing.T) (string, releaseProfileDefinition) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "definitions"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "definitions", "direct.txt"), []byte("direct"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "definitions", "transitive.txt"), []byte("transitive"), 0o600))
	runGit(t, root, "init")
	runGit(t, root, "add", "definitions/direct.txt", "definitions/transitive.txt")
	runGit(t, root, "-c", "user.name=Acceptance Test", "-c", "user.email=acceptance@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "initial")
	return root, releaseProfileDefinition{
		Profile:  releaseProfile,
		Coverage: releaseCoverage{ProductBehaviors: []string{"product.interface.developer_first", "security.browser.storage"}, CleanupCriteria: []string{"cleanup.AC-1", "cleanup.AC-5"}},
		Checks: []releaseCheckDefinition{
			{ID: "unit", Argv: []string{"make", "-C", "mcp-gateway", "test-unit"}, Coverage: releaseCoverage{ProductBehaviors: []string{"product.interface.developer_first"}, CleanupCriteria: []string{"cleanup.AC-1"}}, TimeoutMillis: 90000, Artifacts: []string{"artifacts/unit.json"}, CleanupRequirements: []string{"processes", "listeners", "temporary roots"}},
			{ID: "native", Argv: []string{"make", "-C", "mcp-gateway", "test-keyring-native"}, Coverage: releaseCoverage{ProductBehaviors: []string{"security.browser.storage"}, CleanupCriteria: []string{"cleanup.AC-5"}}, TimeoutMillis: 10000, Artifacts: []string{"artifacts/native.json"}, CleanupRequirements: []string{"processes", "temporary roots"}, Native: true},
		},
		ExternalEvidence: []releaseExternalEvidenceDefinition{{BehaviorID: "tier.browser.cross", CellID: "macos-safari", TargetOS: "macos", Browser: "safari", AcceptanceClass: "blocking_when_available", UnavailableClass: "additive", ExecutableLocator: "/Applications/Safari.app", Checklist: []string{"sign-in-session"}}},
		DefinitionFiles:  []string{"definitions/direct.txt", "definitions/transitive.txt"},
	}
}

func validReleaseReport(t *testing.T, root string, definition releaseProfileDefinition) releaseReport {
	t.Helper()
	profileHash, definitionHash, manifestHash, err := releaseDefinitionHashes(root, definition)
	require.NoError(t, err)
	revision, err := gitOutput(t.Context(), root, "rev-parse", "HEAD")
	require.NoError(t, err)
	start := time.Date(2026, time.August, 30, 1, 0, 0, 0, time.UTC)
	native := keyringnative.NewResult(keyringnative.ResultPassed, "linux", "native_passed", keyringnative.ResultPassed, keyringnative.ResultPassed)
	checks := make([]releaseCheck, len(definition.Checks))
	for index, check := range definition.Checks {
		checkStart := start.Add(time.Duration(index) * time.Millisecond)
		checks[index] = releaseCheck{ID: check.ID, Status: ResultPassed, Argv: append([]string(nil), check.Argv...), Coverage: check.Coverage, Artifacts: append([]string(nil), check.Artifacts...), StartedAt: checkStart.Format(time.RFC3339Nano), EndedAt: checkStart.Add(time.Millisecond).Format(time.RFC3339Nano), DurationMillis: 1, TimeoutMillis: check.TimeoutMillis, Termination: "none", Cleanup: "passed", CleanupRequirements: append([]string(nil), check.CleanupRequirements...), DiagnosticPaths: []string{}}
	}
	return releaseReport{SchemaVersion: releaseReportSchemaVersion, Profile: releaseProfile, ProfileHash: profileHash, CommandDefinitionHash: definitionHash, ManifestHash: manifestHash, Result: ResultPassed, Reason: "all_checks_passed", Revision: revision, Dirty: false, CleanBefore: true, CleanAfter: true, StartedAt: start.Format(time.RFC3339Nano), EndedAt: start.Add(2 * time.Millisecond).Format(time.RFC3339Nano), DurationMillis: 2, Coverage: definition.Coverage, Checks: checks, Native: &native, ExternalEvidence: []releaseExternalEvidenceReference{{ID: "tier.browser.cross", CellID: "macos-safari", Availability: "available", Result: "passed", Blocking: true, Path: releaseExternalEvidencePath("tier.browser.cross"), SHA256: "sha256:" + strings.Repeat("a", 64)}}, Cleanup: releaseCleanup{Status: "passed", Processes: true, Listeners: true, TemporaryRoots: true}}
}

func cloneReleaseReport(t *testing.T, report releaseReport) releaseReport {
	t.Helper()
	contents, err := json.Marshal(report)
	require.NoError(t, err)
	var clone releaseReport
	require.NoError(t, json.Unmarshal(contents, &clone))
	return clone
}
