package catalog

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCurrentSnapshotIsDescriptorOnlyCurrentAndCloneIsolated(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	_, err = registry.Publish(context.Background(), Publication{
		Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1,
		Candidate: candidateFor(t, server.ID, "sample", "one", "two"), Current: func() bool { return true },
	})
	require.NoError(t, err)

	snapshot := registry.CurrentSnapshot()
	assert.Equal(t, CurrentGeneration{ProcessGeneration: activeProcessID, ActiveGeneration: 1}, snapshot.Generation)
	require.Len(t, snapshot.Descriptors, 2)
	assert.Equal(t, []string{"one", "two"}, toolDescriptorNames(snapshot.Descriptors))
	assert.True(t, registry.IsCurrentGeneration(snapshot.Generation))
	assert.Equal(t, []string{"Generation", "Descriptors"}, exportedFieldNames(reflect.TypeOf(CurrentSnapshot{})))
	assert.Equal(t, []string{"ProcessGeneration", "ActiveGeneration"}, exportedFieldNames(reflect.TypeOf(CurrentGeneration{})))

	snapshot.Descriptors[0].UpstreamName = "mutated"
	snapshot.Descriptors[0].Descriptor.InputSchema[0] = 'x'
	again := registry.CurrentSnapshot()
	assert.Equal(t, "one", again.Descriptors[0].UpstreamName)
	assert.NotEqual(t, byte('x'), again.Descriptors[0].Descriptor.InputSchema[0])

	admin, err := registry.List(nil, 100)
	require.NoError(t, err)
	require.Len(t, admin.Items, 2)
	assert.True(t, registry.MarkStaleExact(server.ID, "runtime-1", 1, 3))
	assert.Empty(t, registry.CurrentSnapshot().Descriptors)
	assert.False(t, registry.IsCurrentGeneration(snapshot.Generation))
	admin, err = registry.List(nil, 100)
	require.NoError(t, err)
	assert.Len(t, admin.Items, 2, "administrative listing must retain stale descriptors")
}

func TestCurrentSnapshotGenerationFencesReplacementWithdrawalAndDrain(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	_, err = registry.Publish(context.Background(), Publication{
		Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1,
		Candidate: candidateFor(t, server.ID, "sample", "one"), Current: func() bool { return true },
	})
	require.NoError(t, err)
	first := registry.CurrentSnapshot()

	_, err = registry.Publish(context.Background(), Publication{
		Fence: catalogFence(server.ID, "1"), RuntimeID: "runtime-2", RuntimeGeneration: 2,
		Candidate: candidateFor(t, server.ID, "sample", "two"), Current: func() bool { return true },
	})
	require.NoError(t, err)
	second := registry.CurrentSnapshot()
	assert.Equal(t, first.Generation.ActiveGeneration+1, second.Generation.ActiveGeneration)
	assert.False(t, registry.IsCurrentGeneration(first.Generation))
	assert.True(t, registry.IsCurrentGeneration(second.Generation))
	assert.Equal(t, []string{"two"}, toolDescriptorNames(second.Descriptors))

	assert.True(t, registry.WithdrawExact(server.ID, "runtime-2", 2, contract.ActiveCatalogUnavailable))
	assert.Empty(t, registry.CurrentSnapshot().Descriptors)
	assert.False(t, registry.IsCurrentGeneration(second.Generation))

	restarted, err := NewActiveRegistry(repository, clock, "new-process")
	require.NoError(t, err)
	assert.False(t, restarted.IsCurrentGeneration(second.Generation))
	assert.True(t, restarted.IsCurrentGeneration(restarted.CurrentSnapshot().Generation))

	registry.Drain()
	assert.Empty(t, registry.CurrentSnapshot().Descriptors)
	assert.False(t, registry.IsCurrentGeneration(registry.CurrentSnapshot().Generation))
}

func TestCurrentSnapshotConcurrentReadsNeverExposeStaleOrMutableState(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	_, err = registry.Publish(context.Background(), Publication{
		Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1,
		Candidate: candidateFor(t, server.ID, "sample", "one"), Current: func() bool { return true },
	})
	require.NoError(t, err)

	start := make(chan struct{})
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for range 100 {
				snapshot := registry.CurrentSnapshot()
				if len(snapshot.Descriptors) != 0 {
					snapshot.Descriptors[0].Descriptor.InputSchema[0] = 'x'
				}
				_ = registry.IsCurrentGeneration(snapshot.Generation)
			}
		}()
	}
	close(start)
	assert.True(t, registry.MarkStaleExact(server.ID, "runtime-1", 1, 1))
	wait.Wait()
	assert.Empty(t, registry.CurrentSnapshot().Descriptors)
}

func toolDescriptorNames(descriptors []contract.ToolDescriptor) []string {
	result := make([]string, len(descriptors))
	for index, descriptor := range descriptors {
		result[index] = descriptor.UpstreamName
	}
	return result
}

func exportedFieldNames(value reflect.Type) []string {
	result := make([]string, value.NumField())
	for index := range value.NumField() {
		result[index] = value.Field(index).Name
	}
	return result
}
