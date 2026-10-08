package catalog

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/stretchr/testify/require"
)

func TestCatalogStatusFailureRetainsSQLiteCause(t *testing.T) {
	repository, servers, clock, store := newCatalogRepository(t)
	server := createCatalogServer(t, servers, "inventory")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	candidate := coordinatorCandidate(t, servers, server)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","description":"private-tool-payload-canary","inputSchema":{"type":"object"}}]}`)}
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `ALTER TABLE server_catalogs RENAME COLUMN durable_state TO unavailable_state`)
		return err
	}))
	outcome := coordinator.Activate(t.Context(), candidate)
	require.Equal(t, contract.ActiveCatalogUnavailable, outcome.State)
	require.Contains(t, outcome.DiagnosticDetail.Explanation, "no such column: durable_state")
	require.NotContains(t, outcome.DiagnosticDetail.Explanation, "private-tool-payload-canary")
	encoded, err := json.Marshal(outcome)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "no such column")
	coordinator.Abandon(candidate)
}
