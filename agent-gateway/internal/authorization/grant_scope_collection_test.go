package authorization

import (
	"context"
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestGrantCollectionServerScopeAndDescriptorBindings(t *testing.T) {
	repository, store := newRepository(t, nil)
	seedPrincipal(t, store, principalRow{id: id(1), displayName: "Agent"})
	for i := 0; i < 128; i++ {
		seedProjectedGrant(t, store, projectedGrantRow{id: id(i + 100), principalID: id(1), serverID: id(51), upstreamName: "Tool elephant", effect: contract.GrantAllow})
	}
	seedProjectedGrant(t, store, projectedGrantRow{id: id(301), principalID: id(1), serverID: id(51), effect: contract.GrantAllow})
	seedProjectedGrant(t, store, projectedGrantRow{id: id(303), principalID: id(1), serverID: contract.SyntheticServerID, upstreamName: "get_identity", effect: contract.GrantAllow})
	require.NoError(t, store.Mutate(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO grants (id, principal_id, effect, server_id, upstream_name, read_only, created_at) VALUES (?, ?, 'allow', ?, NULL, 1, ?)`, id(302), id(1), id(51), timestamp(testNow))
		return err
	}))
	names := collectionTargets{id(51): "Remote workshop", contract.SyntheticServerID: "Gateway self-service tools"}
	tools := collectionTools{{id(51), "Tool elephant"}: id(501), {contract.SyntheticServerID, "get_identity"}: id(502)}
	service, err := NewCollectionService(repository, names, tools)
	require.NoError(t, err)
	ctx := context.Background()
	for _, query := range []CollectionQuery{{Server: "workhsop"}, {Server: id(51)}, {Server: id(51)[20:]}} {
		page, err := service.QueryGrants(ctx, query, nil, 50)
		require.NoError(t, err)
		require.Equal(t, 130, page.TotalCount)
	}
	noServer, err := service.QueryGrants(ctx, CollectionQuery{Server: "elephant"}, nil, 50)
	require.NoError(t, err)
	require.Empty(t, noServer.Items)
	query := CollectionQuery{Scope: "elephnat", Sort: "scope"}
	var total int
	var cursor *SnapshotCursor
	for {
		page, err := service.QueryGrants(ctx, query, cursor, 50)
		require.NoError(t, err)
		require.Equal(t, 128, page.TotalCount)
		require.Equal(t, total, page.Offset)
		for _, row := range page.Items {
			require.Equal(t, id(501), row.ToolID)
			require.False(t, row.Grant.ReadOnly)
		}
		total += len(page.Items)
		cursor = page.Next
		if cursor == nil {
			break
		}
	}
	require.Equal(t, 128, total)
	for _, scope := range []string{id(501), id(501)[20:]} {
		page, err := service.QueryGrants(ctx, CollectionQuery{Scope: scope}, nil, 50)
		require.NoError(t, err)
		require.Equal(t, 128, page.TotalCount)
	}
	readonly, err := service.QueryGrants(ctx, CollectionQuery{Scope: "Read-only"}, nil, 50)
	require.NoError(t, err)
	require.Len(t, readonly.Items, 1)
	require.Equal(t, id(302), readonly.Items[0].Grant.ID)
	all, err := service.QueryGrants(ctx, CollectionQuery{Scope: "All tools"}, nil, 50)
	require.NoError(t, err)
	require.Len(t, all.Items, 1)
	require.Equal(t, id(301), all.Items[0].Grant.ID)
	synthetic, err := service.QueryGrants(ctx, CollectionQuery{ServerID: contract.SyntheticServerID}, nil, 50)
	require.NoError(t, err)
	require.Len(t, synthetic.Items, 1)
	require.Empty(t, synthetic.Items[0].ToolID)
	first, err := service.QueryGrants(ctx, query, nil, 50)
	require.NoError(t, err)
	require.NotNil(t, first.Next)
	delete(tools, [2]string{id(51), "Tool elephant"})
	_, err = service.QueryGrants(ctx, query, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	unresolved, err := service.QueryGrants(ctx, query, nil, 50)
	require.NoError(t, err)
	require.Empty(t, unresolved.Items[0].ToolID)
	require.Equal(t, "Tool elephant", *unresolved.Items[0].Grant.UpstreamName)
}
