package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen_CreatesWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	defer db.Close()

	var mode string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&mode))
	assert.Equal(t, "wal", mode)

	var busy int
	require.NoError(t, db.QueryRow("PRAGMA busy_timeout").Scan(&busy))
	assert.Equal(t, 5000, busy)
}

func TestMigrations_AreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	db, err := store.Open(path)
	require.NoError(t, err)
	v1, err := store.UserVersion(db)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// Reopen — should not re-run migrations.
	db, err = store.Open(path)
	require.NoError(t, err)
	v2, err := store.UserVersion(db)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	assert.Equal(t, v1, v2)
	assert.Greater(t, v1, 0)
}

func TestMigration_AtomicRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	// Swap migrations to a single failing migration that creates a table then errors.
	old := store.SetMigrationsForTest([]func(*sql.Tx) error{
		func(tx *sql.Tx) error {
			if _, err := tx.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)"); err != nil {
				return err
			}
			return errors.New("boom")
		},
	})
	defer store.SetMigrationsForTest(old)

	_, err := store.Open(path)
	require.Error(t, err)

	// Inspect the DB directly (bypass migrations) to verify nothing persisted.
	raw, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer raw.Close()

	var n int
	err = raw.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='t'").Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "failing migration's CREATE TABLE must roll back with user_version bump")

	var version int
	err = raw.QueryRow("PRAGMA user_version").Scan(&version)
	require.NoError(t, err)
	assert.Equal(t, 0, version, "user_version must remain 0 after failed migration")
}
