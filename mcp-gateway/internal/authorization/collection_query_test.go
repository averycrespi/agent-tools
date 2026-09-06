package authorization

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

type collectionTargets map[string]string

func (targets collectionTargets) GrantDisplayNamesTx(_ context.Context, tx *sql.Tx) (map[string]string, error) {
	if tx == nil {
		return nil, fmt.Errorf("missing shared transaction")
	}
	return targets, nil
}

func TestCollectionQueriesPageGlobalMatchesAndStableTies(t *testing.T) {
	repository, store := newRepository(t, nil)
	for i := 1; i <= 128; i++ {
		name := "Duplicate"
		if i == 128 {
			name = "Éléphant Faraway"
		}
		seedPrincipal(t, store, principalRow{id: id(i), displayName: name})
		seedGrant(t, store, grantRow{id: id(i + 200), principalID: id(i), serverID: contract.SyntheticServerID})
	}
	service, err := NewCollectionService(repository, collectionTargets{contract.SyntheticServerID: "Gateway self-service tools"})
	require.NoError(t, err)
	ctx := context.Background()
	query := CollectionQuery{Sort: "name", Direction: "ascending"}
	first, err := service.QueryPrincipals(ctx, query, nil, 50)
	require.NoError(t, err)
	require.Len(t, first.Items, 50)
	require.NotNil(t, first.Next)
	second, err := service.QueryPrincipals(ctx, query, first.Next, 50)
	require.NoError(t, err)
	require.Len(t, second.Items, 50)
	third, err := service.QueryPrincipals(ctx, query, second.Next, 50)
	require.NoError(t, err)
	require.Len(t, third.Items, 28)
	require.Nil(t, third.Next)
	seen := map[string]bool{}
	for i, page := range []PrincipalPage{first, second, third} {
		require.Equal(t, 128, page.TotalCount)
		require.Equal(t, i*50, page.Offset)
		for _, item := range page.Items {
			require.False(t, seen[item.ID])
			seen[item.ID] = true
		}
	}
	back, err := service.QueryPrincipals(ctx, query, first.Next, 50)
	require.NoError(t, err)
	require.Equal(t, second, back)
	matched, err := service.QueryPrincipals(ctx, CollectionQuery{Name: "elephnat faraway", Sort: "name"}, nil, 50)
	require.NoError(t, err)
	require.Len(t, matched.Items, 1)
	require.Equal(t, id(128), matched.Items[0].ID)
	require.Equal(t, contract.CollectionRange{TotalCount: 1, Offset: 0}, matched.CollectionRange)
	_, err = service.QueryPrincipals(ctx, CollectionQuery{Sort: "id"}, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	_, err = repository.ListPrincipals(ctx, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)

	grants, err := service.QueryGrants(ctx, CollectionQuery{Sort: "principal", Representation: "table"}, nil, 50)
	require.NoError(t, err)
	require.Len(t, grants.Items, 50)
	require.NotNil(t, grants.Next)
	require.Equal(t, "Duplicate", grants.Items[0].PrincipalDisplayName)
	require.Equal(t, "Gateway self-service tools", grants.Items[0].ServerDisplayName)
	g2, err := service.QueryGrants(ctx, CollectionQuery{Sort: "principal", Representation: "table"}, grants.Next, 50)
	require.NoError(t, err)
	require.Len(t, g2.Items, 50)
	g3, err := service.QueryGrants(ctx, CollectionQuery{Sort: "principal", Representation: "table"}, g2.Next, 50)
	require.NoError(t, err)
	require.Len(t, g3.Items, 28)
	require.Nil(t, g3.Next)
	for i, page := range []GrantTablePage{grants, g2, g3} {
		require.Equal(t, 128, page.TotalCount)
		require.Equal(t, i*50, page.Offset)
	}
	grantBack, err := service.QueryGrants(ctx, CollectionQuery{Sort: "principal", Representation: "table"}, grants.Next, 50)
	require.NoError(t, err)
	require.Equal(t, g2, grantBack)
	grantMatch, err := service.QueryGrants(ctx, CollectionQuery{Principal: "faraway", Target: "self-service", Effect: "allow", State: "active"}, nil, 50)
	require.NoError(t, err)
	require.Len(t, grantMatch.Items, 1)
	require.Equal(t, id(128), grantMatch.Items[0].Grant.PrincipalID)
	require.Equal(t, contract.CollectionRange{TotalCount: 1, Offset: 0}, grantMatch.CollectionRange)

	_, err = repository.PatchPrincipal(ctx, id(128), PatchPrincipalRequest{ExpectedRevision: "1", DisplayName: strPtr("Moved")})
	require.NoError(t, err)
	_, err = service.QueryPrincipals(ctx, query, first.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	_, err = service.QueryGrants(ctx, CollectionQuery{Sort: "principal", Representation: "table"}, grants.Next, 50)
	require.ErrorIs(t, err, ErrStaleCursor)
	matched, err = service.QueryPrincipals(ctx, CollectionQuery{Name: "faraway"}, nil, 50)
	require.NoError(t, err)
	require.Empty(t, matched.Items)
	require.Equal(t, contract.CollectionRange{}, matched.CollectionRange)
	grantMatch, err = service.QueryGrants(ctx, CollectionQuery{Principal: "faraway"}, nil, 50)
	require.NoError(t, err)
	require.Empty(t, grantMatch.Items)
	require.Equal(t, contract.CollectionRange{}, grantMatch.CollectionRange)
}

func TestCollectionQueryExpiryRenameAndValidation(t *testing.T) {
	repository, store := newRepository(t, nil)
	seedPrincipal(t, store, principalRow{id: id(1), displayName: "Agent"})
	expires := testNow.Add(time.Second)
	seedGrant(t, store, grantRow{id: id(11), principalID: id(1), serverID: id(51), expiresAt: &expires})
	seedGrant(t, store, grantRow{id: id(12), principalID: id(1), serverID: id(51)})
	names := collectionTargets{id(51): "Remote label"}
	service, err := NewCollectionService(repository, names)
	require.NoError(t, err)
	ctx := context.Background()
	query := CollectionQuery{Sort: "target", State: "active"}
	page, err := service.QueryGrants(ctx, query, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, page.Next)
	require.Equal(t, 2, page.TotalCount)
	names[id(51)] = "Renamed label"
	_, err = service.QueryGrants(ctx, query, page.Next, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	page, err = service.QueryGrants(ctx, query, nil, 1)
	require.NoError(t, err)
	repository.clock = &fixedClock{now: expires}
	_, err = service.QueryGrants(ctx, query, page.Next, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	filtered, err := service.QueryGrants(ctx, CollectionQuery{State: "expired"}, nil, 50)
	require.NoError(t, err)
	require.Len(t, filtered.Items, 1)
	require.Equal(t, contract.GrantExpired, filtered.Items[0].Grant.State)
	require.Equal(t, contract.CollectionRange{TotalCount: 1}, filtered.CollectionRange)
	active, err := service.QueryGrants(ctx, query, nil, 50)
	require.NoError(t, err)
	require.Len(t, active.Items, 1)
	require.Equal(t, contract.CollectionRange{TotalCount: 1}, active.CollectionRange)
	require.Nil(t, active.Next)
	for _, query := range []CollectionQuery{{Sort: "DROP TABLE"}, {Direction: "backward"}, {State: "unknown"}, {Name: "\x00"}, {Target: "wrong domain"}} {
		_, err := service.QueryPrincipals(ctx, query, nil, 50)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
}

func TestEmptyCollectionQueryRanges(t *testing.T) {
	repository, _ := newRepository(t, nil)
	service, err := NewCollectionService(repository, collectionTargets{})
	require.NoError(t, err)
	principals, err := service.QueryPrincipals(context.Background(), CollectionQuery{Sort: "name"}, nil, 50)
	require.NoError(t, err)
	require.Empty(t, principals.Items)
	require.Nil(t, principals.Next)
	require.Equal(t, contract.CollectionRange{}, principals.CollectionRange)
	grants, err := service.QueryGrants(context.Background(), CollectionQuery{Representation: "table"}, nil, 50)
	require.NoError(t, err)
	require.Empty(t, grants.Items)
	require.Nil(t, grants.Next)
	require.Equal(t, contract.CollectionRange{}, grants.CollectionRange)
}

func strPtr(value string) *string { return &value }
