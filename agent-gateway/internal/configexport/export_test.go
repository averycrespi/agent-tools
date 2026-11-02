package configexport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

const installation = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
const stamp = "2026-10-01T00:00:00.000000000Z"

var template = storagefixture.New(installation)

func id(n int) string { return fmt.Sprintf("%026d", n) }

func fixture(t *testing.T) *storage.Store {
	t.Helper()
	owner, err := paths.Acquire(testutil.NewOwnerOnlyDataRoot(t))
	require.NoError(t, err)
	store, err := template.Open(t.Context(), owner)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()); require.NoError(t, owner.Close()) })
	return store
}
func exec(t *testing.T, tx *sql.Tx, query string, args ...any) {
	t.Helper()
	_, err := tx.ExecContext(t.Context(), query, args...)
	require.NoError(t, err)
}

func seed(t *testing.T, store *storage.Store) {
	t.Helper()
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		for i := 1; i <= 2; i++ {
			exec(t, tx, `INSERT INTO principals(id,display_name,state,visibility,revision,credential_revision,created_at,updated_at) VALUES(?,?,'active','allowed-only',1,0,?,?)`, id(i), "Same name", stamp, stamp)
		}
		exec(t, tx, `UPDATE http_defaults SET policy='allow' WHERE principal_id=?`, id(2))
		transports := []string{
			`{"kind":"stdio","executable":"/bin/example","arguments":["--authored-string=not-scanned"],"working_directory":"/tmp","environment":{"LANG":"C"},"secret_environment":{"API_TOKEN":"api","OTHER_TOKEN":"api"}}`,
			`{"kind":"streamable_http","url":"https://unhealthy.example/mcp","protocol_mode":"auto","authentication":{"mode":"bearer"},"headers":{"X-Project":"authored"}}`,
			`{"kind":"streamable_http","url":"https://oauth.example/mcp","protocol_mode":"modern","authentication":{"mode":"oauth","registration":{"mode":"static","issuer":"https://issuer.example","client_id":"configured-client","token_endpoint_auth_method":"client_secret_basic"},"trusted_origins":[],"request_offline_access":true,"scopes":[]}}`,
		}
		for i := 0; i < 4; i++ {
			exec(t, tx, `INSERT INTO server_identities(id,namespace,created_at) VALUES(?,?,?)`, id(10+i), fmt.Sprintf("server_%d", i), stamp)
			state := "enabled"
			var transport any
			if i < 3 {
				transport = transports[i]
			} else {
				state = "deleted"
			}
			var deleted any
			if i == 3 {
				deleted = stamp
			}
			exec(t, tx, `INSERT INTO servers(id,display_name,desired_state,desired_revision,transport_json,created_at,updated_at,deleted_at) VALUES(?,?,?,1,?,?,?,?)`, id(10+i), "Server", state, transport, stamp, stamp, deleted)
		}
		exec(t, tx, `INSERT INTO http_credentials(id,name,host,port,allow_wildcard,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,'API','*.example.com',443,1,'Authorization','Bearer ',1,0,0,?,?)`, id(20), stamp, stamp)
		exec(t, tx, `INSERT INTO git_credentials(id,name,origin,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,'Git','https://github.com:443','Authorization','Bearer ',1,0,0,?,?)`, id(21), stamp, stamp)
		exec(t, tx, `INSERT INTO git_repositories(id,name,url,aliases_json,credential_id,revision,alias_revision,deleted,created_at,updated_at) VALUES(?,'Repo','https://github.com:443/org/repo.git','["https://github.com:443/org/repo"]',?,1,1,1,?,?)`, id(22), id(21), stamp, stamp)
		exec(t, tx, `INSERT INTO grants(id,principal_id,effect,server_id,upstream_name,constraint_json,expires_at,created_at,description,revision,read_only) VALUES(?,?,'allow',?,'tool',?,? ,?,'Exact numeric constraint',1,0)`, id(30), id(1), id(10), `{"equals":{"/n":1.2300e+4}}`, stamp, stamp)
		exec(t, tx, `INSERT INTO grants(id,principal_id,effect,server_id,upstream_name,constraint_json,created_at,description,revision,read_only) VALUES(?,?,'allow',?,NULL,NULL,?,'Read only deleted target',1,1)`, id(31), id(1), id(13), stamp)
		exec(t, tx, `INSERT INTO grants(id,principal_id,effect,server_id,upstream_name,constraint_json,created_at,revision,read_only) VALUES(?,?,'deny',?,NULL,NULL,?,1,0)`, id(32), id(2), contract.SyntheticServerID, stamp)
		policy := fmt.Sprintf(`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"*.example.com","port":443},"methods":{"values":["GET","POST"]},"path":{"kind":"segment_prefix","value":"/api"}},"allow_private":true,"credential_id":%q}`, id(20))
		exec(t, tx, `INSERT INTO http_grants(id,principal_id,revision,policy_json,expires_at,created_at,updated_at) VALUES(?,?,1,?,?,?,?)`, id(33), id(1), policy, stamp, stamp, stamp)
		exec(t, tx, `INSERT INTO git_grants(id,principal_id,repository_id,revision,policy_json,expires_at,created_at,updated_at) VALUES(?,?,?,1,?,?,?,?)`, id(34), id(1), id(22), `{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/feature/"},"actions":["create","update"]}]}`, stamp, stamp, stamp)
		exec(t, tx, `UPDATE git_routing_profile SET origins_json='["https://github.com:443"]'`)
		// Canary material is deliberately unusable. Export must not ask for its
		// authority, decode it, or consult any healthy downstream/runtime.
		exec(t, tx, `INSERT INTO secret_generations(handle,owner,kind,custody,version,key_id,ciphertext) VALUES('secret-handle-canary',?,'static_credential','encrypted',1,?,?)`, id(10), strings.Repeat("0", 64), []byte(strings.Repeat("SECRET_MATERIAL_CANARY", 3)))
		exec(t, tx, `INSERT INTO admin_credentials(id,verifier,fingerprint,created_at,status,revision) VALUES(?,?, '0123456789abcdef',?,'active',1)`, id(40), bytes.Repeat([]byte("X"), 32), stamp)
		return nil
	}))
}

