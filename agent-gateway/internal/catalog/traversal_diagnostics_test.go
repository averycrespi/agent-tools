package catalog

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/stretchr/testify/require"
)

func TestTraversalPredicatesReachDefaultPollSink(t *testing.T) {
	repository, servers, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, servers, "inventory")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[]}`)}
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { adapter.Finish(nil) })
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }, Diagnostics: adapter, DiagnosticReference: func(string) uint64 { return 1 }})
	require.NoError(t, err)
	t.Cleanup(coordinator.Shutdown)
	candidate := coordinatorCandidate(t, servers, server)
	coordinator.Activate(t.Context(), candidate)
	for _, body := range []string{`{"tools":null}`, `{"tools":null}`, `{"tools":[],"nextCursor":123}`, `{"tools":[],"private-member-canary":"secret-body-canary","private-member-canary":null}`} {
		client.result = json.RawMessage(body)
		coordinator.run(t.Context(), candidate, runtimes.CatalogTraversalPoll)
	}
	coordinator.Shutdown()
	require.True(t, adapter.Finish(nil))
	require.Equal(t, 5, client.callCount())
	require.Equal(t, 3, bytes.Count(output.Bytes(), []byte(`"event":"upstream_unhealthy"`)), "distinct predicates warn; identical repeats suppress")
	for _, fact := range []string{"field=tools rule=present_nonnull_array", "field=nextCursor rule=string_or_null", "rule=unique_members", "page=1 completed_pages=0", "response=true dispatch=unknown"} {
		require.Contains(t, output.String(), fact)
	}
	require.NotContains(t, output.String(), "private-member-canary")
	require.NotContains(t, output.String(), "secret-body-canary")
}
