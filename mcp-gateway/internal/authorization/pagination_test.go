package authorization

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestAdministrativeCursorsRejectRestartExpiryAndTampering(t *testing.T) {
	repository, store := newRepository(t, nil)
	for index := 1; index <= 3; index++ {
		seedPrincipal(t, store, principalRow{id: id(index), displayName: "Duplicate"})
		seedGrant(t, store, grantRow{id: id(index + 10), principalID: id(index), serverID: contract.SyntheticServerID})
	}
	ctx := context.Background()
	principals, err := repository.ListPrincipals(ctx, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, principals.Next)
	grants, err := repository.ListGrants(ctx, GrantFilter{}, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, grants.Next)

	restarted, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(bytes.Repeat([]byte{99}, 1024)))
	require.NoError(t, err)
	_, err = restarted.ListPrincipals(ctx, principals.Next, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	_, err = restarted.ListGrants(ctx, GrantFilter{}, grants.Next, 1)
	require.ErrorIs(t, err, ErrStaleCursor)

	tampered := *principals.Next
	tampered.AfterID = id(2)
	_, err = repository.ListPrincipals(ctx, &tampered, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	tampered = *grants.Next
	tampered.Upper++
	_, err = repository.ListGrants(ctx, GrantFilter{}, &tampered, 1)
	require.ErrorIs(t, err, ErrStaleCursor)

	repository.clock = &fixedClock{now: testNow.Add(5 * time.Minute)}
	_, err = repository.ListPrincipals(ctx, principals.Next, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	_, err = repository.ListGrants(ctx, GrantFilter{}, grants.Next, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	fresh, err := repository.ListPrincipals(ctx, nil, 1)
	require.NoError(t, err)
	require.Len(t, fresh.Items, 1)
}

func TestAdministrativeCursorKeyFailureAndWireBound(t *testing.T) {
	repository, store := newRepository(t, nil)
	_, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(make([]byte, 31)))
	require.ErrorContains(t, err, "generate administrative cursor key")
	cursor := SnapshotCursor{Collection: grantCollection, PrincipalID: id(1), ServerID: id(2), Upper: math.MaxInt64, After: math.MaxInt64, AfterID: id(3), Expires: math.MaxInt64}
	repository.sealCursor(&cursor)
	contents, err := json.Marshal(cursor)
	require.NoError(t, err)
	limit, ok := contract.FixedLimitByName("cursor_bytes")
	require.True(t, ok)
	require.LessOrEqual(t, int64(len(base64.RawURLEncoding.EncodeToString(contents))), limit.Maximum)
}
