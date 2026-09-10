//go:build integration

package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntegrationLaunchdRestart(t *testing.T) {
	for _, mode := range []string{"running", "absent", "unloaded-no-pid", "invalid-plist", "wrong-label", "print-error", "bootout-error", "still-loaded", "process-stuck", "process-error", "bootstrap-error"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newLaunchdFixture(t)
			require.Zero(t, fixture.run(t).ExitCode)
			fixture.script = filepath.Join(filepath.Dir(fixture.script), "restart-launchd-agent.sh")
			tools := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
			log := filepath.Join(t.TempDir(), "calls")
			t.Setenv("RESTART_CALLS", log)
			t.Setenv("RESTART_MODE", mode)
			t.Setenv("RESTART_STOPPED", filepath.Join(t.TempDir(), "stopped"))
			writeTool := func(name, body string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\n"+body), 0o700))
			}
			writeTool("launchctl", `
printf '%s\n' "$1" >> "$RESTART_CALLS"
case "$1" in
  print)
    if [ "$RESTART_MODE" = print-error ]; then echo 'permission denied' >&2; exit 1; fi
    if [ "$RESTART_MODE" = absent ] || { [ -f "$RESTART_STOPPED" ] && [ "$RESTART_MODE" != still-loaded ]; }; then
      echo 'Could not find service "dev.agent-tools.mcp-gateway" in domain for user gui: 501' >&2
      exit 113
    fi
    if [ "$RESTART_MODE" != unloaded-no-pid ]; then echo '  pid = 12345'; fi ;;
  bootout)
    [ "$RESTART_MODE" != bootout-error ] || exit 1
    touch "$RESTART_STOPPED" ;;
  bootstrap) [ "$RESTART_MODE" != bootstrap-error ] ;;
  *) exit 99 ;;
esac
`)
			writeTool("ps", `
printf 'ps\n' >> "$RESTART_CALLS"
case "$RESTART_MODE" in process-stuck) exit 0 ;; process-error) exit 2 ;; esac
exit 1
`)
			writeTool("sleep", "exit 0\n")
			if mode == "invalid-plist" {
				require.NoError(t, os.WriteFile(fixture.plist, []byte("invalid"), 0o600))
			}
			if mode == "wrong-label" {
				data, err := os.ReadFile(fixture.plist)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(fixture.plist, []byte(strings.ReplaceAll(string(data), "dev.agent-tools.mcp-gateway", "different.service")), 0o600))
			}
			result := fixture.run(t)
			calls, err := os.ReadFile(log)
			if os.IsNotExist(err) {
				calls = nil
			} else {
				require.NoError(t, err)
			}
			switch mode {
			case "running":
				require.Zero(t, result.ExitCode, result.Stderr)
				require.Equal(t, "print\nbootout\nprint\nps\nbootstrap\n", string(calls))
			case "absent":
				require.Zero(t, result.ExitCode, result.Stderr)
				require.Equal(t, "print\nbootstrap\n", string(calls))
			case "unloaded-no-pid":
				require.Zero(t, result.ExitCode, result.Stderr)
				require.Equal(t, "print\nbootout\nprint\nbootstrap\n", string(calls))
			case "bootstrap-error":
				require.NotZero(t, result.ExitCode)
				require.Equal(t, 1, strings.Count(string(calls), "bootstrap\n"))
				require.NotContains(t, result.Stdout, "Launch accepted")
			default:
				require.NotZero(t, result.ExitCode)
				require.NotContains(t, string(calls), "bootstrap")
				if mode == "invalid-plist" || mode == "wrong-label" {
					require.Empty(t, calls)
				}
				if mode == "process-stuck" {
					require.Equal(t, 31, strings.Count(string(calls), "ps\n"))
				}
			}
		})
	}
}