func TestConfigurationExportMixedProtocolsWithoutMaterial(t *testing.T) {
	store := fixture(t)
	seed(t, store)
	before, err := store.Identity(t.Context())
	require.NoError(t, err)
	body, err := New(store).ExportConfiguration(t.Context())
	require.NoError(t, err)
	var got contract.ConfigExport
	require.NoError(t, json.Unmarshal(body, &got))
	require.NoError(t, contract.ValidateConfigExport(got))
	require.Len(t, got.Agents, 2)
	require.Equal(t, "agent/1", got.Agents[0].Ref)
	require.Equal(t, contract.HTTPDefaultAllow, got.Agents[1].HTTPDefault)
	require.Len(t, got.MCP.Servers, 4)
	require.True(t, got.MCP.Servers[0].Enabled)
	require.Equal(t, map[string]string{"api": "mcp/server_0/static/api"}, got.MCP.Servers[0].Credentials.Static)
	require.Equal(t, "mcp/server_1/static/bearer", got.MCP.Servers[1].Credentials.Static["bearer"])
	require.Equal(t, "mcp/server_2/oauth/client_secret", *got.MCP.Servers[2].Credentials.OAuthClientSecret)
	require.True(t, got.MCP.Servers[2].Credentials.OAuthAuthorization)
	require.Contains(t, string(got.MCP.Servers[2].Transport), `"scopes":[]`)
	require.True(t, got.MCP.Servers[3].Deleted)
	require.JSONEq(t, "null", string(got.MCP.Servers[3].Transport))
	require.Len(t, got.MCP.Grants, 3)
	require.Contains(t, string(got.MCP.Grants[0].Constraint), "1.2300e+4")
	require.Equal(t, stamp, *got.MCP.Grants[0].ExpiresAt)
	require.True(t, got.MCP.Grants[1].ReadOnly)
	require.Equal(t, "mcp/server_3", got.MCP.Grants[1].Server)
	require.Equal(t, "mcp/mcp_gateway", got.MCP.Grants[2].Server)
	require.Equal(t, "http-credential/1", *got.HTTP.Grants[0].Policy.CredentialRef)
	require.Equal(t, "*.example.com", got.HTTP.Grants[0].Policy.Request.Origin.Host)
	require.Equal(t, contract.HTTPPathPrefix, got.HTTP.Grants[0].Policy.Request.Path.Kind)
	require.True(t, *got.HTTP.Grants[0].Policy.AllowPrivate)
	require.Equal(t, "repository/1", got.Git.Grants[0].Repository)
	require.True(t, got.Git.Repositories[0].Deleted)
	require.Equal(t, "git-credential/1", *got.Git.Repositories[0].CredentialRef)
	require.Equal(t, []string{"https://github.com:443"}, got.Git.RoutingOrigins)
	for _, forbidden := range []string{installation, "SECRET_MATERIAL_CANARY", "secret-handle-canary", "credential_revision", "revision", "fingerprint", "available", "created_at", "ciphertext", "operations", "runtime", "audit"} {
		require.NotContains(t, string(body), forbidden)
	}
	for i := 1; i <= 40; i++ {
		require.NotContains(t, string(body), id(i))
	}
	again, err := New(store).ExportConfiguration(t.Context())
	require.NoError(t, err)
	require.Equal(t, body, again)
	after, err := store.Identity(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestConfigurationProjectionRetainsOneConcurrentSnapshot(t *testing.T) {
	store := fixture(t)
	seed(t, store)
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
		var old string
		require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT display_name FROM principals WHERE id=?`, id(1)).Scan(&old))
		// Commit a complete cross-owner change after this reader pins its snapshot.
		require.NoError(t, store.Mutate(t.Context(), func(writer *sql.Tx) error {
			exec(t, writer, `UPDATE principals SET display_name='new' WHERE id=?`, id(1))
			exec(t, writer, `UPDATE servers SET display_name='new' WHERE id=?`, id(10))
			return nil
		}))
		got, err := configurationTx(t.Context(), tx)
		require.NoError(t, err)
		require.Equal(t, old, got.Agents[0].DisplayName)
		require.Equal(t, "Server", got.MCP.Servers[0].DisplayName)
		return nil
	}))
	body, err := New(store).ExportConfiguration(t.Context())
	require.NoError(t, err)
	var fresh contract.ConfigExport
	require.NoError(t, json.Unmarshal(body, &fresh))
	require.Equal(t, "new", fresh.Agents[0].DisplayName)
	require.Equal(t, "new", fresh.MCP.Servers[0].DisplayName)
}

func TestConfigurationExportRejectsBrokenGraphBeforeDelivery(t *testing.T) {
	store := fixture(t)
	seed(t, store)
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		exec(t, tx, `UPDATE http_grants SET policy_json=json_set(policy_json,'$.credential_id',?)`, id(99))
		return nil
	}))
	body, err := New(store).ExportConfiguration(t.Context())
	require.Error(t, err)
	require.Empty(t, body)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	body, err = New(store).ExportConfiguration(canceled)
	require.Error(t, err)
	require.Empty(t, body)
}
