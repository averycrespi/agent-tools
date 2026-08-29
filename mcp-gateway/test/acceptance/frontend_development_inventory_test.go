package acceptance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
