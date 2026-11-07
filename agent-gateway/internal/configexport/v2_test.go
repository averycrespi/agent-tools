package configexport

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/configdefinition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func seedV2(t *testing.T, store *storage.Store) {
	t.Helper()
	seed(t, store)
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		kinds := []struct {
			kind  resourcekeys.Kind
			table string
		}{{resourcekeys.Agent, "principals"}, {resourcekeys.MCPGrant, "grants"}, {resourcekeys.HTTPGrant, "http_grants"}, {resourcekeys.GitGrant, "git_grants"}, {resourcekeys.HTTPCredential, "http_credentials"}, {resourcekeys.GitCredential, "git_credentials"}, {resourcekeys.GitRepository, "git_repositories"}}
		for _, k := range kinds {
			rows, err := tx.QueryContext(t.Context(), "SELECT id FROM "+k.table+" ORDER BY id")
			require.NoError(t, err)
			ids := []string{}
			for rows.Next() {
				var value string
				require.NoError(t, rows.Scan(&value))
				ids = append(ids, value)
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			for i, value := range ids {
				_, err := resourcekeys.ReserveTx(t.Context(), tx, k.kind, value, fmt.Sprintf("%s-key-%d", strings.ReplaceAll(string(k.kind), "_", "-"), i))
				require.NoError(t, err)
			}
		}
		// Preserve a physically removed grant reservation and a deleted credential's
		// authored definition independently of its lost material and history.
		_, err := resourcekeys.ReserveTx(t.Context(), tx, resourcekeys.MCPGrant, id(90), "removed-grant")
		require.NoError(t, err)
		exec(t, tx, `INSERT INTO http_credentials(id,name,host,port,allow_wildcard,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,'Deleted','old.example.com',443,0,'Authorization','Bearer ',9,7,1,?,?)`, id(91), stamp, stamp)
		_, err = resourcekeys.ReserveTx(t.Context(), tx, resourcekeys.HTTPCredential, id(91), "deleted-credential")
		require.NoError(t, err)
		exec(t, tx, `INSERT INTO grants(id,principal_id,effect,server_id,upstream_name,constraint_json,expires_at,created_at,description,revision,read_only) SELECT ?,principal_id,effect,server_id,upstream_name,constraint_json,expires_at,created_at,description,revision,read_only FROM grants WHERE id=?`, id(92), id(30))
		_, err = resourcekeys.ReserveTx(t.Context(), tx, resourcekeys.MCPGrant, id(92), "duplicate-grant")
		require.NoError(t, err)
		exec(t, tx, `INSERT INTO server_identities(id,namespace,created_at) VALUES(?,'reserved_namespace',?)`, id(93), stamp)
		return nil
	}))
}

func TestV2BundleAndEverySingleDefinitionRetainTypedKeysAndState(t *testing.T) {
	store := fixture(t)
	seedV2(t, store)
	service := New(store)
	raw, err := service.ExportBundle(t.Context())
	require.NoError(t, err)
	bundle, err := configdefinition.DecodeBundle(raw)
	require.NoError(t, err)
	require.Len(t, bundle.Resources, 19)
	byID := map[string]contract.ConfigDefinition{}
	for _, d := range bundle.Resources {
		byID[d.Kind+"/"+d.Key] = d
		single, err := service.ExportResource(t.Context(), d.Kind, d.Key)
		require.NoError(t, err)
		decoded, err := configdefinition.DecodeDefinition(single)
		require.NoError(t, err)
		equal, err := configdefinition.Equivalent(d, decoded)
		require.NoError(t, err)
		require.True(t, equal)
		require.NotContains(t, string(single), `"resources"`)
		if d.Kind != "git_routing" {
			missingKey := d
			missingKey.Key = ""
			missing, err := json.Marshal(missingKey)
			require.NoError(t, err)
			_, err = configdefinition.DecodeDefinition(missing)
			require.ErrorIs(t, err, configdefinition.ErrInvalid, d.Kind)
		}
	}
	grant := byID["mcp_grant/mcp-grant-key-0"]
	require.Contains(t, string(grant.Spec), `"agent":{"kind":"agent","key":"agent-key-0"}`)
	require.Contains(t, string(grant.Spec), `"server":{"kind":"mcp_server","key":"server_0"}`)
	require.Contains(t, string(grant.Spec), `1.2300e+4`)
	require.Contains(t, string(grant.Spec), stamp)
	require.Contains(t, string(byID["mcp_grant/mcp-grant-key-2"].Spec), `"server":{"kind":"gateway_mcp"}`)
	require.Contains(t, string(byID["mcp_server/server_3"].Spec), `"deleted":true`)
	require.Contains(t, string(byID["git_repository/git-repository-key-0"].Spec), `"deleted":true`)
	require.Contains(t, string(byID["http_credential/deleted-credential"].Spec), `"secret_ref":null`)
	require.True(t, byID["mcp_server/reserved_namespace"].Reserved)
	require.True(t, byID["mcp_grant/removed-grant"].Reserved)
	require.JSONEq(t, "null", string(byID["mcp_grant/removed-grant"].Spec))
	require.NotEmpty(t, byID["mcp_grant/duplicate-grant"].Spec)
	for _, forbidden := range []string{installation, "SECRET_MATERIAL_CANARY", "secret-handle-canary", "revision", "created_at", "fingerprint", "runtime", "ciphertext", "operations"} {
		require.NotContains(t, string(raw), forbidden)
	}
	for i := 1; i < 100; i++ {
		require.NotContains(t, string(raw), id(i))
	}
	repeated, err := service.ExportBundle(t.Context())
	require.NoError(t, err)
	require.Equal(t, raw, repeated)
	// Deliberately no translator or unsupported init round-trip: v1 init and its
	// matching route remain paired only until the next backend ticket's cutover.
	old, err := service.ExportConfiguration(t.Context())
	require.NoError(t, err)
	_, err = configdefinition.DecodeBundle(old)
	require.ErrorIs(t, err, configdefinition.ErrInvalid)
	require.Contains(t, string(old), `"version":1`)
}

