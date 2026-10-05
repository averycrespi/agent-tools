package storage

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func waitMutationOccupancy(t *testing.T, store *Store, owned bool, waiting int) {
	t.Helper()
	require.Eventually(t, func() bool { active, queued := store.MutationOccupancy(); return active == owned && queued == waiting }, time.Second, time.Millisecond)
}

func TestControlMutationNeverQueues(t *testing.T) {
	store := &Store{}
	require.NoError(t, store.acquireMutation(t.Context()))
	var workers sync.WaitGroup
	for range 64 {
		workers.Go(func() { require.ErrorIs(t, store.acquireMutation(t.Context()), ErrMutationBusy) })
	}
	workers.Wait()
	waitMutationOccupancy(t, store, true, 0)
	store.releaseMutation()
	require.NoError(t, store.acquireMutation(t.Context()))
	store.releaseMutation()
	waitMutationOccupancy(t, store, false, 0)
}

func TestControlMutationOwnsActualSettlement(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- store.Mutate(t.Context(), func(tx *sql.Tx) error {
			close(entered)
			<-release
			_, err := tx.ExecContext(t.Context(), `UPDATE gateway_meta SET revision=revision+1`)
			return err
		})
	}()
	<-entered
	require.ErrorIs(t, store.Mutate(t.Context(), func(*sql.Tx) error { t.Error("competing callback executed"); return nil }), ErrMutationBusy)
	waitMutationOccupancy(t, store, true, 0)
	unblock()
	require.NoError(t, <-done)
	identity, err := store.Identity(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 1, identity.Revision)
	require.False(t, store.Latched())
	waitMutationOccupancy(t, store, false, 0)
}

func TestControlMutationPreWorkRefusalsHaveNoIntent(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	var calls atomic.Int32
	callback := func(*sql.Tx) error { calls.Add(1); return nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, store.Mutate(ctx, callback), context.Canceled)
	require.NoError(t, store.acquireMutation(t.Context()))
	require.ErrorIs(t, store.Mutate(t.Context(), callback), ErrMutationBusy)
	require.ErrorIs(t, store.MutateAgentCredentialCandidate(t.Context(), recoveryCandidate(), callback), ErrMutationBusy)
	store.releaseMutation()
	require.Zero(t, calls.Load())
	require.False(t, store.Latched())
	_, err = os.Lstat(owner.Layout().MutationMarker)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, store.Mutate(t.Context(), callback))
	require.EqualValues(t, 1, calls.Load())
}
