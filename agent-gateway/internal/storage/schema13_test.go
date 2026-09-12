package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrantDescriptionSchemaPreservesExistingNames(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	writePopulatedSchemaNineFixture(t, ctx, ownership)

	fixture, err := openConfigured(ctx, ownership.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, fixture.migrateThrough(ctx, 9, 12))
	_, err = fixture.database.ExecContext(ctx, `INSERT INTO grants (
		id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at, name
	) VALUES ('01J60000000000000000000060', '01J60000000000000000000020', 'allow',
		'00000000000000000000000000', NULL, NULL, NULL, '2026-08-26T00:00:00Z', 'Deployment access')`)
	require.NoError(t, err)
	require.NoError(t, fixture.Checkpoint(ctx))
	require.NoError(t, fixture.Close())

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	var description *string
	var revision int64
	require.NoError(t, store.database.QueryRowContext(ctx,
		`SELECT description, revision FROM grants WHERE id = '01J60000000000000000000060'`).Scan(&description, &revision))
	require.NotNil(t, description)
	assert.Equal(t, "Deployment access", *description)
	assert.Equal(t, int64(1), revision)
}

func TestGrantDescriptionSchemaAllowsOptionalBoundedDescriptions(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	assert.GreaterOrEqual(t, CurrentSchema, 13)
	assert.Equal(t, expectedMigrationVersions(), mustMigrationVersions(t, store, ctx))
	columns, err := store.database.QueryContext(ctx, `PRAGMA table_info(grants)`)
	require.NoError(t, err)
	defer func() { require.NoError(t, columns.Close()) }()
	foundDescription, foundRevision, foundName := false, false, false
	for columns.Next() {
		var sequence, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		require.NoError(t, columns.Scan(&sequence, &name, &kind, &notNull, &defaultValue, &primaryKey))
		switch name {
		case "description":
			foundDescription = true
			assert.Equal(t, "TEXT", kind)
			assert.Zero(t, notNull)
		case "revision":
			foundRevision = true
			assert.Equal(t, "INTEGER", kind)
			assert.Equal(t, 1, notNull)
		case "name":
			foundName = true
		}
	}
	require.NoError(t, columns.Err())
	assert.True(t, foundDescription)
	assert.True(t, foundRevision)
	assert.False(t, foundName)
}
