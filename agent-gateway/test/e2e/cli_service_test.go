//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIServiceRetiredWithoutInstalledResourceAccess(t *testing.T) {
	binary := gatewayBinary(t)
	runner := firstRunRunner(t)
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_DATA_HOME", isolated)
	// Retained operator-owned artifacts must not be interpreted or rewritten.
	plist := filepath.Join(isolated, "dev.agent-tools.agent-gateway.plist")
	original := []byte("retained operator-owned definition\n")
	require.NoError(t, os.WriteFile(plist, original, 0600))
	for _, verb := range []string{"install", "start", "stop", "restart", "update", "status", "uninstall"} {
		result, err := runner.Run(t.Context(), binary, "service", verb)
		require.Error(t, err)
		require.Contains(t, string(result.Stderr), "The command is not recognized. Usage: agent-gateway --help")
		help, err := runner.Run(t.Context(), binary, "service", verb, "--help")
		require.NoError(t, err)
		require.NotContains(t, string(help.Stdout), "agent-gateway service")
	}
	help, err := runner.Run(t.Context(), binary, "--help")
	require.NoError(t, err)
	require.NotContains(t, string(help.Stdout), "Manage the macOS background service")
	completion, err := runner.Run(t.Context(), binary, "__complete", "serv")
	require.NoError(t, err)
	require.Contains(t, string(completion.Stdout), "serve\t")
	require.NotContains(t, string(completion.Stdout), "service\t")
	retained, err := os.ReadFile(plist)
	require.NoError(t, err)
	require.Equal(t, original, retained)
	entries, err := os.ReadDir(isolated)
	require.NoError(t, err)
	require.Len(t, entries, 1, "retired grammar must not initialize storage or create service files")
}
