package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitializePreservesExactSchemaEightAuthorityFoundation(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	assertSchemaEightFoundation(t, ctx, store.database)
	assertSchemaNineInvocationFoundation(t, ctx, store.database)
	assert.Equal(t, expectedMigrationVersions(), mustMigrationVersions(t, store, ctx))
}

func TestOpenMigratesPopulatedSchemaSevenWithoutChangingServerFacts(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	copyFixture(t, "testdata/schema-v7.db", ownership.Layout().Database)

	backupIdentity, err := VerifyBackup(ctx, ownership.Layout().Database)
	require.NoError(t, err)
	assert.Equal(t, 7, backupIdentity.SchemaVersion)

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	assertPopulatedSchemaSevenFacts(t, ctx, store.database)
	assertSchemaEightFoundation(t, ctx, store.database)
	assertSchemaNineInvocationFoundation(t, ctx, store.database)
}

func TestOpenReplacementMigratesAcceptedSchemaSevenBackup(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	staged := filepath.Join(ownership.Layout().Root, "schema-v7-restore.db")
	copyFixture(t, "testdata/schema-v7.db", staged)

	identity, err := VerifyBackup(ctx, staged)
	require.NoError(t, err)
	assert.Equal(t, 7, identity.SchemaVersion)

	replacement, err := OpenReplacement(ctx, ownership, staged)
	require.NoError(t, err)
	defer func() { require.NoError(t, replacement.Close()) }()
	assertPopulatedSchemaSevenFacts(t, ctx, replacement.database)
	assertSchemaEightFoundation(t, ctx, replacement.database)
	assertSchemaNineInvocationFoundation(t, ctx, replacement.database)
}

func TestVerifyBackupRejectsSchemaNewerThanCurrent(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	raw := openRaw(t, ownership.Layout().Database)
	_, err = raw.Exec(`PRAGMA user_version = 12`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	_, err = VerifyBackup(ctx, ownership.Layout().Database)
	require.ErrorIs(t, err, ErrInvalidDatabase)
}

func TestSchemaEightChecksRejectPartialAuthorityRows(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	_, err = store.database.ExecContext(ctx, `INSERT INTO authorization_meta (singleton, revision) VALUES (2, 0)`)
	require.Error(t, err)
	_, err = store.database.ExecContext(ctx, `
		INSERT INTO principals (
			id, display_name, state, visibility, revision, credential_revision,
			credential_id, created_at, updated_at
		) VALUES (?, 'Partial', 'active', 'requestable', 1, 1, ?, ?, ?)`,
		"01J60000000000000000000010", "01J60000000000000000000011", "2026-08-25T00:00:00Z", "2026-08-25T00:00:00Z")
	require.Error(t, err)

	_, err = store.database.ExecContext(ctx, `
		INSERT INTO principals (
			id, display_name, state, visibility, revision, credential_revision,
			created_at, updated_at
		) VALUES (?, 'Valid', 'active', 'requestable', 1, 0, ?, ?)`,
		"01J60000000000000000000012", "2026-08-25T00:00:00Z", "2026-08-25T00:00:00Z")
	require.NoError(t, err)
	_, err = store.database.ExecContext(ctx, `
		INSERT INTO grants (
			id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at
		) VALUES (?, ?, 'allow', ?, NULL, '{"equals":{"/x":1}}', NULL, ?)`,
		"01J60000000000000000000013", "01J60000000000000000000012", contract.SyntheticServerID, "2026-08-25T00:00:00Z")
	require.Error(t, err, "server-wide grants cannot carry constraints")
}

func assertSchemaEightFoundation(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()

	expectedColumns := map[string][]string{
		"synthetic_server_identity": {"singleton", "server_id", "namespace"},
		"authorization_meta":        {"singleton", "revision"},
		"principals": {
			"insertion_sequence", "id", "display_name", "state", "visibility", "revision", "credential_revision",
			"credential_id", "credential_verifier", "credential_fingerprint", "credential_created_at", "created_at", "updated_at",
		},
		"grants": {"insertion_sequence", "id", "principal_id", "effect", "server_id", "upstream_name", "constraint_json", "expires_at", "created_at"},
	}
	for table, expected := range expectedColumns {
		rows, err := database.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		require.NoError(t, err, table)
		var columns []string
		for rows.Next() {
			var column string
			require.NoError(t, rows.Scan(&column))
			columns = append(columns, column)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		assert.Equal(t, expected, columns, table)
	}

	var migrationName string
	require.NoError(t, database.QueryRowContext(ctx, `SELECT name FROM schema_migrations WHERE version = 8`).Scan(&migrationName))
	assert.Equal(t, "authorization", migrationName)

	var serverID, namespace string
	require.NoError(t, database.QueryRowContext(ctx, `SELECT server_id, namespace FROM synthetic_server_identity WHERE singleton = 1`).Scan(&serverID, &namespace))
	assert.Equal(t, contract.SyntheticServerID, serverID)
	assert.Equal(t, contract.SyntheticServerNamespace, namespace)
	var revision, principals, grants int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT revision FROM authorization_meta WHERE singleton = 1`).Scan(&revision))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*) FROM principals`).Scan(&principals))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*) FROM grants`).Scan(&grants))
	assert.Zero(t, revision)
	assert.Zero(t, principals)
	assert.Zero(t, grants)

	for table, columns := range expectedColumns {
		for _, column := range columns {
			assert.NotContains(t, []string{"bearer", "raw_bearer", "session", "history", "issuer", "reason"}, column, table+"."+column)
		}
	}
}

func assertPopulatedSchemaSevenFacts(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()

	var namespace, displayName, state string
	var desiredRevision int
	require.NoError(t, database.QueryRowContext(ctx, `
		SELECT identity.namespace, server.display_name, server.desired_state, server.desired_revision
		FROM server_identities identity JOIN servers server ON server.id = identity.id
		WHERE identity.id = '01J60000000000000000000001'`).Scan(&namespace, &displayName, &state, &desiredRevision))
	assert.Equal(t, "fixture", namespace)
	assert.Equal(t, "Schema Seven Fixture", displayName)
	assert.Equal(t, "disabled", state)
	assert.Equal(t, 3, desiredRevision)

	var catalogState, upstreamName, externalName, descriptor string
	var durableRevision int
	require.NoError(t, database.QueryRowContext(ctx, `
		SELECT catalog.durable_state, catalog.durable_revision, identity.upstream_name,
		       identity.external_name, descriptor.descriptor_json
		FROM server_catalogs catalog
		JOIN durable_tool_identities identity ON identity.server_id = catalog.server_id
		JOIN tool_descriptors descriptor ON descriptor.tool_id = identity.id
		WHERE catalog.server_id = '01J60000000000000000000001'`).Scan(
		&catalogState, &durableRevision, &upstreamName, &externalName, &descriptor))
	assert.Equal(t, "current", catalogState)
	assert.Equal(t, 2, durableRevision)
	assert.Equal(t, "fixture_tool", upstreamName)
	assert.Equal(t, "fixture.fixture_tool", externalName)
	assert.JSONEq(t, `{"name":"fixture_tool","inputSchema":{"type":"object"}}`, descriptor)
}
