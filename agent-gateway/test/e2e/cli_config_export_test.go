//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestCLIConfigurationExportOnlineWithoutUpstreamMaterial(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	configPath := filepath.Join(t.TempDir(), "server.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"namespace":"export-canary","display_name":"Missing upstream material","enabled":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"primary"}}}`), 0o600))
	runOnlineCLI(t, harness, bearerPath, true, "mcp", "server", "create", "--file", configPath, "--idempotency-key", "config-export-create", "--json")
	created := runOnlineCLI(t, harness, bearerPath, true, "agent", "create", "--display-name", "Export agent", "--visibility", "allowed-only", "--json")
	var agent contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(created.Stdout, &agent))
	exported := runOnlineCLI(t, harness, bearerPath, true, "export", "config")
	require.Empty(t, exported.Stderr)
	var document contract.ConfigExport
	require.NoError(t, json.Unmarshal(exported.Stdout, &document))
	require.NoError(t, contract.ValidateConfigExport(document))
	require.Len(t, document.Agents, 1)
	require.Len(t, document.MCP.Servers, 1)
	require.False(t, document.MCP.Servers[0].Enabled)
	require.Equal(t, "mcp/export-canary/static/primary", document.MCP.Servers[0].Credentials.Static["primary"])
	require.Len(t, document.MCP.Grants, 1)
	require.Equal(t, "mcp/mcp_gateway", document.MCP.Grants[0].Server)
	require.Equal(t, "agent/1", document.MCP.Grants[0].Agent)
	require.NotContains(t, string(exported.Stdout), agent.Principal.ID)
	require.NotContains(t, string(exported.Stdout), harness.bearer)
	require.False(t, exported.StdoutTruncated)
	require.False(t, exported.StderrTruncated)
	require.True(t, exported.Cleanup.Reaped)
	harness.Stop(syscall.SIGTERM)
}
