//go:build integration

package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchemaSevenFixtureMigratesWithRealSQLite(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	copyFixture(t, "testdata/schema-v7.db", ownership.Layout().Database)

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	assertPopulatedSchemaSevenFacts(t, ctx, store.database)
	assertSchemaEightFoundation(t, ctx, store.database)
	assertSchemaNineInvocationFoundation(t, ctx, store.database)
}

func TestPopulatedSchemaEightMigratesToNineWithRealSQLite(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	writePopulatedSchemaEightFixture(t, ctx, ownership)

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	assertSchemaNineInvocationFoundation(t, ctx, store.database)
	var principals int
	require.NoError(t, store.database.QueryRowContext(ctx, `SELECT count(*) FROM principals`).Scan(&principals))
	assert.Equal(t, 1, principals)
}

func TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	connections := make([]*sql.Conn, 0, connectionLimit)
	for range connectionLimit {
		connection, err := store.database.Conn(ctx)
		require.NoError(t, err)
		connections = append(connections, connection)
		assertConnectionSettings(t, ctx, connection)
	}
	for _, connection := range connections {
		require.NoError(t, connection.Close())
	}

	_, err = store.database.ExecContext(ctx, `CREATE TABLE busy_probe (value INTEGER NOT NULL) STRICT`)
	require.NoError(t, err)
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	require.NoError(t, err)
	defer func() { require.NoError(t, transaction.Rollback()) }()
	_, err = transaction.ExecContext(ctx, `INSERT INTO busy_probe VALUES (1)`)
	require.NoError(t, err)

	contender, err := sql.Open("sqlite3", dataSource(ownership.Layout().Database, false, true))
	require.NoError(t, err)
	defer func() { require.NoError(t, contender.Close()) }()
	started := time.Now()
	_, err = contender.ExecContext(ctx, `INSERT INTO busy_probe VALUES (2)`)
	elapsed := time.Since(started)
	assert.Error(t, err)
	assert.GreaterOrEqual(t, elapsed, time.Second)
	assert.Less(t, elapsed, 5*time.Second)
}

func TestBusyBeyondDeadlineLatchesMutationAcrossRestart(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)

	contender, err := sql.Open("sqlite3", dataSource(ownership.Layout().Database, false, true))
	require.NoError(t, err)
	transaction, err := contender.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	require.NoError(t, err)
	defer func() { _ = transaction.Rollback() }()
	_, err = transaction.ExecContext(ctx, `UPDATE gateway_meta SET revision = revision`)
	require.NoError(t, err)

	started := time.Now()
	err = store.Mutate(ctx, func(*sql.Tx) error {
		t.Fatal("mutation callback ran despite a busy begin")
		return nil
	})
	elapsed := time.Since(started)
	assert.ErrorIs(t, err, ErrStorageLatched)
	assert.GreaterOrEqual(t, elapsed, time.Second)
	assert.Less(t, elapsed, 5*time.Second)
	require.NoError(t, transaction.Rollback())
	require.NoError(t, contender.Close())
	require.NoError(t, store.Close())

	reopened, err := Open(ctx, ownership)
	require.NoError(t, err)
	assert.True(t, reopened.Latched())
	require.NoError(t, reopened.Close())
}

func assertConnectionSettings(t *testing.T, ctx context.Context, connection *sql.Conn) {
	t.Helper()
	checks := []struct {
		query string
		want  any
	}{
		{query: `PRAGMA foreign_keys`, want: int64(1)},
		{query: `PRAGMA synchronous`, want: int64(2)},
		{query: `PRAGMA busy_timeout`, want: int64(BusyTimeoutMilliseconds)},
	}
	for _, check := range checks {
		var got int64
		require.NoError(t, connection.QueryRowContext(ctx, check.query).Scan(&got))
		assert.Equal(t, check.want, got)
	}
	var journalMode string
	require.NoError(t, connection.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode))
	assert.Equal(t, "wal", journalMode)
}