func TestV2ReferencesDoNotMoveAfterUnrelatedInventoryInsertion(t *testing.T) {
	store := fixture(t)
	seedV2(t, store)
	service := New(store)
	before, err := service.ExportResource(t.Context(), "mcp_grant", "mcp-grant-key-0")
	require.NoError(t, err)
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		exec(t, tx, `INSERT INTO principals(id,display_name,state,visibility,revision,credential_revision,created_at,updated_at) VALUES(?,'Earlier','active','all',1,0,?,?)`, id(0), stamp, stamp)
		_, err := resourcekeys.ReserveTx(t.Context(), tx, resourcekeys.Agent, id(0), "earlier-agent")
		return err
	}))
	after, err := service.ExportResource(t.Context(), "mcp_grant", "mcp-grant-key-0")
	require.NoError(t, err)
	require.Equal(t, before, after)
	body, err := service.ExportResource(t.Context(), "agent", "missing")
	require.ErrorIs(t, err, ErrConfigurationNotFound)
	require.Empty(t, body)
	body, err = service.ExportResource(t.Context(), "mcp_server", contract.SyntheticServerNamespace)
	require.ErrorIs(t, err, ErrConfigurationNotFound)
	require.Empty(t, body)
}

func TestV2ConfigurationUsesOneSnapshotAndRejectsBrokenBindings(t *testing.T) {
	store := fixture(t)
	seedV2(t, store)
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
		var old string
		require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT display_name FROM principals WHERE id=?`, id(1)).Scan(&old))
		require.NoError(t, store.Mutate(t.Context(), func(writer *sql.Tx) error {
			exec(t, writer, `UPDATE principals SET display_name='new' WHERE id=?`, id(1))
			exec(t, writer, `UPDATE servers SET display_name='new' WHERE id=?`, id(10))
			return nil
		}))
		bundle, err := configurationV2Tx(t.Context(), tx)
		require.NoError(t, err)
		for _, d := range bundle.Resources {
			if d.Kind == "agent" && d.Key == "agent-key-0" {
				var s contract.ConfigAgentSpec
				require.NoError(t, json.Unmarshal(d.Spec, &s))
				require.Equal(t, old, s.DisplayName)
			}
			if d.Kind == "mcp_server" && d.Key == "server_0" {
				require.Contains(t, string(d.Spec), `"display_name":"Server"`)
			}
		}
		return nil
	}))
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		exec(t, tx, `UPDATE http_grants SET policy_json=json_set(policy_json,'$.credential_id',?)`, id(99))
		return nil
	}))
	raw, err := New(store).ExportBundle(t.Context())
	require.Error(t, err)
	require.Empty(t, raw)
	// A single export neither includes nor traverses unrelated HTTP policies.
	raw, err = New(store).ExportResource(t.Context(), "agent", "agent-key-0")
	require.NoError(t, err)
	_, err = configdefinition.DecodeDefinition(raw)
	require.NoError(t, err)
}
