package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPurposeEvidenceDAGMetadataIsComplete(t *testing.T) {
	dag := purposeEvidenceDAG()
	require.NoError(t, validatePurposeEvidenceDAG(dag))

	expectedLeaves := []string{
		"test-unit", "test-integration", "test-harness", "test-material", "test-serve-temporary", "test-e2e", "test-security", "test-stress", "test-keyring-native",
		"test-browser-workflows", "test-browser-privacy", "test-browser-visual", "test-browser-accessibility", "test-browser-cross",
		"test-frontend-development-node", "test-frontend-development-browser", "frontend-typecheck", "frontend-build",
		"frontend-verify-generated", "frontend-verify-supply-chain", "frontend-audit",
	}
	actualLeaves := make([]string, 0, len(dag.Leaves))
	for id := range dag.Leaves {
		actualLeaves = append(actualLeaves, id)
	}
	assert.ElementsMatch(t, expectedLeaves, actualLeaves)
	assert.Equal(t, map[string][]string{
		"test":                      {"test-unit", "test-integration", "test-harness", "test-material", "test-serve-temporary"},
		"test-browser":              {"test-browser-workflows", "test-browser-privacy", "test-browser-visual", "test-browser-accessibility", "test-browser-cross"},
		"test-frontend-development": {"test-frontend-development-node", "test-frontend-development-browser"},
	}, dag.Aggregates)

	knownBehaviors := make(map[string]struct{})
	for _, row := range contract.ProductBehaviorManifest() {
		knownBehaviors[row.ID] = struct{}{}
	}
	for _, row := range contract.SecurityBehaviorManifest() {
		knownBehaviors[row.ID] = struct{}{}
	}
	for _, row := range contract.PredecessorBehaviorManifest() {
		knownBehaviors[row.ID] = struct{}{}
	}
	for _, row := range contract.EvidenceTierManifest() {
		knownBehaviors[row.ID] = struct{}{}
	}

	root := repositoryRoot(t)
	for id, leaf := range dag.Leaves {
		assert.Equal(t, id, leaf.ID)
		assert.NotEmpty(t, leaf.BehaviorIDs, id)
		assert.Positive(t, leaf.Timeout, id)
		assert.GreaterOrEqual(t, leaf.Budget, leaf.Timeout, id)
		assert.Positive(t, leaf.Repeats, id)
		assert.NotEmpty(t, leaf.DefinitionFiles, id)
		assert.NotEmpty(t, leaf.Artifacts, id)
		assert.NotEmpty(t, leaf.Cleanup, id)
		assert.NotEmpty(t, leaf.Command, id)
		rootCommand := dag.Commands[leaf.Command]
		assert.Equal(t, []string{"make", "-C", "mcp-gateway", id}, rootCommand.Argv, id)
		assert.NotEmpty(t, rootCommand.Dependencies, id)
		for _, behaviorID := range leaf.BehaviorIDs {
			_, ok := knownBehaviors[behaviorID]
			assert.True(t, ok, "%s has unknown behavior %s", id, behaviorID)
		}
		for _, path := range leaf.DefinitionFiles {
			_, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
			assert.NoError(t, err, "%s definition %s", id, path)
		}
	}
	assert.Equal(t, 5*time.Minute, dag.Leaves["test-unit"].Timeout)
	assert.Equal(t, 6*time.Minute, dag.Leaves["test-unit"].Budget)
	assert.Equal(t, 66, dag.Leaves["test-e2e"].GatewayStarts)
	assert.Equal(t, 33, dag.Leaves["test-browser-workflows"].GatewayStarts)
	assert.Equal(t, 33, dag.Leaves["test-browser-workflows"].BrowserStarts)
	assert.Equal(t, 2, dag.Leaves["test-frontend-development-browser"].GatewayStarts)
	assert.Equal(t, 2, dag.Leaves["test-frontend-development-browser"].BrowserStarts)
	assert.Equal(t, 1, dag.Leaves["test-browser-cross"].GatewayStarts)
	assert.Equal(t, 2, dag.Leaves["test-browser-cross"].BrowserStarts)

	for id, command := range dag.Commands {
		assert.Equal(t, id, command.ID)
		assert.NotEmpty(t, command.Argv, id)
		assert.NotEmpty(t, command.DefinitionFiles, id)
		for _, path := range command.DefinitionFiles {
			_, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
			assert.NoError(t, err, "%s definition %s", id, path)
		}
	}
}

