package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestSchemaInitializesExactFoundation(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	assert.Equal(t, 12, CurrentSchema)
	assertSchemaTenRequestFoundation(t, ctx, store.database)
	assert.Equal(t, expectedMigrationVersions(), mustMigrationVersions(t, store, ctx))
}

func TestRequestSchemaMigratesPopulatedPredecessorAndReplacement(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	writePopulatedSchemaNineFixture(t, ctx, ownership)

	identity, err := VerifyBackup(ctx, ownership.Layout().Database)
	require.NoError(t, err)
	assert.Equal(t, 9, identity.SchemaVersion)

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	assertPopulatedSchemaNineFacts(t, ctx, store.database)
	assertSchemaTenRequestFoundation(t, ctx, store.database)
	require.NoError(t, store.Checkpoint(ctx))
	require.NoError(t, store.Close())

	staged := filepath.Join(ownership.Layout().Root, "schema-nine-replacement.db")
	writePopulatedSchemaNineFixtureAt(t, ctx, ownership.Layout(), staged)
	identity, err = VerifyBackup(ctx, staged)
	require.NoError(t, err)
	assert.Equal(t, 9, identity.SchemaVersion)
	replacement, err := OpenReplacement(ctx, ownership, staged)
	require.NoError(t, err)
	defer func() { require.NoError(t, replacement.Close()) }()
	assertPopulatedSchemaNineFacts(t, ctx, replacement.database)
	assertSchemaTenRequestFoundation(t, ctx, replacement.database)
}

func TestRequestSchemaEnforcesTransitionsUniquenessAndEvidenceAccounting(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	insertPendingServerRequest(t, ctx, store.database, "01J60000000000000000000050", []byte("dedupe-a"))
	_, err = store.database.ExecContext(ctx, pendingServerRequestSQL,
		"01J60000000000000000000051", []byte("dedupe-a"))
	require.Error(t, err, "identical pending requests must be unique")

	_, err = store.database.ExecContext(ctx, `UPDATE grant_requests SET
		state = 'approved', revision = 2,
		approved_scope = 'server', approved_target = requested_target,
		approved_constraint = NULL, approved_duration_seconds = NULL,
		approved_future_tools_acknowledged = 1,
		approved_grant_id = '01J60000000000000000000060',
		updated_at = '2026-08-27T00:00:01Z', closed_at = '2026-08-27T00:00:01Z'
		WHERE id = '01J60000000000000000000050'`)
	require.NoError(t, err)
	_, err = store.database.ExecContext(ctx, `UPDATE grant_requests SET requested_target = 'changed' WHERE id = '01J60000000000000000000050'`)
	require.Error(t, err, "terminal requests must be immutable")
	_, err = store.database.ExecContext(ctx, `UPDATE grant_requests SET state = 'pending', revision = 3, closed_at = NULL WHERE id = '01J60000000000000000000050'`)
	require.Error(t, err, "terminal requests must not reopen")

	insertPendingServerRequest(t, ctx, store.database, "01J60000000000000000000051", []byte("dedupe-a"))
	_, err = store.database.ExecContext(ctx, `DELETE FROM grant_requests WHERE id = '01J60000000000000000000051'`)
	require.Error(t, err, "pending requests must never be evicted")

	evidence := []byte(`{"descriptor":{"name":"tool"}}`)
	insertPendingToolRequest(t, ctx, store.database, "01J60000000000000000000052", []byte("dedupe-b"), evidence)
	assert.Equal(t, int64(len(evidence)), requestEvidenceBytes(t, ctx, store.database))
	_, err = store.database.ExecContext(ctx, `UPDATE grant_requests SET
		state = 'cancelled', revision = 2,
		updated_at = '2026-08-27T00:00:01Z', closed_at = '2026-08-27T00:00:01Z'
		WHERE id = '01J60000000000000000000052'`)
	require.NoError(t, err)
	_, err = store.database.ExecContext(ctx, `DELETE FROM grant_requests WHERE id = '01J60000000000000000000052'`)
	require.NoError(t, err)
	assert.Zero(t, requestEvidenceBytes(t, ctx, store.database))

	var firstSequence int64
	require.NoError(t, store.database.QueryRowContext(ctx, `SELECT insertion_sequence FROM grant_requests WHERE id = '01J60000000000000000000050'`).Scan(&firstSequence))
	_, err = store.database.ExecContext(ctx, `DELETE FROM grant_requests WHERE id = '01J60000000000000000000050'`)
	require.NoError(t, err)
	insertPendingServerRequest(t, ctx, store.database, "01J60000000000000000000053", []byte("dedupe-c"))
	var nextSequence int64
	require.NoError(t, store.database.QueryRowContext(ctx, `SELECT insertion_sequence FROM grant_requests WHERE id = '01J60000000000000000000053'`).Scan(&nextSequence))
	assert.Greater(t, nextSequence, firstSequence, "request insertion sequence must never reuse an evicted value")
}

