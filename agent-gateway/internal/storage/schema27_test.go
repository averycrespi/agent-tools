package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigurationTargetMigrationPreservesRowsAndSequence(t *testing.T) {
	owner := newOwnership(t)
	ctx := t.Context()
	writePopulatedSchemaEightFixture(t, ctx, owner)
	prior, err := openConfigured(ctx, owner.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, prior.migrateThrough(ctx, 8, 26))
	const target = "01J60000000000000000000081"
	const grant = "01J60000000000000000000082"
	const stamp = "2026-08-25T00:00:00.000000000Z"
	_, err = prior.database.ExecContext(ctx, `INSERT INTO git_repositories(id,name,url,aliases_json,revision,alias_revision,deleted,created_at,updated_at) VALUES(?,'Repo','https://example.com:443/repo','[]',1,1,1,?,?)`, target, stamp, stamp)
	require.NoError(t, err)
	for _, v := range []struct{ kind, id, key string }{{"git_repository", target, "retained"}, {"git_grant", grant, "retained-grant"}} {
		_, err = prior.database.ExecContext(ctx, `INSERT INTO resource_keys VALUES(?,?,?)`, v.kind, v.id, v.key)
		require.NoError(t, err)
	}
	_, err = prior.database.ExecContext(ctx, `INSERT INTO git_grants(insertion_sequence,id,principal_id,repository_id,description,revision,policy_json,expires_at,created_at,updated_at) SELECT 70,?,id,?,'Retained',4,'{"version":1,"read":true,"refs":[]}',NULL,?,? FROM principals LIMIT 1`, grant, target, stamp, stamp)
	require.NoError(t, err)
	// Deletion must not allow the new table to reuse a historical sequence.
	_, err = prior.database.ExecContext(ctx, `UPDATE sqlite_sequence SET seq=90 WHERE name='git_grants'`)
	require.NoError(t, err)
	tables := []string{"git_grants", "git_repositories", "resource_keys", "principals", "authorization_meta"}
	before := map[string][][]any{}
	for _, table := range tables {
		before[table] = snapshotRows(t, prior.database, table)
	}
	require.NoError(t, prior.Checkpoint(ctx))
	require.NoError(t, prior.Close())
	current, err := Open(ctx, owner)
	require.NoError(t, err)
	for _, table := range tables {
		require.Equal(t, before[table], snapshotRows(t, current.database, table), table)
	}
	var sequence int
	require.NoError(t, current.database.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name='git_grants'`).Scan(&sequence))
	require.Equal(t, 90, sequence)
	require.NoError(t, current.verifyMigrationStructure(ctx, "027_configuration_targets.sql"))
	const reserved = "01J60000000000000000000083"
	_, err = current.database.ExecContext(ctx, `INSERT INTO resource_keys VALUES('git_repository',?,'key-only')`, reserved)
	require.NoError(t, err)
	_, err = current.database.ExecContext(ctx, `INSERT INTO git_grants(id,principal_id,repository_id,revision,policy_json,created_at,updated_at) SELECT '01J60000000000000000000084',id,?,1,'{"version":1,"read":true,"refs":[]}',?,? FROM principals LIMIT 1`, reserved, stamp, stamp)
	require.NoError(t, err)
	require.NoError(t, current.database.QueryRowContext(ctx, `SELECT insertion_sequence FROM git_grants WHERE id='01J60000000000000000000084'`).Scan(&sequence))
	require.Equal(t, 91, sequence)
	_, err = current.database.ExecContext(ctx, `UPDATE git_grants SET repository_id='01J60000000000000000000099'`)
	require.Error(t, err)
	require.NoError(t, current.Close())
}

func TestConfigurationTargetStartupRejectsChangedEnforcement(t *testing.T) {
	owner := newOwnership(t)
	ctx := t.Context()
	store, err := Initialize(ctx, owner, testInstallationID)
	require.NoError(t, err)
	_, err = store.database.ExecContext(ctx, `DROP TRIGGER git_grants_reserved_target`)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	_, err = Open(ctx, owner)
	require.ErrorIs(t, err, ErrInvalidDatabase)
}
