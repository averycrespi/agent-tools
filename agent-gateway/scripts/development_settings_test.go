//go:build integration

package scripts

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestMakeRejectsRetiredDevelopmentSettings(t *testing.T) {
	module, err := filepath.Abs("..")
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(5*time.Second, 16*1024)
	require.NoError(t, err)
	for retired, replacement := range map[string]string{
		"STRESS_COUNT":        "AGENT_GATEWAY_STRESS_COUNT",
		"DEMO_LISTEN":         "AGENT_GATEWAY_DEMO_LISTEN",
		"DEMO_DATASET":        "AGENT_GATEWAY_DEMO_DATASET",
		"TEST_JSON":           "AGENT_GATEWAY_TEST_JSON",
		"GATEWAY_BUILD_DIR":   "AGENT_GATEWAY_BUILD_DIR",
		"GATEWAY_INSTALL_DIR": "AGENT_GATEWAY_INSTALL_DIR",
	} {
		for _, value := range []string{"", "private-canary"} {
			result, runErr := runner.Run(t.Context(), "make", "-C", module, "help", retired+"="+value)
			require.Error(t, runErr)
			require.Equal(t, 2, result.ExitCode)
			require.Contains(t, string(result.Stderr), retired+" is retired; use "+replacement)
			require.NotContains(t, string(result.Stdout)+string(result.Stderr), "private-canary")
		}
		result, runErr := runner.Run(t.Context(), "env", retired+"=", "make", "-C", module, "help")
		require.Error(t, runErr)
		require.Contains(t, string(result.Stderr), retired+" is retired; use "+replacement)
	}
}
