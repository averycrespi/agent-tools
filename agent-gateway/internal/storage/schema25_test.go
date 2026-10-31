package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigurationImportMigrationPreservesExistingAuthorityAndChecksDDL(t *testing.T) {
	owner := newOwnership(t)
	writePopulatedSchemaEightFixture(t, t.Context(), owner)
	prior, err := openConfigured(t.Context(), owner.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, prior.migrateThrough(t.Context(), 8, 24))
	var before string
	require.NoError(t, prior.database.QueryRow(`SELECT group_concat(id || ':' || display_name || ':' || state) FROM principals`).Scan(&before))
	require.NoError(t, prior.Checkpoint(t.Context()))
	require.NoError(t, prior.Close())
	current, err := Open(t.Context(), owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, current.Close()) }()
	var after string
	require.NoError(t, current.database.QueryRow(`SELECT group_concat(id || ':' || display_name || ':' || state) FROM principals`).Scan(&after))
	require.Equal(t, before, after)
	for _, table := range []string{"configuration_import", "server_import_activation", "configuration_import_grant_expiries"} {
		var count int
		require.NoError(t, current.database.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count, table)
	}
	_, err = current.database.Exec(`INSERT INTO configuration_import VALUES(1,'bad','bad')`)
	require.Error(t, err)
	require.NoError(t, current.verifyMigrationStructure(t.Context(), "025_configuration_import.sql"))
	_, err = current.database.Exec(`ALTER TABLE configuration_import_grant_expiries ADD COLUMN bypass INTEGER`)
	require.NoError(t, err)
	require.Error(t, current.verifyMigrationStructure(t.Context(), "025_configuration_import.sql"))
}
