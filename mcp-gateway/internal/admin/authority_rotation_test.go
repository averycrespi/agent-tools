package admin

import (
	"context"
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminAuthorityConditionalRotation(t *testing.T) {
	ctx := context.Background()

	t.Run("conditional create and completion advance authority", func(t *testing.T) {
		store, _ := newStore(t)
		service := NewService(store, testutil.NewFakeClock(testNow), newDeterministicEntropy())
		initialSink := new(memorySink)
		old, err := service.Initialize(ctx, initialSink)
		require.NoError(t, err)

		authority, err := service.Authority(ctx)
		require.NoError(t, err)
		assert.Equal(t, contract.AdminAuthority{Revision: "1"}, authority)
		require.NoError(t, store.Mutate(ctx, func(transaction *sql.Tx) error {
			_, updateErr := transaction.ExecContext(ctx, `UPDATE gateway_meta SET revision = revision + 1 WHERE singleton = 1`)
			return updateErr
		}))
		authority, err = service.Authority(ctx)
		require.NoError(t, err)
		assert.Equal(t, "1", authority.Revision, "unrelated Gateway revisions must not change administrator authority")

		_, err = service.CreateConditional(ctx, nil, "0")
		assert.ErrorIs(t, err, ErrStaleAuthority)
		items, err := service.List(ctx)
		require.NoError(t, err)
		assert.Len(t, items, 1)

		replacement, err := service.CreateConditional(ctx, nil, authority.Revision)
		require.NoError(t, err)
		var invalidated *string
		unsubscribe := service.SubscribeCredentialInvalidations(func(id *string) { invalidated = id })
		defer unsubscribe()
		assert.Equal(t, "3", replacement.Revision)
		_, err = service.Authenticate(ctx, replacement.Bearer)
		require.NoError(t, err)

		result, err := service.CompleteRotation(ctx, old.ID, replacement.ID, replacement.Revision)
		require.NoError(t, err)
		assert.Equal(t, contract.CredentialRevoked, result.OldCredential.Status)
		assert.Equal(t, "4", result.OldCredential.Revision)
		assert.Equal(t, contract.CredentialActive, result.NewCredential.Status)
		assert.True(t, result.NewCredential.NonExpiring)
		assert.Equal(t, replacement.ID, result.NewCredential.ID)
		require.NotNil(t, invalidated)
		assert.Equal(t, old.ID, *invalidated)
		_, err = service.Authenticate(ctx, initialSink.value)
		assert.ErrorIs(t, err, ErrAuthenticationRequired)
		_, err = service.Authenticate(ctx, replacement.Bearer)
		require.NoError(t, err)
		authority, err = service.Authority(ctx)
		require.NoError(t, err)
		assert.Equal(t, "4", authority.Revision)
	})

	t.Run("stale and invalid state never revoke old authority", func(t *testing.T) {
		store, _ := newStore(t)
		clock := testutil.NewFakeClock(testNow)
		service := NewService(store, clock, newDeterministicEntropy())
		oldSink := new(memorySink)
		old, err := service.Initialize(ctx, oldSink)
		require.NoError(t, err)
		replacement, err := service.CreateConditional(ctx, nil, "1")
		require.NoError(t, err)

		_, err = service.CompleteRotation(ctx, old.ID, replacement.ID, "1")
		assert.ErrorIs(t, err, ErrStaleAuthority)
		_, err = service.Authenticate(ctx, oldSink.value)
		require.NoError(t, err)

		require.NoError(t, service.Revoke(ctx, replacement.ID))
		authority, err := service.Authority(ctx)
		require.NoError(t, err)
		_, err = service.CompleteRotation(ctx, old.ID, replacement.ID, authority.Revision)
		assert.ErrorIs(t, err, ErrRotationConflict)
		_, err = service.Authenticate(ctx, oldSink.value)
		require.NoError(t, err)

		expires := clock.Now().Add(contract.CredentialMinimumLifetime)
		expiring, err := service.CreateConditional(ctx, &expires, authority.Revision)
		require.NoError(t, err)
		_, err = service.CompleteRotation(ctx, old.ID, expiring.ID, expiring.Revision)
		assert.ErrorIs(t, err, ErrRotationConflict)
		_, err = service.Authenticate(ctx, oldSink.value)
		require.NoError(t, err)
	})

	t.Run("reset between create and completion invalidates the workflow", func(t *testing.T) {
		store, _ := newStore(t)
		service := NewService(store, testutil.NewFakeClock(testNow), newDeterministicEntropy())
		oldSink := new(memorySink)
		old, err := service.Initialize(ctx, oldSink)
		require.NoError(t, err)
		replacement, err := service.CreateConditional(ctx, nil, "1")
		require.NoError(t, err)
		resetSink := new(memorySink)
		_, err = service.Reset(ctx, resetSink)
		require.NoError(t, err)
		authority, err := service.Authority(ctx)
		require.NoError(t, err)
		_, err = service.CompleteRotation(ctx, old.ID, replacement.ID, authority.Revision)
		assert.ErrorIs(t, err, ErrRotationConflict)
	})
}
