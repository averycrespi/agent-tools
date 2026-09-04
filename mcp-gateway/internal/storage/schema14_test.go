package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatcherV2SchemaFencePreservesConstraintBytes(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	writePopulatedSchemaNineFixture(t, ctx, ownership)

	fixture, err := openConfigured(ctx, ownership.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, fixture.migrateThrough(ctx, 9, 13))
	v1 := `{"equals":{"/attempt":1.0}}`
	v2 := `{"version":2,"equals":{"/attempt":1e0},"regex":{"/resource":"[a-z]+/[0-9]+"}}`
	for index, constraint := range []string{v1, v2} {
		_, err = fixture.database.ExecContext(ctx, `INSERT INTO grants (
			id, principal_id, effect, server_id, upstream_name, constraint_json,
			expires_at, created_at, description, revision
		) VALUES (?, '01J60000000000000000000020', 'allow',
			'01J60000000000000000000040', 'tool', ?, NULL,
			'2026-08-27T00:00:00Z', NULL, 1)`, []string{"01J60000000000000000000060", "01J60000000000000000000061"}[index], constraint)
		require.NoError(t, err)
	}
	_, err = fixture.database.ExecContext(ctx, `INSERT INTO grant_requests (
		id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
		requested_scope, requested_target, requested_constraint, requested_duration_seconds,
		requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
		approved_scope, approved_target, approved_constraint, approved_duration_seconds,
		approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
		created_at, updated_at, closed_at
	) VALUES ('01J60000000000000000000062', '01J60000000000000000000020', 'pending', 1,
		'01J60000000000000000000040', 'tool', 'tool', 'namespace__tool', ?, NULL, 0, 2,
		?, ?, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
		'2026-08-27T00:00:00Z', '2026-08-27T00:00:00Z', NULL)`, v2, []byte("dedupe-v2"), []byte(`{"descriptor":{"name":"tool"}}`))
	require.NoError(t, err)
	require.NoError(t, fixture.Checkpoint(ctx))
	require.NoError(t, fixture.Close())

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	assert.Equal(t, 14, CurrentSchema)
	assert.Equal(t, expectedMigrationVersions(), mustMigrationVersions(t, store, ctx))
	var migration string
	require.NoError(t, store.database.QueryRowContext(ctx, `SELECT name FROM schema_migrations WHERE version = 14`).Scan(&migration))
	assert.Equal(t, "matcher_v2", migration)

	for index, expected := range []string{v1, v2} {
		var actual string
		require.NoError(t, store.database.QueryRowContext(ctx, `SELECT constraint_json FROM grants WHERE id = ?`, []string{"01J60000000000000000000060", "01J60000000000000000000061"}[index]).Scan(&actual))
		assert.Equal(t, expected, actual)
	}
	var requested string
	require.NoError(t, store.database.QueryRowContext(ctx, `SELECT requested_constraint FROM grant_requests WHERE id = '01J60000000000000000000062'`).Scan(&requested))
	assert.Equal(t, v2, requested)
}
