package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/ncruces/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMutateCommitsWithoutFilesystemIntent(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		for _, path := range store.marker.paths() {
			_, err := os.Lstat(path)
			require.ErrorIs(t, err, os.ErrNotExist)
		}
		_, err := tx.ExecContext(t.Context(), `UPDATE gateway_meta SET revision=revision+1 WHERE singleton=1`)
		return err
	}))
	identity, err := store.Identity(t.Context())
	require.NoError(t, err)
	require.Equal(t, uint64(1), identity.Revision)
	require.False(t, store.Latched())
}

func TestMutateRejectsWithoutQueuing(t *testing.T) {
	store, err := Initialize(t.Context(), newOwnership(t), testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- store.Mutate(context.Background(), func(*sql.Tx) error { close(entered); <-release; return nil })
	}()
	<-entered
	require.ErrorIs(t, store.Mutate(t.Context(), func(*sql.Tx) error { return nil }), ErrMutationBusy)
	close(release)
	require.NoError(t, <-finished)
}

func TestKnownRollbackButStorageFailureLatches(t *testing.T) {
	store, err := Initialize(t.Context(), newOwnership(t), testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	domainErr := errors.New("domain conflict")
	require.ErrorIs(t, store.Mutate(t.Context(), func(*sql.Tx) error { return domainErr }), domainErr)
	require.False(t, store.Latched())
	require.ErrorIs(t, store.Mutate(t.Context(), func(*sql.Tx) error { return sqlite3.IOERR }), ErrStorageLatched)
	require.True(t, store.Latched())
}

func TestDatabaseSizeRemainsFailClosed(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	reopened, err := openWithOptions(t.Context(), owner, testOptions{databaseByteLimit: 1})
	require.NoError(t, err)
	require.True(t, reopened.Latched())
	require.ErrorIs(t, reopened.Mutate(t.Context(), func(*sql.Tx) error { return nil }), ErrStorageLatched)
	require.NoError(t, reopened.Close())
}

func TestUncleanRestartRunsIntegrityVerification(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, owner.Close())
	require.NoError(t, os.WriteFile(filepath.Join(root, gatewaypaths.DatabaseName), []byte("corrupt"), 0600))
	restarted, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, restarted.Close()) }()
	require.True(t, restarted.WasUnclean())
	_, err = Open(t.Context(), restarted)
	require.Error(t, err)
}

func TestSuccessfulQueriesNeverClearLatch(t *testing.T) {
	store, err := initializeWithOptions(t.Context(), newOwnership(t), testInstallationID, testOptions{fault: failOnce(FaultAfterCommit)})
	require.NoError(t, err)
	require.ErrorIs(t, store.Mutate(t.Context(), func(*sql.Tx) error { return nil }), ErrStorageLatched)
	_, err = store.Identity(t.Context())
	require.NoError(t, err)
	_, err = store.Settings(t.Context())
	require.NoError(t, err)
	assert.True(t, store.Latched())
	require.NoError(t, store.Close())
}

func failOnce(want FaultPoint) func(FaultPoint) error {
	failed := false
	return func(got FaultPoint) error {
		if !failed && got == want {
			failed = true
			return errors.New("injected mutation fault")
		}
		return nil
	}
}
