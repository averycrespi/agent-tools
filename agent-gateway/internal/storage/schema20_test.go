package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPDefaultsMigrationAndStructuralValidation(t *testing.T) {
	owner := newOwnership(t)
	writePopulatedSchemaEightFixture(t, t.Context(), owner)
	prior, err := openConfigured(t.Context(), owner.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, prior.migrateThrough(t.Context(), 8, 19))
	require.NoError(t, prior.Checkpoint(t.Context()))
	require.NoError(t, prior.Close())
	current, err := Open(t.Context(), owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, current.Close()) }()
	var policy string
	var revision int
	require.NoError(t, current.database.QueryRowContext(t.Context(), `SELECT policy,revision FROM http_defaults WHERE principal_id='01J60000000000000000000020'`).Scan(&policy, &revision))
	require.Equal(t, "block", policy)
	require.Equal(t, 1, revision)
	var count int
	require.NoError(t, current.database.QueryRowContext(t.Context(), `SELECT count(*) FROM http_grants`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, current.verifyMigrationStructure(t.Context(), "020_http_grants.sql"))
	_, err = current.database.ExecContext(t.Context(), `DROP TRIGGER principal_http_default`)
	require.NoError(t, err)
	require.Error(t, current.verifyMigrationStructure(t.Context(), "020_http_grants.sql"))
}
