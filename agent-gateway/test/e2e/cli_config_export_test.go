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
	var document contract.ConfigBundle
	require.NoError(t, json.Unmarshal(exported.Stdout, &document))
	require.NoError(t, contract.ValidateConfigBundleEnvelope(document))
	counts := map[string]int{}
	for _, d := range document.Resources {
		counts[d.Kind]++
		switch d.Kind {
		case "mcp_server":
			var spec contract.ConfigServerSpec
			require.NoError(t, json.Unmarshal(d.Spec, &spec))
			require.False(t, spec.Enabled)
			require.Equal(t, "export-canary", d.Key)
			require.Equal(t, "mcp/export-canary/static/primary", spec.Credentials.Static["primary"])
		case "mcp_grant":
			var spec contract.ConfigMCPGrantSpec
			require.NoError(t, json.Unmarshal(d.Spec, &spec))
			require.Equal(t, contract.ConfigReference{Kind: "gateway_mcp"}, spec.Server)
			require.Equal(t, contract.ConfigReference{Kind: "agent", Key: agent.Principal.Key}, spec.Agent)
		}
	}
	require.Equal(t, 1, counts["agent"])
	require.Equal(t, 1, counts["mcp_server"])
	require.Equal(t, 1, counts["mcp_grant"])
	require.NotContains(t, string(exported.Stdout), agent.Principal.ID)
	require.NotContains(t, string(exported.Stdout), harness.bearer)
	require.False(t, exported.StdoutTruncated)
	require.False(t, exported.StderrTruncated)
	require.True(t, exported.Cleanup.Reaped)
	harness.Stop(syscall.SIGTERM)
}