func TestPurposeEvidenceDAGExpansionAndMultiplicity(t *testing.T) {
	dag := purposeEvidenceDAG()
	finalLeaves, err := expandPurposeLeaves(dag, dag.FinalLeaves)
	require.NoError(t, err)
	commands, err := expandPurposeCommands(dag, finalLeaves)
	require.NoError(t, err)

	finalIDs := make([]string, 0, len(finalLeaves))
	for _, leaf := range finalLeaves {
		finalIDs = append(finalIDs, leaf.ID)
	}
	for _, id := range dag.FinalLeaves {
		_, directLeaf := dag.Leaves[id]
		assert.True(t, directLeaf, id)
	}
	for aggregate := range dag.Aggregates {
		assert.NotContains(t, dag.FinalLeaves, aggregate)
		assert.NotContains(t, finalIDs, aggregate)
	}
	assert.NotContains(t, finalIDs, "frontend-build")
	assert.NotContains(t, finalIDs, "frontend-verify-generated")
	assert.Contains(t, finalIDs, "frontend-verify-supply-chain")
	assert.Equal(t, 1, strings.Count(strings.Join(finalIDs, "\n"), "test-e2e"))

	e2e := dag.Leaves["test-e2e"]
	assert.Equal(t, 1, e2e.Repeats)
	assert.Contains(t, strings.Join(dag.Commands[e2e.Command].Dependencies, " "), "go.e2e.complete")
	assert.Equal(t, suiteCommandArgv("test-e2e"), dag.Commands["go.e2e.complete"].Argv)

	stress := dag.Leaves["test-stress"]
	assert.Equal(t, 20, stress.Repeats)
	moduleRoot := purposeTargetModuleRoot(t)
	inventory, err := DiscoverSuiteInventory(moduleRoot, "linux", "arm64")
	require.NoError(t, err)
	stressCommands, err := PlanSuite(moduleRoot, "test-stress", inventory, stress.Repeats)
	require.NoError(t, err)
	var selectors []string
	for _, command := range stressCommands {
		assert.Contains(t, command.Argv, "-tags=stress")
		assert.Contains(t, command.Argv, "-count=20")
		assert.Contains(t, command.Argv, "-race")
		for _, test := range command.Tests {
			selectors = append(selectors, test.Name)
		}
	}
	sort.Strings(selectors)
	assert.Equal(t, purposeStressTestIDs, selectors)
	for id, leaf := range dag.Leaves {
		if id != "test-stress" {
			assert.Equal(t, 1, leaf.Repeats, id)
		}
	}

	assert.Equal(t, 1, countPurposeCommandsContaining(commands, "run-suite test-browser-privacy"))
	assert.Equal(t, 1, countPurposeCommandsContaining(commands, "mcp-gateway/web/scripts/verify-generated.mjs"))
	assert.Equal(t, 1, countPurposeCommandsContaining(commands, "run-suite test-e2e"))
}

func TestPurposeEvidenceDAGMatchesMakeAndNPMDefinitions(t *testing.T) {
	dag := purposeEvidenceDAG()
	moduleRoot := purposeTargetModuleRoot(t)
	for id, leaf := range dag.Leaves {
		lines := purposeTargetDryRunLines(t, moduleRoot, id)
		dependencies := dag.Commands[leaf.Command].Dependencies
		require.Len(t, lines, len(dependencies), id)
		for index, dependency := range dependencies {
			expected := strings.Join(dag.Commands[dependency].Argv, " ")
			expected = strings.ReplaceAll(expected, "$(STRESS_COUNT)", strconv.Itoa(leaf.Repeats))
			actual := strings.ReplaceAll(lines[index], "'", "")
			assert.Equal(t, expected, actual, id)
		}
	}

	packageBytes, err := os.ReadFile(filepath.Join(repositoryRoot(t), "package.json"))
	require.NoError(t, err)
	var packageManifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	require.NoError(t, json.Unmarshal(packageBytes, &packageManifest))
	owners := map[string]string{
		"ui:test-dev":            "npm.ui.test-dev",
		"ui:typecheck":           "npm.ui.typecheck",
		"ui:build":               "npm.ui.build",
		"ui:verify-generated":    "npm.ui.verify-generated",
		"ui:verify-supply-chain": "npm.ui.verify-supply-chain",
		"ui:audit":               "npm.ui.audit",
	}
	for script, owner := range owners {
		command := dag.Commands[owner]
		require.Len(t, command.Dependencies, 1, owner)
		assert.Equal(t, packageManifest.Scripts[script], strings.Join(dag.Commands[command.Dependencies[0]].Argv, " "), script)
	}
	assert.Equal(t, []string{"node.verify-generated"}, dag.Commands["node.verify-supply-chain"].Dependencies)
}

func TestPurposeEvidenceDAGRecordsTransitiveMakeAndNPMEdges(t *testing.T) {
	dag := purposeEvidenceDAG()
	leaves, err := expandPurposeLeaves(dag, []string{"frontend-verify-supply-chain"})
	require.NoError(t, err)
	commands, err := expandPurposeCommands(dag, leaves)
	require.NoError(t, err)
	ids := make([]string, 0, len(commands))
	for _, command := range commands {
		ids = append(ids, command.ID)
	}
	assert.ElementsMatch(t, []string{
		"make.frontend-verify-supply-chain", "npm.ui.verify-supply-chain", "node.verify-supply-chain",
		"node.verify-generated", "go.frontend-static-supply-chain",
	}, ids)

	browserLeaves, err := expandPurposeLeaves(dag, []string{"test-browser"})
	require.NoError(t, err)
	assert.Len(t, browserLeaves, 5)
	_, err = expandPurposeLeaves(dag, []string{"test-browser", "test-browser-privacy"})
	assert.ErrorContains(t, err, "duplicate leaf test-browser-privacy")
}

func countPurposeCommandsContaining(commands []purposeCommandNode, fragment string) int {
	count := 0
	for _, command := range commands {
		if strings.Contains(strings.Join(command.Argv, " "), fragment) {
			count++
		}
	}
	return count
}
