package storage

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitializeSeedsExactSchemaNineInvocationFoundation(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	assertSchemaNineInvocationFoundation(t, ctx, store.database)
	assert.Equal(t, expectedMigrationVersions(), mustMigrationVersions(t, store, ctx))
}

func TestOpenMigratesPopulatedSchemaEightWithoutChangingAuthorityFacts(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	writePopulatedSchemaEightFixture(t, ctx, ownership)

	identity, err := VerifyBackup(ctx, ownership.Layout().Database)
	require.NoError(t, err)
	assert.Equal(t, 8, identity.SchemaVersion)

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	var displayName, fingerprint string
	require.NoError(t, store.database.QueryRowContext(ctx, `
		SELECT display_name, credential_fingerprint FROM principals
		WHERE id = '01J60000000000000000000020'`).Scan(&displayName, &fingerprint))
	assert.Equal(t, "Schema Eight Principal", displayName)
	assert.Equal(t, "0123456789abcdef", fingerprint)
	var grants int
	require.NoError(t, store.database.QueryRowContext(ctx, `SELECT count(*) FROM grants WHERE principal_id = '01J60000000000000000000020'`).Scan(&grants))
	assert.Equal(t, 1, grants)
	assertSchemaNineInvocationFoundation(t, ctx, store.database)
}

func TestSchemaNineChecksAndTerminalTriggerAreExact(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	insertValidInvocation(t, ctx, store.database, "01J60000000000000000000030")
	_, err = store.database.ExecContext(ctx, `UPDATE invocations
		SET completed_at = '2026-08-26T00:00:01Z', terminal_class = 'succeeded'
		WHERE id = '01J60000000000000000000030'`)
	require.NoError(t, err)
	_, err = store.database.ExecContext(ctx, `UPDATE invocations SET completed_at = '2026-08-26T00:00:02Z'
		WHERE id = '01J60000000000000000000030'`)
	require.Error(t, err, "a second terminal update must be rejected")
	_, err = store.database.ExecContext(ctx, `UPDATE invocations SET requested_name = 'changed'
		WHERE id = '01J60000000000000000000030'`)
	require.Error(t, err, "admission evidence must be immutable")
	var firstSequence int64
	require.NoError(t, store.database.QueryRowContext(ctx, `SELECT insertion_sequence FROM invocations WHERE id = '01J60000000000000000000030'`).Scan(&firstSequence))
	_, err = store.database.ExecContext(ctx, `DELETE FROM invocations WHERE id = '01J60000000000000000000030'`)
	require.NoError(t, err, "FIFO deletion remains permitted")
	insertValidInvocation(t, ctx, store.database, "01J60000000000000000000036")
	var nextSequence int64
	require.NoError(t, store.database.QueryRowContext(ctx, `SELECT insertion_sequence FROM invocations WHERE id = '01J60000000000000000000036'`).Scan(&nextSequence))
	assert.Greater(t, nextSequence, firstSequence, "insertion sequence must not reuse an evicted value")

	invalid := []struct {
		name   string
		values string
	}{
		{name: "unknown admission class", values: validInvocationValues("01J60000000000000000000031", "bogus", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL")},
		{name: "partial route", values: validInvocationValues("01J60000000000000000000032", "invalid_arguments", "'tool'", "'{}'", "'01J60000000000000000000040'", "NULL", "NULL", "NULL", "NULL", "NULL", "NULL")},
		{name: "evaluated without decision", values: validInvocationValues("01J60000000000000000000033", "evaluated", "'tool'", "'{}'", "'01J60000000000000000000040'", "'01J60000000000000000000041'", "'upstream'", "1", "'"+strings.Repeat("a", 64)+"'", "NULL", "NULL")},
		{name: "non-evaluated with decision", values: validInvocationValues("01J60000000000000000000034", "unknown_tool", "'tool'", "'{}'", "NULL", "NULL", "NULL", "NULL", "NULL", "'deny'", "NULL")},
		{name: "partial terminal", values: validInvocationValues("01J60000000000000000000035", "evaluated", "'tool'", "'{}'", "'01J60000000000000000000040'", "'01J60000000000000000000041'", "'upstream'", "1", "'"+strings.Repeat("a", 64)+"'", "'allow'", "'2026-08-26T00:00:01Z'")},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, insertErr := store.database.ExecContext(ctx, invocationInsertSQL+test.values)
			require.Error(t, insertErr)
		})
	}
}

func TestOpenRejectsMalformedSchemaNineInvocationStructure(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper string
	}{
		{name: "missing index", tamper: `DROP INDEX invocations_id`},
		{name: "missing trigger", tamper: `DROP TRIGGER invocations_terminal_once`},
		{name: "extra column", tamper: `ALTER TABLE invocations ADD COLUMN raw_error TEXT`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			ownership := newOwnership(t)
			store, err := Initialize(ctx, ownership, testInstallationID)
			require.NoError(t, err)
			require.NoError(t, store.Close())
			raw := openRaw(t, ownership.Layout().Database)
			_, err = raw.Exec(test.tamper)
			require.NoError(t, err)
			require.NoError(t, raw.Close())

			_, err = Open(ctx, ownership)
			require.ErrorIs(t, err, ErrInvalidDatabase)
		})
	}
}

