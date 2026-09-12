package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuthDiagnosticSchemaIsBoundedAndCoherent(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	assert.Equal(t, []string{
		"insertion_sequence", "id", "server_id", "flow_state", "target_desired_revision",
		"registration_revision", "created_at", "expires_at", "finished_at", "reason",
		"diagnostic_stage", "diagnostic_http_status", "audit_cause",
	}, tableColumns(t, ctx, store.database, "server_auth_flows"))
	var migrationName string
	require.NoError(t, store.database.QueryRowContext(ctx, `SELECT name FROM schema_migrations WHERE version = 11`).Scan(&migrationName))
	assert.Equal(t, "oauth_diagnostics", migrationName)

	_, err = store.database.ExecContext(ctx, `
		INSERT INTO server_identities (id, namespace, created_at)
		VALUES ('01J60000000000000000000110', 'diagnostic', '2026-08-31T00:00:00Z')`)
	require.NoError(t, err)
	_, err = store.database.ExecContext(ctx, `
		INSERT INTO servers (id, display_name, desired_state, desired_revision, transport_json, created_at, updated_at)
		VALUES ('01J60000000000000000000110', 'Diagnostic', 'disabled', 1,
		'{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"auto","authentication":{"mode":"oauth","registration":{"mode":"dynamic","issuer":null},"trusted_origins":[],"request_offline_access":false}}',
		'2026-08-31T00:00:00Z', '2026-08-31T00:00:00Z')`)
	require.NoError(t, err)
	_, err = store.database.ExecContext(ctx, `
		INSERT INTO server_auth_flows (
			id, server_id, flow_state, target_desired_revision, registration_revision,
			created_at, expires_at, finished_at, reason, diagnostic_stage, diagnostic_http_status
		) VALUES (
			'01J60000000000000000000111', '01J60000000000000000000110', 'failed', 1, 0,
			'2026-08-31T00:00:00Z', '2026-08-31T00:05:00Z', '2026-08-31T00:00:01Z',
			'protocol_invalid', 'client_registration', 400
		)`)
	require.NoError(t, err)

	_, err = store.database.ExecContext(ctx, `UPDATE server_auth_flows SET diagnostic_http_status = 99 WHERE id = '01J60000000000000000000111'`)
	require.Error(t, err)
	_, err = store.database.ExecContext(ctx, `UPDATE server_auth_flows SET flow_state = 'succeeded', reason = NULL WHERE id = '01J60000000000000000000111'`)
	require.Error(t, err)
	_, err = store.database.ExecContext(ctx, `UPDATE server_auth_flows SET diagnostic_stage = NULL WHERE id = '01J60000000000000000000111'`)
	require.Error(t, err)
}
