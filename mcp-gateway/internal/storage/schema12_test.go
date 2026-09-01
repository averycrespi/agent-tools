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

	store, err := openConfigured(ctx, ownership.Layout(), testOptions{})
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	_, err = store.database.ExecContext(ctx, `INSERT INTO grants (
		id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at
	) VALUES ('01J60000000000000000000060', '01J60000000000000000000020', 'allow',
		'00000000000000000000000000', NULL, NULL, NULL, '2026-08-26T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, store.migrateThrough(ctx, 9, 12))
	var name string
	require.NoError(t, store.database.QueryRowContext(ctx,
		`SELECT name FROM grants WHERE id = '01J60000000000000000000060'`).Scan(&name))
	assert.Equal(t, "Grant 01J60000000000000000000060", name)
}

func TestGrantNameSchemaRequiresBoundedNames(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	writePopulatedSchemaNineFixture(t, ctx, ownership)
	store, err := openConfigured(ctx, ownership.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, store.migrateThrough(ctx, 9, 12))
	defer func() { require.NoError(t, store.Close()) }()

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
	assert.True(t, found, "schema-12 grants.name must exist")
}
