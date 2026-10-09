package catalog

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/stretchr/testify/require"
)

func TestAnnotationExclusionsReachDefaultPollWarning(t *testing.T) {
	for _, test := range []struct{ name, annotations, field, kind string }{
		{"array", `["private-annotation-canary"]`, "field=annotations rule=bounded_unique_object", "array"},
		{"title", `{"title":42}`, "field=annotations.title", "number"},
		{"hint", `{"readOnlyHint":"private-annotation-canary","private-member-canary":true}`, "field=annotations.readOnlyHint rule=boolean_hint", "string"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, servers, clock, _ := newCatalogRepository(t)
			server := createCatalogServer(t, servers, "annotations")
			registry, err := NewActiveRegistry(repository, clock, activeProcessID)
			require.NoError(t, err)
			client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"kept","inputSchema":{"type":"object"}},{"name":"rejected","inputSchema":{"type":"object"},"annotations":` + test.annotations + `}]}`)}
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Warn)
			t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
			coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }, Diagnostics: adapter})
			require.NoError(t, err)
			t.Cleanup(coordinator.Shutdown)
			outcome := coordinator.run(t.Context(), coordinatorCandidate(t, servers, server), runtimes.CatalogTraversalPoll)
			require.Equal(t, runtimes.CatalogPublicationInstalled, outcome.Phase)
			require.Nil(t, outcome.Reason)
			require.EqualValues(t, 1, registry.Status(server.ID).ToolCount)
			require.Equal(t, 1, client.callCount())
			coordinator.Shutdown()
			require.True(t, adapter.Finish(nil))
			for _, fact := range []string{`"event":"upstream_unhealthy"`, "raw=2 accepted=1 rejected=1", "tool=rejected", test.field, "observed_kind=" + test.kind, "subset=available"} {
				require.Contains(t, output.String(), fact)
			}
			require.NotContains(t, output.String(), "private-annotation-canary")
			require.NotContains(t, output.String(), "private-member-canary")
		})
	}
}

func TestPartialCatalogPublicationExplainsExclusionAtDefaultSink(t *testing.T) {
	repository, servers, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, servers, "inventory")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"kept","inputSchema":{"type":"object"}},{"name":"rejected","inputSchema":{"type":"object"}},{"name":"omitted","inputSchema":{"type":"object"}}]}`)}
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { adapter.Finish(nil) })
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }, Diagnostics: adapter})
	require.NoError(t, err)
	t.Cleanup(coordinator.Shutdown)
	candidate := coordinatorCandidate(t, servers, server)
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(t.Context(), candidate).State)
	client.result = json.RawMessage(`{"tools":[{"name":"kept","inputSchema":{"type":"object"},"outputSchema":{"type":["string","null"]}},{"name":"rejected","description":"actual-secret-canary","inputSchema":{"type":"object","properties":{"private-property-canary":{"type":"string","x-mcp-header":"private-header-canary"}}}}]}`)
	outcome := coordinator.run(t.Context(), candidate, runtimes.CatalogTraversalPoll)
	require.Equal(t, contract.ActiveCatalogCurrent, outcome.State)
	require.Equal(t, runtimes.CatalogPublicationInstalled, outcome.Phase)
	require.Nil(t, outcome.Reason, "public success/availability is unchanged")
	require.EqualValues(t, 1, registry.Status(server.ID).ToolCount)
	require.Equal(t, 2, client.callCount())
	coordinator.Shutdown()
	require.True(t, adapter.Finish(nil))
	for _, fact := range []string{"raw=2 accepted=1 rejected=1 omitted_samples=0", "prior_active_retired rejected=1 omitted=1 unknown=0", "tool=rejected", "field=inputSchema", "header_binding_requires_modern_http", "subset=available", "mode=", "era=unknown"} {
		require.Contains(t, output.String(), fact)
	}
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte(`"event":"upstream_unhealthy"`)))
	for _, secret := range []string{"actual-secret-canary", "private-property-canary", "private-header-canary"} {
		require.NotContains(t, output.String(), secret)
	}
}
