//go:build integration

package servers

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerSchemaAndBackupContainNoSecretOrTransientRepresentation(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	server := mustCreateServer(t, repository, "artifact-scan", false)
	operation, err := repository.CreateOperation(context.Background(), OperationRequest{ServerID: server.ID, Kind: "reload"})
	require.NoError(t, err)
	_, err = repository.TransitionOperation(context.Background(), operation.Operation.ID, "cancelled", nil)
	require.NoError(t, err)

	forbiddenColumns := map[string]struct{}{
		"secret": {}, "client_secret": {}, "access_token": {}, "refresh_token": {},
		"oauth_state": {}, "verifier": {}, "authorization_url": {}, "code": {},
		"runtime_id": {}, "pid": {}, "session_id": {}, "route_capability": {},
		"request_id": {}, "exchange_work": {}, "attempt": {},
	}
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		rows, err := transaction.Query(`
			SELECT name FROM sqlite_schema
			WHERE type = 'table' AND (
				name LIKE 'server_%' OR name = 'servers' OR name = 's2_idempotency' OR
				name IN ('durable_tool_identities', 'tool_descriptors')
			)
			ORDER BY name`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		tables := make([]string, 0)
		for rows.Next() {
			var table string
			if err := rows.Scan(&table); err != nil {
				return err
			}
			tables = append(tables, table)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, table := range tables {
			columns, err := transaction.Query(`SELECT name FROM pragma_table_info(?)`, table)
			if err != nil {
				return err
			}
			for columns.Next() {
				var name string
				if err := columns.Scan(&name); err != nil {
					_ = columns.Close()
					return err
				}
				_, forbidden := forbiddenColumns[name]
				assert.False(t, forbidden, "%s.%s must not be durable", table, name)
			}
			if err := columns.Close(); err != nil {
				return err
			}
		}
		return nil
	}))

	backup := filepath.Join(t.TempDir(), "gateway-backup.db")
	require.NoError(t, store.BackupTo(context.Background(), backup))
	artifact, err := os.ReadFile(backup)
	require.NoError(t, err)
	for _, canary := range []string{
		"STATIC-SECRET-CANARY", "OAUTH-CLIENT-SECRET-CANARY", "ACCESS-TOKEN-CANARY",
		"REFRESH-TOKEN-CANARY", "OAUTH-STATE-CANARY", "PKCE-VERIFIER-CANARY",
		"AUTHORIZATION-CODE-CANARY", "AUTHORIZATION-URL-CANARY", "RUNTIME-ID-CANARY",
	} {
		assert.False(t, bytes.Contains(artifact, []byte(canary)), canary)
	}
}

func TestServerSnapshotWatermarkExcludesLaterInsert(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	mustCreateServer(t, repository, "snapshot-a", false)
	mustCreateServer(t, repository, "snapshot-b", false)

	first, err := repository.ListServers(context.Background(), nil, 1)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.NotNil(t, first.Next)
	mustCreateServer(t, repository, "snapshot-later", false)

	second, err := repository.ListServers(context.Background(), first.Next, 10)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	assert.False(t, strings.Contains(second.Items[0].Namespace, "later"))
	assert.Nil(t, second.Next)
}
