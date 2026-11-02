package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommittedMutationSurvivesLostAcknowledgmentWithoutMarker(t *testing.T) {
	ctx := context.Background()
	owner := newOwnership(t)
	store, err := initializeWithOptions(ctx, owner, testInstallationID, testOptions{fault: failOnce(FaultAfterCommit)})
	require.NoError(t, err)
	require.ErrorIs(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE gateway_meta SET revision=revision+1 WHERE singleton=1`)
		return err
	}), ErrStorageLatched)
	require.ErrorIs(t, store.Mutate(ctx, func(*sql.Tx) error { t.Fatal("latched callback replayed"); return nil }), ErrStorageLatched)
	require.NoError(t, store.Close())
	_, err = os.Lstat(owner.Layout().MutationMarker)
	require.ErrorIs(t, err, os.ErrNotExist)
	reopened, err := Open(ctx, owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	require.False(t, reopened.Latched())
	identity, err := reopened.Identity(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), identity.Revision)
}

func TestCancelledMutationSettlesRollbackBeforeNextAdmission(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	ctx, cancel := context.WithCancel(context.Background())
	err = store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE gateway_meta SET revision=revision+1 WHERE singleton=1`)
		require.NoError(t, err)
		cancel()
		return ctx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, store.Latched())
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		var revision int
		require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT revision FROM gateway_meta`).Scan(&revision))
		require.Zero(t, revision)
		return nil
	}))
}

func TestRotationInspectionAuthenticatesCommittedWAL(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE gateway_meta SET revision=revision+1 WHERE singleton=1`)
		return err
	}))
	info, err := os.Stat(owner.Layout().Database + "-wal")
	require.NoError(t, err)
	require.Positive(t, info.Size())
	for _, suffix := range []string{"-wal", "-shm"} {
		require.NoError(t, os.Chmod(owner.Layout().Database+suffix, 0600))
	}
	snapshot, err := InspectRotation(t.Context(), owner, func(tx *sql.Tx) error {
		var revision int
		err := tx.QueryRowContext(t.Context(), `SELECT revision FROM gateway_meta`).Scan(&revision)
		require.Equal(t, 1, revision)
		return err
	})
	require.NoError(t, err)
	require.True(t, snapshot.walAware)
	require.NoError(t, snapshot.Revalidate(t.Context(), owner))
}

func TestKnownRollbackDoesNotCreateFilesystemIntent(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	sentinel := errors.New("policy rejected")
	require.ErrorIs(t, store.Mutate(t.Context(), func(*sql.Tx) error { return sentinel }), sentinel)
	require.False(t, store.Latched())
	for _, path := range store.marker.paths() {
		_, err := os.Lstat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}
