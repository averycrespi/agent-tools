package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeCleanupMigrationStartsEmptyAndPreservesEvidence(t *testing.T) {
	owner := newOwnership(t)
	writePopulatedSchemaEightFixture(t, t.Context(), owner)
	prior, err := openConfigured(t.Context(), owner.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, prior.migrateThrough(t.Context(), 8, 23))
	require.NoError(t, prior.Checkpoint(t.Context()))
	require.NoError(t, prior.Close())
	current, err := Open(t.Context(), owner)
	require.NoError(t, err)
	var count int
	require.NoError(t, current.database.QueryRow(`SELECT count(*) FROM native_cleanup`).Scan(&count))
	require.Zero(t, count)
	_, err = current.database.Exec(`INSERT INTO native_cleanup(handle,owner,kind,revision,chunks) VALUES('handle','owner','http_ca',1,2)`)
	require.NoError(t, err)
	_, err = current.database.Exec(`INSERT INTO native_cleanup_items(handle,item,state) VALUES('handle',-1,'uncertain')`)
	require.NoError(t, err)
	require.NoError(t, current.Checkpoint(t.Context()))
	require.NoError(t, current.Close())
	current, err = Open(t.Context(), owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, current.Close()) }()
	var state string
	require.NoError(t, current.database.QueryRow(`SELECT state FROM native_cleanup_items`).Scan(&state))
	require.Equal(t, "uncertain", state)
	require.NoError(t, current.verifyMigrationStructure(t.Context(), "024_native_cleanup.sql"))
	_, err = current.database.Exec(`ALTER TABLE native_cleanup_items ADD COLUMN bypass INTEGER`)
	require.NoError(t, err)
	require.Error(t, current.verifyMigrationStructure(t.Context(), "024_native_cleanup.sql"))
}
