package catalog

import (
	"context"
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/stretchr/testify/require"
)

func TestGrantRecognitionUsesExactDescriptorIdentity(t *testing.T) {
	repository, serverRepository, _, _ := newCatalogRepository(t)
	first := createCatalogServer(t, serverRepository, "first")
	second := createCatalogServer(t, serverRepository, "second")
	ctx := context.Background()
	for _, server := range []struct{ id, namespace string }{{first.ID, "first"}, {second.ID, "second"}} {
		_, err := repository.Commit(ctx, catalogFence(server.id, "0"), candidateFor(t, server.id, server.namespace, "tool"))
		require.NoError(t, err)
	}
	one, err := descriptorByName(ctx, repository, first.ID, "tool")
	require.NoError(t, err)
	two, err := descriptorByName(ctx, repository, second.ID, "tool")
	require.NoError(t, err)
	// A retired descriptor remains a real readable identity, not a callable claim.
	_, err = repository.Commit(ctx, catalogFence(first.ID, "1"), candidateFor(t, first.ID, "first"))
	require.NoError(t, err)
	require.NoError(t, repository.store.View(ctx, func(tx *sql.Tx) error {
		ids, err := repository.GrantToolIDsTx(ctx, tx)
		require.NoError(t, err)
		require.Equal(t, map[[2]string]string{{first.ID, "tool"}: one.ID, {second.ID, "tool"}: two.ID}, ids)
		require.Empty(t, ids[[2]string{first.ID, "Tool"}])
		require.Empty(t, ids[[2]string{first.ID, "first.tool"}])
		return nil
	}))
	_, err = repository.GrantToolIDsTx(ctx, nil)
	require.ErrorIs(t, err, servers.ErrStorageUnavailable)
}
