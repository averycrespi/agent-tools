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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS5AcceptanceProfileClosesCommandsEvidenceAndOutput(t *testing.T) {
	commands, err := ProfileCommands(ProfileS5)
	require.NoError(t, err)
	require.Len(t, commands, 11)
	assert.Equal(t, []string{
		"npm run format:check",
		"make -C mcp-gateway verify",
		"make -C mcp-gateway test-security-s5",
		"make -C mcp-gateway test-unit",
		"make -C mcp-gateway test-integration-s5",
		"make -C mcp-gateway test-stress-s5 STRESS_COUNT=20",
		"make -C mcp-gateway test-e2e",
		"go -C mcp-gateway tool govulncheck ./...",
		"make -C mcp-gateway test-keyring-native",
		"make check-other-tools",
		"git diff --check 85e81320d7aec44e6c8b4a6bee6f2208d539857f..HEAD --",
	}, commandStrings(commands))
	assert.Equal(t, []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second, 90 * time.Second, 30 * time.Second, 4 * time.Minute, 90 * time.Second, 5 * time.Second, 30 * time.Second, 15 * time.Second, time.Second}, commandTimeouts(commands))
	for index, command := range commands {
		assert.Equal(t, index == 8, command.Native)
	}
	nativeFixture := keyringnative.NewResult(keyringnative.ResultSkipped, "linux", "linux_prerequisites_unavailable", keyringnative.ResultPassed, keyringnative.ResultSkipped)
	nativeJSON, err := json.Marshal(nativeFixture)
	require.NoError(t, err)
	parsedNative, err := parseNativeOutput(append(append([]byte("make: Entering directory\n"), nativeJSON...), []byte("\nmake: Leaving directory\n")...))
	require.NoError(t, err)
	assert.Equal(t, nativeFixture, parsedNative)
	makefile, err := os.ReadFile(filepath.Join(repositoryRoot(t), "mcp-gateway", "Makefile"))
	require.NoError(t, err)
	assert.Contains(t, string(makefile), "accept-s5:\n\t@test -n \"$(REPORT)\"")
	assert.Contains(t, string(makefile), "go run ./test/acceptance/cmd --profile s5 --output \"$(REPORT)\"")

	root := initializedRepository(t)
	native := keyringnative.NewResult(keyringnative.ResultSkipped, "linux", "linux_prerequisites_unavailable", keyringnative.ResultPassed, keyringnative.ResultSkipped)
	report := RunProfile(context.Background(), root, &fakeExecutor{native: native}, ProfileS5, false)
	assert.Equal(t, ResultPassed, report.Result)
	assert.Equal(t, ProfileS5, report.Profile)
	assert.Len(t, report.EvidenceManifest, 7)
	assert.Len(t, report.ClauseManifest, 106)
	require.Len(t, report.Checks, 11)
	for index, check := range report.Checks {
		assert.Equal(t, commands[index].Timeout.Milliseconds(), check.TimeoutMillis)
	}
	path := filepath.Join(t.TempDir(), "nested", "report.json")
	require.NoError(t, WriteReport(path, report))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = Parse(contents)
	require.NoError(t, err)
}

func TestS5ReportAdopterRevalidatesWithoutExecutingChecks(t *testing.T) {
	root := initializedRepository(t)
	native := keyringnative.NewResult(keyringnative.ResultSkipped, "linux", "linux_prerequisites_unavailable", keyringnative.ResultPassed, keyringnative.ResultSkipped)
	executor := &fakeExecutor{native: native}
	report := RunProfile(context.Background(), root, executor, ProfileS5, false)
	require.Equal(t, ResultPassed, report.Result)
	calls := executor.calls
	reportPath := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, WriteReport(reportPath, report))
	before, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	adoptionPath := filepath.Join(t.TempDir(), "nested", "adoption.json")
	adoption, err := AdoptReport(root, reportPath, adoptionPath, func() time.Time { return time.Date(2026, time.August, 28, 0, 0, 0, 0, time.UTC) })
	require.NoError(t, err)
	assert.Equal(t, calls, executor.calls)
	after, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	sum := sha256.Sum256(before)
	assert.Equal(t, hex.EncodeToString(sum[:]), adoption.ReportSHA256)
	assert.Equal(t, ProfileS5, adoption.Profile)
	assert.True(t, adoption.CleanAfter)
	assert.Equal(t, keyringnative.ResultSkipped, adoption.NativeResult)
	assert.Len(t, adoption.Criteria, 7)
	assert.Len(t, adoption.Clauses, 106)
	_, err = AdoptReport(root, reportPath, reportPath, time.Now)
	require.ErrorContains(t, err, "distinct")
}

func TestS5ProfileDeadlineBoundsEveryCheck(t *testing.T) {
	deadline, reserve, ok := ProfileDeadline(ProfileS5)
	require.True(t, ok)
	assert.Equal(t, 15*time.Minute, deadline)
	assert.Equal(t, 15*time.Second, reserve)
	commands, err := ProfileCommands(ProfileS5)
	require.NoError(t, err)
	var total time.Duration
	for _, command := range commands {
		require.Positive(t, command.Timeout)
		total += command.Timeout
	}
	assert.Less(t, total, deadline-reserve)
}

func commandTimeouts(commands []Command) []time.Duration {
	values := make([]time.Duration, len(commands))
	for index, command := range commands {
		values[index] = command.Timeout
	}
	return values
}