func TestSchemaNineInvocationColumnsExcludeForbiddenPayloads(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	columns := tableColumns(t, ctx, store.database, "invocations")
	for _, forbidden := range []string{"bearer", "credential_verifier", "downstream_request_id", "dispatch_started", "successful_result", "result", "raw_error", "error", "replay", "retry"} {
		assert.NotContains(t, columns, forbidden)
	}
}

func assertSchemaNineInvocationFoundation(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	expected := []string{
		"insertion_sequence", "id", "principal_id", "credential_id", "credential_fingerprint", "credential_revision",
		"admitted_at", "admission_class", "requested_name", "redacted_arguments", "server_id", "tool_id", "upstream_name",
		"descriptor_revision", "descriptor_fingerprint", "decision", "authorization_revision", "evaluated_at", "grant_id",
		"completed_at", "terminal_class",
	}
	assert.Equal(t, expected, tableColumns(t, ctx, database, "invocations"))
	var migrationName string
	require.NoError(t, database.QueryRowContext(ctx, `SELECT name FROM schema_migrations WHERE version = 9`).Scan(&migrationName))
	assert.Equal(t, "invocations", migrationName)
	for kind, name := range map[string]string{"index": "invocations_id", "trigger": "invocations_terminal_once"} {
		var found string
		require.NoError(t, database.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type = ? AND name = ?`, kind, name).Scan(&found))
		assert.Equal(t, name, found)
	}
	var count int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&count))
	assert.Zero(t, count)
	require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_list('invocations')`).Scan(&count))
	assert.Zero(t, count, "audit evidence must not reference mutable authority or catalog rows")
}

func tableColumns(t *testing.T, ctx context.Context, database *sql.DB, table string) []string {
	t.Helper()
	rows, err := database.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var columns []string
	for rows.Next() {
		var column string
		require.NoError(t, rows.Scan(&column))
		columns = append(columns, column)
	}
	require.NoError(t, rows.Err())
	return columns
}

func writePopulatedSchemaEightFixture(t *testing.T, ctx context.Context, ownership *gatewaypaths.Ownership) {
	t.Helper()
	layout := ownership.Layout()
	file, err := gatewaypaths.CreateOwnerOnlyFile(layout.Database)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	store, err := openConfigured(ctx, layout, testOptions{})
	require.NoError(t, err)
	require.NoError(t, store.bootstrap(ctx, testInstallationID))
	require.NoError(t, store.configureSizeLimit(ctx))
	require.NoError(t, store.migrateThrough(ctx, 0, 8))
	_, err = store.database.ExecContext(ctx, `INSERT INTO principals (
		id, display_name, state, visibility, revision, credential_revision, credential_id,
		credential_verifier, credential_fingerprint, credential_created_at, created_at, updated_at
	) VALUES (?, 'Schema Eight Principal', 'active', 'requestable', 1, 1, ?, ?, '0123456789abcdef', ?, ?, ?)`,
		"01J60000000000000000000020", "01J60000000000000000000021", []byte(strings.Repeat("v", 32)),
		"2026-08-25T00:00:00Z", "2026-08-25T00:00:00Z", "2026-08-25T00:00:00Z")
	require.NoError(t, err)
	_, err = store.database.ExecContext(ctx, `INSERT INTO grants (
		id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at
	) VALUES (?, ?, 'allow', '00000000000000000000000000', NULL, NULL, NULL, ?)`,
		"01J60000000000000000000022", "01J60000000000000000000020", "2026-08-25T00:00:00Z")
	require.NoError(t, err)
	require.NoError(t, store.Checkpoint(ctx))
	require.NoError(t, store.Close())
}

const invocationInsertSQL = `INSERT INTO invocations (
	id, principal_id, credential_id, credential_fingerprint, credential_revision, admitted_at,
	admission_class, requested_name, redacted_arguments, server_id, tool_id, upstream_name,
	descriptor_revision, descriptor_fingerprint, decision, authorization_revision, evaluated_at,
	grant_id, completed_at, terminal_class
) VALUES `

func insertValidInvocation(t *testing.T, ctx context.Context, database *sql.DB, id string) {
	t.Helper()
	_, err := database.ExecContext(ctx, invocationInsertSQL+validInvocationValues(
		id, "evaluated", "'tool'", "'{}'", "'01J60000000000000000000040'", "'01J60000000000000000000041'",
		"'upstream'", "1", "'"+strings.Repeat("a", 64)+"'", "'allow'", "NULL"))
	require.NoError(t, err)
}

func validInvocationValues(id, admissionClass, requestedName, redactedArguments, serverID, toolID, upstreamName, descriptorRevision, descriptorFingerprint, decision, completedAt string) string {
	authorization := "NULL, NULL"
	if decision != "NULL" {
		authorization = "1, '2026-08-26T00:00:00Z'"
	}
	terminal := "NULL"
	if completedAt != "NULL" {
		terminal = "NULL"
	}
	return "('" + id + "', '01J60000000000000000000042', '01J60000000000000000000043', '0123456789abcdef', 7, '2026-08-26T00:00:00Z', '" +
		admissionClass + "', " + requestedName + ", " + redactedArguments + ", " + serverID + ", " + toolID + ", " + upstreamName + ", " +
		descriptorRevision + ", " + descriptorFingerprint + ", " + decision + ", " + authorization + ", NULL, " + completedAt + ", " + terminal + ")"
}
