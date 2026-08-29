package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrontendDevelopmentTargetDryRun(t *testing.T) {
	root := frontendDevelopmentModuleRoot(t)
	command := exec.Command("make", "-n", "-C", root, "test-frontend-development")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	lines := make([]string, 0, 2)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.HasPrefix(line, "make[") || strings.HasPrefix(line, "make: ") {
			continue
		}
		lines = append(lines, line)
	}
	expected := make([]string, 0, len(FrontendDevelopmentLeaves))
	for _, leaf := range FrontendDevelopmentLeaves {
		expected = append(expected, leaf.Command)
	}
	require.Equal(t, expected, lines)
	joined := strings.Join(lines, " ")
	for _, forbidden := range []string{
		"test-frontend-s6", "test-browser-s6-workflows", "ui:build", "ui:verify-generated", "ui:verify-supply-chain", "TestS6",
	} {
		assert.NotContains(t, joined, forbidden)
	}
}

func TestFrontendDevelopmentInventory(t *testing.T) {
	assert.Equal(t, 1, FrontendDevelopmentInventoryVersion)
	assert.Equal(t, "test-frontend-development", FrontendDevelopmentTarget)
	assert.Equal(t, "M4 combined checkpoint", FrontendDevelopmentAggregatePhase)
	assert.Equal(t, 1, FrontendDevelopmentAggregateRuns)
	assert.Equal(t, 1, FrontendDevelopmentStructuralRuns)
	assert.Equal(t, 150*time.Second, FrontendDevelopmentBudget)
	require.Len(t, FrontendDevelopmentLeaves, 2)

	seen := make(map[string]bool)
	gatewayStarts := 0
	chromiumStarts := 0
	for _, leaf := range FrontendDevelopmentLeaves {
		assert.False(t, seen[leaf.ID], leaf.ID)
		seen[leaf.ID] = true
		assert.NotEmpty(t, leaf.Command, leaf.ID)
		assert.Positive(t, leaf.Timeout, leaf.ID)
		assert.LessOrEqual(t, leaf.Timeout, FrontendDevelopmentBudget, leaf.ID)
		gatewayStarts += leaf.GatewayStarts
		chromiumStarts += leaf.ChromiumStarts
	}
	assert.Equal(t, 15, FrontendDevelopmentLeaves[0].ExpectedMatches)
	assert.Equal(t, 2, FrontendDevelopmentLeaves[1].ExpectedMatches)
	assert.Equal(t, 2, gatewayStarts)
	assert.Equal(t, 2, chromiumStarts)
	assert.Zero(t, FrontendDevelopmentLeaves[0].GatewayStarts)
	assert.Zero(t, FrontendDevelopmentLeaves[0].ChromiumStarts)
	assert.Contains(t, FrontendDevelopmentLeaves[1].Command, "-count=1")
	assert.Contains(t, FrontendDevelopmentLeaves[1].Command, "-timeout=2m")

	root := frontendDevelopmentModuleRoot(t)
	nodeTests, err := filepath.Glob(filepath.Join(root, "web", "tests", "*.test.ts"))
	require.NoError(t, err)
	nodeMatches := 0
	for _, path := range nodeTests {
		contents, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		nodeMatches += strings.Count(string(contents), "\ntest(")
	}
	assert.Equal(t, FrontendDevelopmentLeaves[0].ExpectedMatches, nodeMatches)
	browserTests, err := filepath.Glob(filepath.Join(root, "test", "e2e", "frontend_*_test.go"))
	require.NoError(t, err)
	browserMatches := 0
	for _, path := range browserTests {
		contents, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		browserMatches += strings.Count(string(contents), "\nfunc TestFrontendDevelopment")
	}
	assert.Equal(t, FrontendDevelopmentLeaves[1].ExpectedMatches, browserMatches)
}

