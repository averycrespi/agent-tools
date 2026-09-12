package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevelopmentEnvironmentRejectsRetiredControls(t *testing.T) {
	for _, name := range []string{"UI_LISTEN", "TEST_CLEANUP_LEDGER", "E2E_ACCOUNT_HOME", "KEYRING_NATIVE", "DISPOSABLE_MACOS_KEYCHAIN", "STDIO_FIXTURE"} {
		for _, value := range []string{"", "private-canary"} {
			err := CheckDevelopmentEnvironment([]string{"MCP_GATEWAY_" + name + "=" + value, "AGENT_GATEWAY_" + name + "=new"})
			require.EqualError(t, err, "MCP_GATEWAY_"+name+" is retired; use AGENT_GATEWAY_"+name)
		}
	}
	for old, replacement := range map[string]string{
		"DEMO_TEST_SCENARIO":         "AGENT_GATEWAY_DEMO_TEST_SCENARIO",
		"GATEWAY_INSTALL_DIR":        "AGENT_GATEWAY_INSTALL_DIR",
		"RESTART_MODE":               "AGENT_GATEWAY_RESTART_MODE",
		"MCP_FALLBACK_PROBE_PROCESS": "AGENT_GATEWAY_MCP_FALLBACK_PROBE_PROCESS",
	} {
		require.EqualError(t, CheckDevelopmentEnvironment([]string{old + "="}), old+" is retired; use "+replacement)
	}
	require.NoError(t, CheckDevelopmentEnvironment([]string{
		"AGENT_GATEWAY_UI_LISTEN=127.0.0.1:5173", "MCP_GATEWAY_ENDPOINT=client-endpoint", "MCP_GATEWAY_AGENT_TOKEN=private-canary", "PATH=/bin",
	}))
}
