package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/stretchr/testify/require"
)

func TestPollPostCommitFailureDoesNotRecoverPriorCurrent(t *testing.T) {
	repository, servers, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, servers, "inventory")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`)}
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { adapter.Finish(nil) })
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }, Diagnostics: adapter, DiagnosticReference: func(string) uint64 { return 1 }})
	require.NoError(t, err)
	t.Cleanup(coordinator.Shutdown)
	candidate := coordinatorCandidate(t, servers, server)
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(t.Context(), candidate).State)
	registry.beforeDescriptorRead = func() error { return errors.New("descriptor read: disk I/O error") }
	for range 2 {
		outcome := coordinator.run(t.Context(), candidate, runtimes.CatalogTraversalPoll)
		require.Equal(t, contract.ActiveCatalogCurrent, outcome.State, "old usable routes remain current")
		require.Equal(t, runtimes.CatalogPublicationDurableOnly, outcome.Phase)
	}
	require.Equal(t, "1", *registry.Status(server.ID).Revision)
	require.EqualValues(t, 1, registry.Status(server.ID).ToolCount)
	durable, err := repository.Status(t.Context(), server.ID)
	require.NoError(t, err)
	require.Equal(t, "3", *durable.Revision)
	require.Equal(t, 3, client.callCount())
	coordinator.Shutdown()
	require.True(t, adapter.Finish(nil))
	require.NotContains(t, output.String(), `"event":"upstream_recovered"`)
	require.Equal(t, 2, bytes.Count(output.Bytes(), []byte(`"event":"upstream_unhealthy"`)), "each newly committed revision is distinct evidence")
	require.Contains(t, output.String(), "disk I/O error")
	require.Contains(t, output.String(), "durable_revision=2")
	require.Contains(t, output.String(), "active_revision=1 active_routes=1")
	require.Contains(t, output.String(), "candidate_activation=not_ack")
}