func TestRequestSchemaRejectsMalformedRowsAndStructure(t *testing.T) {
	t.Run("malformed rows", func(t *testing.T) {
		ctx := context.Background()
		ownership := newOwnership(t)
		store, err := Initialize(ctx, ownership, testInstallationID)
		require.NoError(t, err)
		defer func() { require.NoError(t, store.Close()) }()

		insertPendingServerRequest(t, ctx, store.database, "01J60000000000000000000070", []byte("dedupe-c"))
		_, err = store.database.ExecContext(ctx, pendingToolRequestSQL,
			"01J60000000000000000000072", []byte("dedupe-oversize"), make([]byte, 135169))
		require.Error(t, err, "one evidence envelope must not exceed 135,168 bytes")
		for _, statement := range []string{
			`UPDATE grant_requests SET state = 'rejected', revision = 2, rejection_reason = 'other', updated_at = '2026-08-27T00:00:01Z', closed_at = '2026-08-27T00:00:01Z' WHERE id = '01J60000000000000000000070'`,
			`UPDATE grant_requests SET state = 'cancelled', revision = 4, updated_at = '2026-08-27T00:00:01Z', closed_at = '2026-08-27T00:00:01Z' WHERE id = '01J60000000000000000000070'`,
			`UPDATE grant_requests SET state = 'approved', revision = 2, approved_scope = 'tool', approved_target = 'ns__tool', approved_future_tools_acknowledged = 0, approved_grant_id = '01J60000000000000000000071', updated_at = '2026-08-27T00:00:01Z', closed_at = '2026-08-27T00:00:01Z' WHERE id = '01J60000000000000000000070'`,
		} {
			_, err = store.database.ExecContext(ctx, statement)
			require.Error(t, err)
		}
	})

	for _, test := range []struct {
		name   string
		tamper string
	}{
		{name: "missing dedupe index", tamper: `DROP INDEX grant_requests_pending_dedupe`},
		{name: "missing transition trigger", tamper: `DROP TRIGGER grant_requests_terminal_once`},
		{name: "missing evidence trigger", tamper: `DROP TRIGGER grant_requests_evidence_insert`},
		{name: "drifted evidence aggregate", tamper: `UPDATE grant_request_evidence_bytes SET total_bytes = 1`},
		{name: "forbidden extra column", tamper: `ALTER TABLE grant_requests ADD COLUMN raw_error TEXT`},
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

func TestRequestSchemaMigrationRollsBackAtomically(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	writePopulatedSchemaNineFixture(t, ctx, ownership)
	raw := openRaw(t, ownership.Layout().Database)
	_, err := raw.Exec(`CREATE TABLE grant_request_evidence_bytes (singleton INTEGER PRIMARY KEY) STRICT`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	_, err = Open(ctx, ownership)
	require.ErrorIs(t, err, ErrInvalidDatabase)
	raw = openRaw(t, ownership.Layout().Database)
	defer func() { require.NoError(t, raw.Close()) }()
	var version, requests, migration int
	require.NoError(t, raw.QueryRow(`PRAGMA user_version`).Scan(&version))
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'grant_requests'`).Scan(&requests))
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = 10`).Scan(&migration))
	assert.Equal(t, 9, version)
	assert.Zero(t, requests)
	assert.Zero(t, migration)
}

func TestRequestSchemaColumnsExcludeForbiddenPayloads(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	columns := tableColumns(t, ctx, store.database, "grant_requests")
	for _, forbidden := range []string{
		"credential", "bearer", "verifier", "fingerprint", "invocation", "arguments", "justification",
		"note", "reviewer", "raw_error", "result", "replay", "retry", "deny", "conflict_detail",
	} {
		for _, column := range columns {
			assert.NotContains(t, column, forbidden)
		}
	}
}

func assertSchemaTenRequestFoundation(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	expected := []string{
		"insertion_sequence", "id", "principal_id", "state", "revision", "resolved_server_id", "resolved_upstream_name",
		"requested_scope", "requested_target", "requested_constraint", "requested_duration_seconds", "requested_future_tools_acknowledged",
		"dedupe_version", "dedupe_bytes", "submitted_evidence", "approved_scope", "approved_target", "approved_constraint",
		"approved_duration_seconds", "approved_future_tools_acknowledged", "approved_grant_id", "rejection_reason", "approved_evidence",
		"created_at", "updated_at", "closed_at",
	}
	assert.Equal(t, []string{"id", "created_at"}, tableColumns(t, ctx, database, "grant_request_identities"))
	assert.Equal(t, expected, tableColumns(t, ctx, database, "grant_requests"))
	assert.Equal(t, []string{"singleton", "total_bytes"}, tableColumns(t, ctx, database, "grant_request_evidence_bytes"))
	var migrationName string
	require.NoError(t, database.QueryRowContext(ctx, `SELECT name FROM schema_migrations WHERE version = 10`).Scan(&migrationName))
	assert.Equal(t, "grant_requests", migrationName)
	for kind, names := range map[string][]string{
		"index":   {"grant_requests_id", "grant_requests_pending_dedupe", "grant_requests_principal_page", "grant_requests_admin_page", "grant_requests_pending_principal"},
		"trigger": {"grant_requests_terminal_once", "grant_requests_pending_not_deleted", "grant_requests_evidence_insert", "grant_requests_evidence_update", "grant_requests_evidence_delete"},
	} {
		for _, name := range names {
			var found string
			require.NoError(t, database.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type = ? AND name = ?`, kind, name).Scan(&found))
			assert.Equal(t, name, found)
		}
	}
	var identities, requests, total, foreignKeys int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*) FROM grant_request_identities`).Scan(&identities))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*) FROM grant_requests`).Scan(&requests))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT total_bytes FROM grant_request_evidence_bytes WHERE singleton = 1`).Scan(&total))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_list('grant_requests')`).Scan(&foreignKeys))
	assert.Zero(t, identities)
	assert.Zero(t, requests)
	assert.Zero(t, total)
	assert.Zero(t, foreignKeys, "historical request facts must not reference mutable authority, targets, or grants")
}

func writePopulatedSchemaNineFixture(t *testing.T, ctx context.Context, ownership *gatewaypaths.Ownership) {
	t.Helper()
	writePopulatedSchemaNineFixtureAt(t, ctx, ownership.Layout(), ownership.Layout().Database)
}

func writePopulatedSchemaNineFixtureAt(t *testing.T, ctx context.Context, layout gatewaypaths.Layout, path string) {
	t.Helper()
	file, err := gatewaypaths.CreateOwnerOnlyFile(path)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	layout.Database = path
	layout.MutationMarker = path + ".mutation"
	store, err := openConfigured(ctx, layout, testOptions{})
	require.NoError(t, err)
	require.NoError(t, store.bootstrap(ctx, testInstallationID))
	require.NoError(t, store.configureSizeLimit(ctx))
	require.NoError(t, store.migrateThrough(ctx, 0, 9))
	_, err = store.database.ExecContext(ctx, `INSERT INTO principals (
		id, display_name, state, visibility, revision, credential_revision, credential_id,
		credential_verifier, credential_fingerprint, credential_created_at, created_at, updated_at
	) VALUES (?, 'Schema Nine Principal', 'active', 'requestable', 1, 1, ?, ?, '0123456789abcdef', ?, ?, ?)`,
		"01J60000000000000000000020", "01J60000000000000000000021", []byte(strings.Repeat("v", 32)),
		"2026-08-26T00:00:00Z", "2026-08-26T00:00:00Z", "2026-08-26T00:00:00Z")
	require.NoError(t, err)
	insertValidInvocation(t, ctx, store.database, "01J60000000000000000000030")
	require.NoError(t, store.Checkpoint(ctx))
	require.NoError(t, store.Close())
}

func assertPopulatedSchemaNineFacts(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var principals, invocations int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*) FROM principals WHERE id = '01J60000000000000000000020'`).Scan(&principals))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*) FROM invocations WHERE id = '01J60000000000000000000030'`).Scan(&invocations))
	assert.Equal(t, 1, principals)
	assert.Equal(t, 1, invocations)
}

const pendingServerRequestSQL = `INSERT INTO grant_requests (
	id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
	requested_scope, requested_target, requested_constraint, requested_duration_seconds,
	requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
	approved_scope, approved_target, approved_constraint, approved_duration_seconds,
	approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
	created_at, updated_at, closed_at
) VALUES (?, '01J60000000000000000000020', 'pending', 1, '01J60000000000000000000040', NULL,
	'server', 'namespace', NULL, NULL, 1, 1, ?, NULL,
	NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
	'2026-08-27T00:00:00Z', '2026-08-27T00:00:00Z', NULL)`

func insertPendingServerRequest(t *testing.T, ctx context.Context, database *sql.DB, id string, dedupe []byte) {
	t.Helper()
	_, err := database.ExecContext(ctx, pendingServerRequestSQL, id, dedupe)
	require.NoError(t, err)
}

const pendingToolRequestSQL = `INSERT INTO grant_requests (
	id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
	requested_scope, requested_target, requested_constraint, requested_duration_seconds,
	requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
	approved_scope, approved_target, approved_constraint, approved_duration_seconds,
	approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
	created_at, updated_at, closed_at
) VALUES (?, '01J60000000000000000000020', 'pending', 1, '01J60000000000000000000040', 'tool',
	'tool', 'namespace__tool', NULL, '60', 0, 1, ?, ?,
	NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
	'2026-08-27T00:00:00Z', '2026-08-27T00:00:00Z', NULL)`

func insertPendingToolRequest(t *testing.T, ctx context.Context, database *sql.DB, id string, dedupe, evidence []byte) {
	t.Helper()
	_, err := database.ExecContext(ctx, pendingToolRequestSQL, id, dedupe, evidence)
	require.NoError(t, err)
}

func requestEvidenceBytes(t *testing.T, ctx context.Context, database *sql.DB) int64 {
	t.Helper()
	var total int64
	require.NoError(t, database.QueryRowContext(ctx, `SELECT total_bytes FROM grant_request_evidence_bytes WHERE singleton = 1`).Scan(&total))
	return total
}
