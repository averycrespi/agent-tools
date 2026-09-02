package catalog

import (
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalSyntheticTargetsPinEvidenceAndValidators(t *testing.T) {
	for _, tool := range contract.SyntheticSelfServiceTools() {
		target, found := ResolveSyntheticCall(tool.ExternalName)
		require.True(t, found)
		assert.Equal(t, tool.ID, target.Descriptor.ID)
		assert.Equal(t, tool.ServerID, target.Descriptor.ServerID)
		assert.Equal(t, tool.UpstreamName, target.Descriptor.UpstreamName)
		assert.Equal(t, tool.ExternalName, target.Descriptor.ExternalName)
		assert.Equal(t, tool.CatalogRevision, target.Descriptor.CatalogRevision)
		assert.Equal(t, tool.Fingerprint, target.Descriptor.Fingerprint)
		require.NotNil(t, target.Validator)
	}
	_, found := ResolveSyntheticCall("mcp_gateway.not_a_tool")
	assert.False(t, found)
}

func TestSyntheticSnapshotIsExactClonedAndOutsideActiveOccupancy(t *testing.T) {
	snapshot, err := SyntheticSnapshot()
	require.NoError(t, err)
	contractTools := contract.SyntheticSelfServiceTools()
	require.Len(t, snapshot, 6)
	require.Len(t, contractTools, len(snapshot))
	for index := range snapshot {
		assert.Equal(t, contractTools[index].ID, snapshot[index].ID)
		assert.Equal(t, contractTools[index].ServerID, snapshot[index].ServerID)
		assert.Equal(t, contractTools[index].UpstreamName, snapshot[index].UpstreamName)
		assert.Equal(t, contractTools[index].ExternalName, snapshot[index].ExternalName)
		assert.Equal(t, contractTools[index].CatalogRevision, snapshot[index].CatalogRevision)
		assert.Equal(t, contractTools[index].Fingerprint, snapshot[index].Fingerprint)
		assert.Equal(t, contractTools[index].Descriptor, snapshot[index].Descriptor)
	}

	snapshot[0].UpstreamName = "mutated"
	snapshot[0].Descriptor.InputSchema[0] = '['
	snapshot[0].Descriptor.Annotations.Title = nil
	again, err := SyntheticSnapshot()
	require.NoError(t, err)
	assert.Equal(t, contractTools[0].UpstreamName, again[0].UpstreamName)
	assert.Equal(t, byte('{'), again[0].Descriptor.InputSchema[0])
	require.NotNil(t, again[0].Descriptor.Annotations.Title)

	repository, _, clock, _ := newCatalogRepository(t)
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	beforeSummary := registry.Summary()
	beforeSnapshot := registry.CurrentSnapshot()
	beforePage, err := registry.List(nil, 100)
	require.NoError(t, err)
	_, err = SyntheticSnapshot()
	require.NoError(t, err)
	assert.Equal(t, beforeSummary, registry.Summary())
	assert.Equal(t, beforeSnapshot, registry.CurrentSnapshot())
	afterPage, err := registry.List(nil, 100)
	require.NoError(t, err)
	assert.Equal(t, beforePage, afterPage)
}
