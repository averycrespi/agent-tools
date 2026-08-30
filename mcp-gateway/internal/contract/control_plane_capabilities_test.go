package contract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlPlaneCapabilityManifestIsClosedAndCopySafe(t *testing.T) {
	capabilities := ControlPlaneCapabilityManifest()
	require.Len(t, capabilities, 31)
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
	require.Len(t, lifecycle, 8)
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
