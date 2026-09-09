package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadOnlyMigrationPreservesLegacyRowsAndDedupe(t *testing.T) {
	ownership := newOwnership(t)
	writePopulatedSchemaNineFixture(t, t.Context(), ownership)
	fixture, err := openConfigured(t.Context(), ownership.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, fixture.migrateThrough(t.Context(), 9, 15))
	_, err = fixture.database.ExecContext(t.Context(), `INSERT INTO grant_requests (id,principal_id,state,revision,resolved_server_id,requested_scope,requested_target,requested_future_tools_acknowledged,dedupe_version,dedupe_bytes,created_at,updated_at) VALUES ('01J60000000000000000000062','01J60000000000000000000020','pending',1,'01J60000000000000000000040','server','sample',1,1,?,'2026-08-27T00:00:00Z','2026-08-27T00:00:00Z')`, []byte("legacy-dedupe"))
	require.NoError(t, err)
	require.NoError(t, fixture.Checkpoint(t.Context()))
	require.NoError(t, fixture.Close())
	store, err := Open(t.Context(), ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	var requested, approved, version, restricted int
	var dedupe []byte
	require.NoError(t, store.database.QueryRowContext(t.Context(), `SELECT requested_read_only,approved_read_only,dedupe_version,dedupe_bytes FROM grant_requests`).Scan(&requested, &approved, &version, &dedupe))
	require.Zero(t, requested)
	require.Zero(t, approved)
	require.Equal(t, 1, version)
	require.Equal(t, []byte("legacy-dedupe"), dedupe)
	require.NoError(t, store.database.QueryRowContext(t.Context(), `SELECT count(*) FROM grants WHERE read_only <> 0`).Scan(&restricted))
	require.Zero(t, restricted)
	_, err = store.database.ExecContext(t.Context(), `UPDATE grant_requests SET requested_read_only=1`)
	require.Error(t, err)
}

func TestReadOnlySchemaRejectsChangedEnforcement(t *testing.T) {
	ownership := newOwnership(t)
	store, err := Initialize(t.Context(), ownership, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	raw := openRaw(t, ownership.Layout().Database)
	_, err = raw.ExecContext(t.Context(), `DROP TRIGGER grants_read_only_immutable`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	_, err = Open(t.Context(), ownership)
	require.ErrorIs(t, err, ErrInvalidDatabase)
}
