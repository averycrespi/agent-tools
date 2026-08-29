package contract

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLegacyControlPlaneManifest(t *testing.T) {
	assert.Equal(t, 1, S6ManifestVersion)
	criteria := S6AcceptanceEvidenceManifest()
	require.Len(t, criteria, 11)
	for index, criterion := range criteria {
		assert.Equal(t, fmt.Sprintf("AC-%d", index+1), criterion.Criterion)
		assert.NotEmpty(t, criterion.Evidence)
	}
	criteria[0].Evidence[0] = "changed"
	assert.NotEqual(t, "changed", S6AcceptanceEvidenceManifest()[0].Evidence[0])

	clauses := S6ClauseEvidenceManifest()
	require.Len(t, clauses, 90)
	seenTasks := make(map[string]bool)
	for _, clause := range clauses {
		assert.NotEmpty(t, clause.Tasks, clause.Clause)
		assert.NotEmpty(t, clause.Evidence, clause.Clause)
		for _, task := range clause.Tasks {
			seenTasks[task] = true
		}
	}
	assert.Equal(t, "RB-1.1", clauses[0].Clause)
	assert.Equal(t, "RB-12.8", clauses[len(clauses)-1].Clause)
	for task := 3; task <= 56; task++ {
		if task == 37 || task == 53 || task == 54 || task == 55 || task == 56 {
			continue
		}
		assert.True(t, seenTasks[fmt.Sprintf("T%d", task)], "task T%d has no behavioral clause", task)
	}

	capabilities := S6CapabilityManifest()
	require.Len(t, capabilities, 31)
	for _, row := range capabilities {
		assert.NotEmpty(t, row.WebScenario, row.ID)
		assert.NotEmpty(t, row.CLIScenario, row.ID)
		assert.NotEmpty(t, row.Implementation, row.ID)
	}
	lifecycle := S6LifecycleCapabilityManifest()
	assert.Len(t, lifecycle, 8)
	foundCLIBearer := false
	for _, row := range lifecycle {
		if row.ID == "cli-bearer" {
			foundCLIBearer = true
			assert.Equal(t, "owner-only explicit file/exclusive stdin/resolved default file", row.Mechanics)
			assert.NotContains(t, row.Mechanics, "prompt")
		}
	}
	assert.True(t, foundCLIBearer)
	assert.Len(t, S6DocumentationManifest(), 40)
}
