package authorization

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestConfigurationRoutingRevalidatesOpaqueOwnershipUnderAuthorityGate(t *testing.T) {
	r, store := newRepository(t, nil)
	ctx := audit.WithOffline(t.Context())
	origins := []string{"https://example.com:443"}
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error { return r.CheckConfigurationRoutingTx(ctx, tx, origins) }))
	// A real current-owner registry fact appearing after preview invalidates it.
	r.authority.mu.Lock()
	r.authority.opaqueGitOrigins[origins[0]] = 1
	r.authority.mu.Unlock()
	defs := []contract.ConfigDefinition{
		{Format: contract.ConfigExportFormat, Version: 2, Kind: "agent", Key: "new", Spec: json.RawMessage(`{"display_name":"New","state":"active","visibility":"allowed-only","http_default":"block"}`)},
		{Format: contract.ConfigExportFormat, Version: 2, Kind: "git_routing", Spec: json.RawMessage(`{"origins":["https://example.com:443"]}`)},
	}
	mutate := func() error {
		return r.MutateConfiguration(ctx, func(tx *sql.Tx) error {
			return r.StageConfigurationTx(ctx, tx, defs, map[string]string{"agent/new": id(101)}, testNow, targetInspector{})
		})
	}
	require.ErrorIs(t, mutate(), ErrConflict)
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM principals)+(SELECT count(*) FROM resource_keys)`).Scan(&count); err != nil {
			return err
		}
		require.Zero(t, count)
		return nil
	}))
	p, err := r.GetGitRoutingProfile(ctx)
	require.NoError(t, err)
	require.Equal(t, "1", p.Revision)
	require.Empty(t, p.Origins)
	r.authority.mu.Lock()
	delete(r.authority.opaqueGitOrigins, origins[0])
	r.authority.mu.Unlock()
	require.NoError(t, mutate())
	p, err = r.GetGitRoutingProfile(ctx)
	require.NoError(t, err)
	require.Equal(t, "2", p.Revision)
	require.Equal(t, origins, p.Origins)
}
