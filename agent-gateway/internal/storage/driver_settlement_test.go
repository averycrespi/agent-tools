package storage

import (
	"database/sql"
	"errors"
	"testing"

	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/require"
)

func TestActualDriverFailedCommitSettlesBeforeConnectionReuse(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	err = store.Mutate(t.Context(), func(tx *sql.Tx) error {
		for _, query := range []string{
			`CREATE TEMP TABLE settlement_parent (id INTEGER PRIMARY KEY)`,
			`CREATE TEMP TABLE settlement_child (id INTEGER REFERENCES settlement_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
			`INSERT INTO settlement_child VALUES (1)`,
			`UPDATE gateway_meta SET revision=revision+1 WHERE singleton=1`,
		} {
			if _, err := tx.ExecContext(t.Context(), query); err != nil {
				return err
			}
		}
		return nil
	})
	require.ErrorContains(t, err, "commit security mutation")
	require.ErrorIs(t, err, ErrStorageLatched)
	require.Empty(t, store.pendingConnections, "ncruces Commit synchronously rolls back failed deferred constraint")
	identity, err := store.Identity(t.Context())
	require.NoError(t, err)
	require.Zero(t, identity.Revision)
	require.NoError(t, store.Close())
	reopened, err := Open(t.Context(), owner)
	require.NoError(t, err)
	require.False(t, reopened.Latched())
	require.NoError(t, reopened.Close())
}

func TestActualDriverRollbackErrorDoesNotInferSettlementFromTxDone(t *testing.T) {
	store, err := Initialize(t.Context(), newOwnership(t), testInstallationID)
	require.NoError(t, err)
	err = store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `ROLLBACK`)
		require.NoError(t, err)
		return errors.New("domain failure after driver rollback")
	})
	require.ErrorIs(t, err, ErrStorageLatched)
	require.ErrorContains(t, err, "no transaction is active")
	require.Empty(t, store.pendingConnections, "actual driver autocommit proves this connection settled despite rollback error")
	require.NoError(t, store.Close())
}

func TestUnsettledActualConnectionIsQuarantinedNotPooled(t *testing.T) {
	store, err := Initialize(t.Context(), newOwnership(t), testInstallationID)
	require.NoError(t, err)
	connection, err := store.database.Conn(t.Context())
	require.NoError(t, err)
	require.NoError(t, connection.Raw(func(raw any) error { return raw.(sqliteDriver.Conn).Raw().Exec(`BEGIN IMMEDIATE`) }))
	require.ErrorIs(t, store.finishConnection(connection), ErrStorageLatched)
	require.Len(t, store.pendingConnections, 1)
	require.ErrorIs(t, store.Mutate(t.Context(), func(*sql.Tx) error { t.Fatal("quarantine allowed reuse"); return nil }), ErrStorageLatched)
	require.ErrorIs(t, store.Close(), ErrStorageLatched)
	// Fixture-owned observation of a later actual settlement is not mutation replay.
	require.NoError(t, connection.Raw(func(raw any) error { return raw.(sqliteDriver.Conn).Raw().Exec(`ROLLBACK`) }))
	require.NoError(t, store.Close())
	require.Empty(t, store.pendingConnections)
}
