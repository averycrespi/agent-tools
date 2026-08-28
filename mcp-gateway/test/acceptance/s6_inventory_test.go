package acceptance

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6Inventory(t *testing.T) {
	owners := S6PlannedInventory()
	require.Len(t, owners, 69)
	assert.Len(t, S6FinalInventory(), 16)
	assert.Len(t, S6Baselines, 11)
	assert.Equal(t, 211469*time.Millisecond, S6Baselines[len(S6Baselines)-1].Duration)

	categories := make(map[S6LeafCategory]int)
	gatewayStarts := 0
	chromiumStarts := 0
	leaves := 0
	seenLeaves := make(map[string]bool)
	for _, owner := range owners {
		assert.NotEmpty(t, owner.Intent, owner.ID)
		assert.NotEmpty(t, owner.DefinitionHash, owner.ID)
		assert.True(t, owner.Blocking, owner.ID)
		if owner.Layer == "task" {
			assert.LessOrEqual(t, owner.Budget, 2*time.Minute, owner.ID)
		} else {
			assert.LessOrEqual(t, owner.Budget, 5*time.Minute, owner.ID)
		}
		for _, leaf := range owner.Leaves {
			assert.False(t, seenLeaves[leaf.ID], leaf.ID)
			seenLeaves[leaf.ID] = true
			categories[leaf.Category]++
			gatewayStarts += leaf.GatewayStarts
			chromiumStarts += leaf.ChromiumStarts
			leaves++
		}
	}
	assert.Equal(t, 79, leaves)
	assert.Equal(t, map[S6LeafCategory]int{
		S6LeafNPM: 4, S6LeafGo: 17, S6LeafIntegration: 2, S6LeafSecurity: 1,
		S6LeafBrowser: 36, S6LeafE2E: 18, S6LeafExternal: 1,
	}, categories)
	assert.Equal(t, 55, gatewayStarts)
	assert.Equal(t, 36, chromiumStarts)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, S6InventoryDefinitionHash())
}

func TestS6PlannedCommand(t *testing.T) {
	require.NoError(t, ValidateS6OwnerForExecution("T1"))
	require.NoError(t, ValidateS6OwnerForExecution("T2"))
	require.NoError(t, ValidateS6OwnerForExecution("T3"))
	require.NoError(t, ValidateS6OwnerForExecution("T4"))
	require.NoError(t, ValidateS6OwnerForExecution("T5"))
	require.NoError(t, ValidateS6OwnerForExecution("T6"))
	require.NoError(t, ValidateS6OwnerForExecution("T7"))
	require.NoError(t, ValidateS6OwnerForExecution("T8"))
	require.NoError(t, ValidateS6OwnerForExecution("T9"))
	require.NoError(t, ValidateS6OwnerForExecution("T10"))
	require.NoError(t, ValidateS6OwnerForExecution("T11"))
	require.NoError(t, ValidateS6OwnerForExecution("T12"))
	require.NoError(t, ValidateS6OwnerForExecution("T13"))
	require.NoError(t, ValidateS6OwnerForExecution("T14"))
	require.NoError(t, ValidateS6OwnerForExecution("T15"))
	require.NoError(t, ValidateS6OwnerForExecution("T16"))
	require.NoError(t, ValidateS6OwnerForExecution("T17"))
	require.NoError(t, ValidateS6OwnerForExecution("M1"))
	require.NoError(t, ValidateS6OwnerForExecution("M2"))
	require.NoError(t, ValidateS6OwnerForExecution("M3"))
	assert.EqualError(t, ValidateS6OwnerForExecution("T18"), "S6 owner T18 is planned, not executable")
	assert.EqualError(t, ValidateS6OwnerForExecution("M4"), "S6 owner M4 is planned, not executable")
	assert.EqualError(t, ValidateS6OwnerForExecution("unknown"), `unknown S6 owner "unknown"`)

	owner, ok := S6Owner("T1")
	require.True(t, ok)
	require.Len(t, owner.Leaves, 1)
	leaf := owner.Leaves[0]
	assert.Equal(t, 5, leaf.ExpectedMatches)
	assert.Contains(t, leaf.Argv, "^TestS6(Manifest|Inventory|PlannedCommand|Capability|Documentation)$")

	anchored := regexp.MustCompile(`^\^.*\$$`)
	for _, planned := range S6PlannedInventory() {
		for _, candidate := range planned.Leaves {
			joined := strings.Join(candidate.Argv, " ")
			assert.NotContains(t, joined, "accept-s2-1")
			assert.NotContains(t, joined, "accept-s3")
			assert.NotContains(t, joined, "accept-s4")
			assert.NotContains(t, joined, "accept-s5")
			if candidate.Category != S6LeafNPM && candidate.Category != S6LeafExternal {
				selectorIndex := -1
				for index, argument := range candidate.Argv {
					if argument == "-run" {
						selectorIndex = index
						break
					}
				}
				require.GreaterOrEqual(t, selectorIndex, 0, candidate.ID)
				assert.True(t, anchored.MatchString(candidate.Argv[selectorIndex+1]), candidate.ID)
				assert.Contains(t, candidate.Argv, "-count=1")
			}
		}
	}
}

func TestS6Capability(t *testing.T) {
	rows := contract.S6CapabilityManifest()
	require.Len(t, rows, 31)
	ids := make(map[string]bool)
	cliLeaves := 0
	for _, row := range rows {
		assert.False(t, ids[row.ID], row.ID)
		ids[row.ID] = true
		assert.NotEmpty(t, row.Operation, row.ID)
		assert.NotEmpty(t, row.WebControl, row.ID)
		assert.NotEmpty(t, row.Mechanics, row.ID)
		assert.NotEmpty(t, row.WebScenario, row.ID)
		assert.NotEmpty(t, row.CLIScenario, row.ID)
		cliLeaves += len(row.CLIUses)
	}
	assert.Equal(t, 41, cliLeaves)
	lifecycle := contract.S6LifecycleCapabilityManifest()
	require.Len(t, lifecycle, 8)
	assert.Equal(t, "web-exchange", lifecycle[0].ID)
	assert.Equal(t, "cli-restore", lifecycle[len(lifecycle)-1].ID)
}

func TestS6Documentation(t *testing.T) {
	rows := contract.S6DocumentationManifest()
	require.Len(t, rows, 40)
	ids := make(map[string]bool)
	approved := map[string]bool{
		"mcp-gateway/README.md": true, "mcp-gateway/DESIGN.md": true,
		"mcp-gateway/CLAUDE.md": true, "command-help": true,
	}
	for _, row := range rows {
		assert.False(t, ids[row.ID], row.ID)
		ids[row.ID] = true
		assert.NotEmpty(t, row.Subject, row.ID)
		for _, target := range row.Targets {
			assert.True(t, approved[target], fmt.Sprintf("%s: %s", row.ID, target))
		}
	}
}