func TestFrontendDevelopmentHandoff(t *testing.T) {
	checkpoint := strings.Repeat("a", 40)
	handoff, err := NewFrontendDevelopmentHandoff(checkpoint)
	require.NoError(t, err)
	assert.Equal(t, 1, handoff.SchemaVersion)
	assert.Equal(t, "frontend", handoff.Namespace)
	assert.Equal(t, ".design/plans/2026-08-29-frontend-live-reload-development.md", handoff.PlanPath)
	assert.Equal(t, checkpoint, handoff.CheckpointSHA)
	assert.Equal(t, FrontendDevelopmentTarget, handoff.AggregateTarget)
	assert.Equal(t, 1, handoff.AggregateRuns)
	assert.Equal(t, []string{
		"make -C mcp-gateway test-frontend-development",
		"make -C mcp-gateway test-cli-usability-e2e",
	}, handoff.CombinedCheckpointCommands)
	require.Len(t, handoff.Criteria, 9)
	require.Len(t, handoff.Leaves, 2)

	seenCriteria := make(map[string]bool)
	for index, owner := range handoff.Criteria {
		expectedID := fmt.Sprintf("frontend.AC-%d", index+1)
		assert.Equal(t, expectedID, owner.ID)
		assert.False(t, seenCriteria[owner.ID], owner.ID)
		seenCriteria[owner.ID] = true
		assert.NotEmpty(t, owner.PrimaryOwner, owner.ID)
		assert.NotEmpty(t, owner.Command, owner.ID)
		assert.NotEmpty(t, owner.Selectors, owner.ID)
	}
	assert.Equal(t, int64(4720), handoff.Leaves[0].MeasuredDurationMilliseconds)
	assert.Equal(t, int64(6880), handoff.Leaves[1].MeasuredDurationMilliseconds)
	assert.Equal(t, 1, handoff.Leaves[0].PreFinalRuns)
	assert.Equal(t, 1, handoff.Leaves[0].FinalRuns)
	assert.Equal(t, 1, handoff.Leaves[1].PreFinalRuns)
	assert.Equal(t, 1, handoff.Leaves[1].FinalRuns)
	assert.Equal(t, 2, handoff.Leaves[1].GatewayStarts)
	assert.Equal(t, 2, handoff.Leaves[1].ChromiumStarts)

	root := filepath.Dir(frontendDevelopmentModuleRoot(t))
	for _, path := range handoff.DefinitionFiles {
		_, statErr := os.Stat(filepath.Join(root, path))
		require.NoError(t, statErr, path)
	}
	for _, leaf := range handoff.Leaves {
		assert.Positive(t, leaf.MeasuredDurationMilliseconds, leaf.ID)
		assert.LessOrEqual(t, leaf.MeasuredDurationMilliseconds, leaf.TimeoutMilliseconds, leaf.ID)
	}
	definitionHash, err := FrontendDevelopmentHandoffHash(handoff)
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^sha256:[0-9a-f]{64}$`), definitionHash)

	first, err := json.Marshal(handoff)
	require.NoError(t, err)
	secondHandoff, err := NewFrontendDevelopmentHandoff(checkpoint)
	require.NoError(t, err)
	second, err := json.Marshal(secondHandoff)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	secondHash, err := FrontendDevelopmentHandoffHash(secondHandoff)
	require.NoError(t, err)
	assert.Equal(t, definitionHash, secondHash)
	_, err = NewFrontendDevelopmentHandoff("not-a-checkpoint")
	assert.Error(t, err)
}

func TestFrontendDevelopmentNodeTimeout(t *testing.T) {
	root := filepath.Dir(frontendDevelopmentModuleRoot(t))
	contents, err := os.ReadFile(filepath.Join(root, "package.json"))
	require.NoError(t, err)
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	require.NoError(t, json.Unmarshal(contents, &manifest))
	assert.Equal(t, "node --test --test-timeout=30000", manifest.Scripts["ui:test-dev"])
}

func frontendDevelopmentModuleRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}
