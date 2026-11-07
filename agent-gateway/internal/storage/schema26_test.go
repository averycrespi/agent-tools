package storage

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResourceKeyMigrationPreservesGraphAndDeletedReservations(t *testing.T) {
	owner := newOwnership(t)
	ctx := t.Context()
	writePopulatedSchemaEightFixture(t, ctx, owner)
	prior, err := openConfigured(ctx, owner.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, prior.migrateThrough(ctx, 8, 25))
	const id = "01J60000000000000000000080"
	const stamp = "2026-08-25T00:00:00Z"
	for _, query := range []string{
		`INSERT INTO http_credentials(id,name,host,port,allow_wildcard,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,'Same','example.com',443,0,'Authorization','Bearer ',1,0,1,?,?)`,
		`INSERT INTO git_credentials(id,name,origin,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,'Same','https://example.com:443','Authorization','Bearer ',1,0,1,?,?)`,
		`INSERT INTO git_repositories(id,name,url,aliases_json,credential_id,revision,alias_revision,deleted,created_at,updated_at) VALUES(?,'Same','https://example.com:443/team/repo','[]','01J60000000000000000000080',1,1,1,?,?)`,
		`INSERT INTO http_grants(id,principal_id,revision,policy_json,created_at,updated_at) SELECT ?,id,1,'{"version":1,"type":"block_destination","destination":{"host":"example.com","port":443}}',?,? FROM principals LIMIT 1`,
		`INSERT INTO git_grants(id,principal_id,repository_id,revision,policy_json,created_at,updated_at) SELECT ?,id,'01J60000000000000000000080',1,'{"version":1,"read":true,"refs":[]}',?,? FROM principals LIMIT 1`,
	} {
		_, err := prior.database.ExecContext(ctx, query, id, stamp, stamp)
		require.NoError(t, err)
	}
	tables := []string{"principals", "grants", "http_defaults", "http_grants", "git_grants", "http_credentials", "git_credentials", "git_repositories", "server_identities", "servers", "authorization_meta", "secret_generations"}
	before := map[string][][]any{}
	for _, table := range tables {
		before[table] = snapshotRows(t, prior.database, table)
	}
	require.NoError(t, prior.Checkpoint(ctx))
	require.NoError(t, prior.Close())
	current, err := Open(ctx, owner)
	require.NoError(t, err)
	for _, table := range tables {
		require.Equal(t, before[table], snapshotRows(t, current.database, table), table)
	}
	for kind, table := range map[string]string{"agent": "principals", "mcp_grant": "grants", "http_grant": "http_grants", "git_grant": "git_grants", "http_credential": "http_credentials", "git_credential": "git_credentials", "git_repository": "git_repositories"} {
		var missing int
		err := current.database.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` r LEFT JOIN resource_keys k ON k.kind=? AND k.resource_id=r.id WHERE k.key IS NULL OR k.key<>?||lower(r.id)`, kind, strings.ReplaceAll(kind, "_", "-")+"-").Scan(&missing)
		require.NoError(t, err)
		require.Zero(t, missing, kind)
	}
	keys := snapshotRows(t, current.database, "resource_keys")
	require.NoError(t, current.Checkpoint(ctx))
	require.NoError(t, current.Close())
	current, err = Open(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, keys, snapshotRows(t, current.database, "resource_keys"))
	require.NoError(t, current.verifyMigrationStructure(ctx, "026_resource_keys.sql"))
	_, err = current.database.ExecContext(ctx, `DELETE FROM http_grants`)
	require.NoError(t, err)
	require.Equal(t, keys, snapshotRows(t, current.database, "resource_keys"), "physical grant deletion retains reservations")
	require.NoError(t, current.Close())
}

func TestResourceKeyStartupRejectsMissingCoverageAndChangedEnforcement(t *testing.T) {
	for _, change := range []string{`DROP TRIGGER resource_keys_immutable_delete`, `INSERT INTO principals(id,display_name,state,visibility,revision,credential_revision,created_at,updated_at) VALUES('01J60000000000000000000080','Agent','active','all',1,0,'2026-08-25T00:00:00Z','2026-08-25T00:00:00Z')`} {
		t.Run(change[:12], func(t *testing.T) {
			owner := newOwnership(t)
			store, err := Initialize(t.Context(), owner, testInstallationID)
			require.NoError(t, err)
			_, err = store.database.ExecContext(t.Context(), change)
			require.NoError(t, err)
			require.NoError(t, store.Close())
			_, err = Open(t.Context(), owner)
			require.ErrorIs(t, err, ErrInvalidDatabase)
		})
	}
}

func snapshotRows(t *testing.T, database *sql.DB, table string) [][]any {
	t.Helper()
	rows, err := database.Query("SELECT * FROM " + table + " ORDER BY 1,2")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	columns, err := rows.Columns()
	require.NoError(t, err)
	out := [][]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		require.NoError(t, rows.Scan(destinations...))
		for i, v := range values {
			if bytes, ok := v.([]byte); ok {
				values[i] = append([]byte(nil), bytes...)
			}
		}
		out = append(out, values)
	}
	require.NoError(t, rows.Err())
	return out
}
