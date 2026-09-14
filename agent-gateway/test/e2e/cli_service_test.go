//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIServiceInstalledGrammarAndPlatformRefusal(t *testing.T) {
	binary := gatewayBinary(t)
	runner := firstRunRunner(t)
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_DATA_HOME", isolated)
	for _, verb := range []string{"install", "start", "stop", "restart", "update", "status", "uninstall"} {
		help, err := runner.Run(t.Context(), binary, "service", verb, "--help")
		require.NoError(t, err)
		require.Contains(t, string(help.Stdout), "agent-gateway service "+verb)
		if runtime.GOOS != "darwin" {
			// Never invoke native mutations on macOS; those need separate resource consent.
			result, e := runner.Run(t.Context(), binary, "service", verb)
			require.Error(t, e)
			require.Empty(t, result.Stdout)
			require.Contains(t, string(result.Stderr), "requires macOS launchd")
		}
	}
	for _, args := range [][]string{{"service", "restart", "--log-level", "debug"}, {"service", "restart", "--data-dir", filepath.Join(isolated, "data")}, {"service", "update", "--clear-allowed-hosts", "--allowed-host", "host.example"}, {"service", "configure"}, {"service", "install", "unexpected"}} {
		result, err := runner.Run(t.Context(), binary, args...)
		require.Error(t, err, strings.Join(args, " "))
		require.Empty(t, result.Stdout)
		require.Contains(t, string(result.Stderr), "Usage:")
	}
	entries, err := os.ReadDir(isolated)
	require.NoError(t, err)
	require.Empty(t, entries, "service help/refusal must not initialize storage or create service files")
}
