package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrantNameSchemaBackfillsExistingGrants(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	writePopulatedSchemaNineFixture(t, ctx, ownership)

	raw := openRaw(t, ownership.Layout().Database)
	_, err := raw.ExecContext(ctx, `INSERT INTO grants (
		id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at
	) VALUES ('01J60000000000000000000060', '01J60000000000000000000020', 'allow',
		'00000000000000000000000000', NULL, NULL, NULL, '2026-08-26T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	var name string
	require.NoError(t, store.database.QueryRowContext(ctx,
		`SELECT name FROM grants WHERE id = '01J60000000000000000000060'`).Scan(&name))
	assert.Equal(t, "Grant 01J60000000000000000000060", name)
}

func TestGrantNameSchemaRequiresBoundedNames(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	assert.Equal(t, 12, CurrentSchema)
	assert.Equal(t, expectedMigrationVersions(), mustMigrationVersions(t, store, ctx))

	columns, err := store.database.QueryContext(ctx, `PRAGMA table_info(grants)`)
	require.NoError(t, err)
	defer func() { require.NoError(t, columns.Close()) }()
	found := false
	for columns.Next() {
		var sequence, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		require.NoError(t, columns.Scan(&sequence, &name, &kind, &notNull, &defaultValue, &primaryKey))
		if name == "name" {
			found = true
			assert.Equal(t, "TEXT", kind)
			assert.Equal(t, 1, notNull)
		}
	}
	require.NoError(t, columns.Err())
	assert.True(t, found, "grants.name must exist")
}
