package servers

import (
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestOperationQueryActiveHistoryAndMutableSnapshot(t *testing.T) {
	clock := &mutableClock{now: testTime}
	repository, _, _ := newRepositoryWithClock(t, clock, new(sequenceReader))
	server := mustCreateServer(t, repository, "operation-query", false)
	ctx := t.Context()
	create := func(kind contract.ServerOperationKind, terminal bool) Operation {
		result, err := repository.CreateOperation(ctx, OperationRequest{ServerID: server.ID, Kind: kind})
		require.NoError(t, err)
		if terminal {
			_, err = repository.TransitionOperation(ctx, result.Operation.ID, contract.OperationInterrupted, nil)
			require.NoError(t, err)
		}
		return result.Operation
	}
	old := create(contract.OperationRetry, false)
	clock.now = clock.now.Add(time.Hour)
	for range 60 {
		create(contract.OperationReload, true)
	}
	clock.now = clock.now.Add(time.Hour)
	newer := create(contract.OperationRefreshCatalog, false)
	active, more, err := repository.ActiveOperations(ctx, server.ID)
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, active, 2)
	require.Equal(t, old.ID, active[0].ID)
	require.Equal(t, newer.ID, active[1].ID)
	query := OperationQuery{Sort: "started", Direction: "descending"}
	first, err := repository.QueryOperations(ctx, server.ID, query, nil, 50)
	require.NoError(t, err)
	require.Len(t, first.Items, 50)
	require.Equal(t, 62, first.TotalCount)
	require.NotNil(t, first.Next)
	require.Equal(t, newer.ID, first.Items[0].ID)
	seen := map[string]bool{}
	for _, item := range first.Items {
		require.NotEqual(t, old.ID, item.ID)
		seen[item.ID] = true
	}
	for _, sortKey := range []string{"action", "status", "started", "outcome"} {
		for _, direction := range []string{"ascending", "descending"} {
			q := OperationQuery{Sort: sortKey, Direction: direction}
			var cursor *OperationQueryCursor
			ids := map[string]bool{}
			for range 4 {
				page, err := repository.QueryOperations(ctx, server.ID, q, cursor, 17)
				require.NoError(t, err)
				for _, item := range page.Items {
					require.False(t, ids[item.ID])
					ids[item.ID] = true
				}
				cursor = page.Next
			}
			require.Nil(t, cursor)
			require.Len(t, ids, 62)
		}
	}
	wrong := *first.Next
	wrong.Position = first.TotalCount
	_, err = repository.QueryOperations(ctx, server.ID, query, &wrong, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	_, err = repository.QueryOperations(ctx, server.ID, OperationQuery{Sort: "status"}, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	// An append is excluded even if its mutable state subsequently changes.
	appended := create(contract.OperationDelete, false)
	_, err = repository.TransitionOperation(ctx, appended.ID, contract.OperationRunning, nil)
	require.NoError(t, err)
	second, err := repository.QueryOperations(ctx, server.ID, query, first.Next, 50)
	require.NoError(t, err)
	require.Equal(t, 50, second.Offset)
	require.Len(t, second.Items, 12)
	require.Nil(t, second.Next)
	for _, item := range second.Items {
		require.False(t, seen[item.ID])
		seen[item.ID] = true
	}
	require.Len(t, seen, 62)
	require.True(t, seen[old.ID])
	require.False(t, seen[appended.ID])
	filtered, err := repository.QueryOperations(ctx, server.ID, OperationQuery{Action: "retry", Status: "scheduled"}, nil, 50)
	require.NoError(t, err)
	require.Len(t, filtered.Items, 1)
	require.Equal(t, old.ID, filtered.Items[0].ID)
	require.Equal(t, 1, filtered.TotalCount)
	active, more, err = repository.ActiveOperations(ctx, server.ID)
	require.NoError(t, err)
	require.True(t, more)
	require.Len(t, active, 2)
	_, err = repository.TransitionOperation(ctx, old.ID, contract.OperationRunning, nil)
	require.NoError(t, err)
	_, err = repository.QueryOperations(ctx, server.ID, query, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	changed, err := repository.QueryOperations(ctx, server.ID, query, nil, 1)
	require.NoError(t, err)
	_, err = repository.TransitionOperation(ctx, old.ID, contract.OperationSucceeded, nil)
	require.NoError(t, err)
	_, err = repository.QueryOperations(ctx, server.ID, query, changed.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	beforePrune, err := repository.QueryOperations(ctx, server.ID, query, nil, 1)
	require.NoError(t, err)
	for range 5 {
		create(contract.OperationReload, true)
	}
	_, err = repository.QueryOperations(ctx, server.ID, query, beforePrune.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
}
