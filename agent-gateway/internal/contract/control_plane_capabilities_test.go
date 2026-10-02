package contract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitControlPlaneCapabilitiesKeepDedicatedOwners(t *testing.T) {
	rows := GitControlPlaneCapabilityManifest()
	require.Len(t, rows, 5)
	uses := make(map[string]bool)
	for _, row := range rows {
		if row.ID == "git-routing-profile" {
			assert.Empty(t, row.WebControl)
			assert.Empty(t, row.WebScenario)
		} else {
			assert.NotEmpty(t, row.WebControl)
			assert.Equal(t, "browser.git", row.WebScenario)
		}
		assert.Equal(t, "cli."+row.ID, row.CLIScenario)
		assert.NotEmpty(t, row.Operation)
		assert.NotEmpty(t, row.Mechanics)
		for _, use := range row.CLIUses {
			assert.False(t, uses[use], use)
			uses[use] = true
		}
	}
	require.Len(t, uses, 20)
	rows[0].CLIUses[0] = "changed"
	assert.Equal(t, "git repository list", GitControlPlaneCapabilityManifest()[0].CLIUses[0])
}

func TestControlPlaneCapabilityManifest(t *testing.T) {
	capabilities := ControlPlaneCapabilityManifest()
	require.Len(t, capabilities, 45)
	productIDs := make([]string, 0, len(capabilities))
	for _, row := range capabilities {
		assert.NotEmpty(t, row.ID)
		assert.NotEmpty(t, row.Operation, row.ID)
		assert.NotEmpty(t, row.WebScenario, row.ID)
		assert.NotEmpty(t, row.CLIScenario, row.ID)
		assert.NotEmpty(t, row.Mechanics, row.ID)
		productIDs = append(productIDs, "product.capability."+strings.ReplaceAll(row.ID, "-", "."))
	}
	var expectedCapabilities []string
	for _, row := range ProductBehaviorManifest() {
		if row.Kind == "capability" {
			expectedCapabilities = append(expectedCapabilities, row.ID)
		}
	}
	assert.Equal(t, expectedCapabilities, productIDs)
	capabilities[0].CLIUses[0] = "changed"
	assert.NotEqual(t, "changed", ControlPlaneCapabilityManifest()[0].CLIUses[0])

	lifecycle := ControlPlaneLifecycleManifest()
	require.Len(t, lifecycle, 9)
	var lifecycleIDs []string
	for _, row := range lifecycle {
		lifecycleIDs = append(lifecycleIDs, "product.lifecycle."+strings.ReplaceAll(row.ID, "-", "."))
		if row.ID == "cli-bearer" {
			assert.Equal(t, "owner-only explicit file/exclusive stdin/resolved default file", row.Mechanics)
			assert.NotContains(t, row.Mechanics, "prompt")
		}
	}
	var expectedLifecycle []string
	for _, row := range ProductBehaviorManifest() {
		if row.Kind == "lifecycle" {
			expectedLifecycle = append(expectedLifecycle, row.ID)
		}
	}
	assert.Equal(t, expectedLifecycle, lifecycleIDs)
}
