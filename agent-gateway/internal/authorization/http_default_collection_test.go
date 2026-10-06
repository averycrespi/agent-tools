package authorization

import (
	"context"
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestPrincipalCollectionHTTPDefaultSelectsAcrossPages(t *testing.T) {
	repository, store := newRepository(t, nil)
	for i := 1; i <= 128; i++ {
		seedPrincipal(t, store, principalRow{id: id(i), displayName: "Duplicate"})
	}
	ctx := context.Background()
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE http_defaults SET policy='allow' WHERE principal_id=?`, id(128))
		return err
	}))
	service, err := NewCollectionService(repository, collectionTargets{}, collectionTools{})
	require.NoError(t, err)
	all, err := service.QueryPrincipals(ctx, CollectionQuery{Sort: "name"}, nil, 50)
	require.NoError(t, err)
	require.Equal(t, 128, all.TotalCount)
	require.NotNil(t, all.Next)
	allowed, err := service.QueryPrincipals(ctx, CollectionQuery{HTTPDefault: "allow", Sort: "name"}, nil, 50)
	require.NoError(t, err)
	require.Equal(t, 1, allowed.TotalCount)
	require.Equal(t, 0, allowed.Offset)
	require.Len(t, allowed.Items, 1)
	require.Equal(t, id(128), allowed.Items[0].ID)
	require.Equal(t, contract.HTTPDefaultAllow, allowed.Items[0].HTTPDefault)
	query := CollectionQuery{HTTPDefault: "block", Sort: "name"}
	first, err := service.QueryPrincipals(ctx, query, nil, 50)
	require.NoError(t, err)
	require.Equal(t, 127, first.TotalCount)
	second, err := service.QueryPrincipals(ctx, query, first.Next, 50)
	require.NoError(t, err)
	require.Equal(t, 50, second.Offset)
	third, err := service.QueryPrincipals(ctx, query, second.Next, 50)
	require.NoError(t, err)
	require.Len(t, third.Items, 27)
	require.Equal(t, 100, third.Offset)
	require.Nil(t, third.Next)
	_, err = service.QueryPrincipals(ctx, CollectionQuery{HTTPDefault: "allow", Sort: "name"}, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE http_defaults SET policy='allow' WHERE principal_id=?`, id(127))
		return err
	}))
	_, err = service.QueryPrincipals(ctx, query, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	_, err = service.QueryPrincipals(ctx, CollectionQuery{HTTPDefault: "other"}, nil, 50)
	require.ErrorIs(t, err, ErrInvalidInput)
	require.False(t, (CollectionQuery{HTTPDefault: "allow"}).Validate("grants"))
}
