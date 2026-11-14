//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestE2EOnlineConfigurationAdministratorCreateOnlyRoundtrip(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	agent := `{"format":"agent-gateway-config","version":2,"kind":"agent","key":"online-agent","spec":{"display_name":"Authored name not echoed by preview","state":"active","visibility":"allowed-only","http_default":"block"}}`
	denied := harness.requestSnapshot(http.MethodPost, contract.ConfigPreviewPath, []byte(agent), map[string]string{"Content-Type": "application/json"})
	require.Equal(t, 401, denied.StatusCode)
	response := harness.adminSnapshot(http.MethodPost, contract.ConfigPreviewPath, []byte(agent))
	require.Equal(t, 200, response.StatusCode)
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	require.NotContains(t, string(response.Body), "Authored name")
	var preview contract.ConfigPreview
	require.NoError(t, json.Unmarshal(response.Body, &preview))
	require.True(t, preview.Advisory)
	require.Len(t, preview.Creations, 1)
	require.Contains(t, preview.Creations[0].Requirements, "issue_agent_credential")
	response = harness.adminSnapshot(http.MethodGet, contract.ConfigExportPath, nil)
	require.Equal(t, 200, response.StatusCode)
	var before contract.ConfigBundle
	require.NoError(t, json.Unmarshal(response.Body, &before))
	require.Len(t, before.Resources, 1)
	require.Equal(t, "git_routing", before.Resources[0].Kind)
	response = harness.adminSnapshot(http.MethodPost, contract.ConfigImportPath, []byte(agent))
	require.Equal(t, 201, response.StatusCode)
	var result contract.ConfigImportResult
	require.NoError(t, json.Unmarshal(response.Body, &result))
	require.Equal(t, "committed", result.Configuration)
	require.Len(t, result.Creations, 1)
	require.True(t, contract.ValidAuditID(result.Creations[0].ID))
	response = harness.adminSnapshot(http.MethodPost, contract.ConfigImportPath, []byte(agent))
	require.Equal(t, 409, response.StatusCode)
	response = harness.adminSnapshot(http.MethodPost, contract.ConfigImportPath+"?mode=skip-identical", []byte(agent))
	require.Equal(t, 400, response.StatusCode)
	invalid := `{"format":"agent-gateway-config","version":2,"resources":[{"format":"agent-gateway-config","version":2,"kind":"agent","key":"must-rollback","spec":{"display_name":"Never created","state":"active","visibility":"allowed-only","http_default":"block"}},{"format":"agent-gateway-config","version":2,"kind":"mcp_grant","key":"dangling","spec":{"agent":{"kind":"agent","key":"must-rollback"},"server":{"kind":"mcp_server","key":"absent"},"description":null,"effect":"allow","upstream_name":null,"constraint":null,"read_only":false,"expires_at":null}}]}`
	response = harness.adminSnapshot(http.MethodPost, contract.ConfigImportPath, []byte(invalid))
	require.Equal(t, 400, response.StatusCode)
	server := `{"format":"agent-gateway-config","version":2,"kind":"mcp_server","key":"missing-material","spec":{"display_name":"Missing material","enabled":true,"deleted":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"primary"}},"credentials":{"static":{"primary":"mcp/missing-material/static/primary"},"oauth_client_secret":null,"oauth_authorization":false}}}`
	response = harness.adminSnapshot(http.MethodPost, contract.ConfigImportPath, []byte(server))
	require.Equal(t, 201, response.StatusCode)
	var serverResult contract.ConfigImportResult
	require.NoError(t, json.Unmarshal(response.Body, &serverResult))
	require.Equal(t, "committed", serverResult.Configuration)
	require.Equal(t, "pending", serverResult.Activation)
	response = harness.adminSnapshot(http.MethodGet, contract.ConfigExportPath, nil)
	require.Equal(t, 200, response.StatusCode)
	var exported contract.ConfigBundle
	require.NoError(t, json.Unmarshal(response.Body, &exported))
	require.NoError(t, contract.ValidateConfigBundleEnvelope(exported))
	require.Len(t, exported.Resources, 3, "only explicit agent/server and existing routing; no implicit grants or partial invalid graph")
	for _, d := range exported.Resources {
		require.NotEqual(t, "mcp_grant", d.Kind)
		require.NotEqual(t, "must-rollback", d.Key)
	}
	harness.Stop(syscall.SIGTERM)
	harness.Start()
	response = harness.adminSnapshot(http.MethodGet, contract.ConfigExportPath, nil)
	require.Equal(t, 200, response.StatusCode)
	require.JSONEq(t, string(mustMarshalConfigurationE2E(t, exported)), string(response.Body))
	response = harness.adminSnapshot(http.MethodGet, "/api/v2/principals/"+result.Creations[0].ID, nil)
	require.Equal(t, 200, response.StatusCode)
	var principal contract.Principal
	require.NoError(t, json.Unmarshal(response.Body, &principal))
	require.Equal(t, "online-agent", principal.Key)
	require.Nil(t, principal.Credential)
	harness.Stop(syscall.SIGTERM)
}
func mustMarshalConfigurationE2E(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}
