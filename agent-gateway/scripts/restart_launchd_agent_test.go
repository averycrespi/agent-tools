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
	for _, mode := range []string{"running", "delayed-removal", "delayed-removal-process-stuck", "post-stop-error", "absent", "unloaded-no-pid", "invalid-plist", "wrong-label", "print-error", "bootout-error", "still-loaded", "process-stuck", "process-error", "bootstrap-error"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newLaunchdFixture(t)
			require.Zero(t, fixture.run(t).ExitCode)
			fixture.script = filepath.Join(filepath.Dir(fixture.script), "restart-launchd-agent.sh")
			tools := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
			log := filepath.Join(t.TempDir(), "calls")
			t.Setenv("AGENT_GATEWAY_RESTART_CALLS", log)
			t.Setenv("AGENT_GATEWAY_RESTART_MODE", mode)
			t.Setenv("AGENT_GATEWAY_RESTART_STOPPED", filepath.Join(t.TempDir(), "stopped"))
			writeTool := func(name, body string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\n"+body), 0o700))
			}
			writeTool("launchctl", `
printf '%s\n' "$1" >> "$AGENT_GATEWAY_RESTART_CALLS"
case "$1" in
  print)
    if [ "$AGENT_GATEWAY_RESTART_MODE" = print-error ]; then echo 'permission denied' >&2; exit 1; fi
    if [ "$AGENT_GATEWAY_RESTART_MODE" = post-stop-error ] && [ -f "$AGENT_GATEWAY_RESTART_STOPPED" ]; then echo 'permission denied' >&2; exit 1; fi
    if { [ "$AGENT_GATEWAY_RESTART_MODE" = delayed-removal ] || [ "$AGENT_GATEWAY_RESTART_MODE" = delayed-removal-process-stuck ]; } && [ -f "$AGENT_GATEWAY_RESTART_STOPPED" ] && [ ! -f "$AGENT_GATEWAY_RESTART_STOPPED.observed" ]; then
      touch "$AGENT_GATEWAY_RESTART_STOPPED.observed"
      echo '  pid = 12345'
      exit 0
    fi
    if [ "$AGENT_GATEWAY_RESTART_MODE" = absent ] || { [ -f "$AGENT_GATEWAY_RESTART_STOPPED" ] && [ "$AGENT_GATEWAY_RESTART_MODE" != still-loaded ]; }; then
      echo 'Could not find service "dev.agent-tools.mcp-gateway" in domain for user gui: 501' >&2
      exit 113
    fi
    if [ "$AGENT_GATEWAY_RESTART_MODE" != unloaded-no-pid ]; then echo '  pid = 12345'; fi ;;
  bootout)
    [ "$AGENT_GATEWAY_RESTART_MODE" != bootout-error ] || exit 1
    touch "$AGENT_GATEWAY_RESTART_STOPPED" ;;
  bootstrap) [ "$AGENT_GATEWAY_RESTART_MODE" != bootstrap-error ] ;;
  *) exit 99 ;;
esac
`)
			writeTool("ps", `
printf 'ps\n' >> "$AGENT_GATEWAY_RESTART_CALLS"
case "$AGENT_GATEWAY_RESTART_MODE" in process-stuck|delayed-removal-process-stuck) exit 0 ;; process-error) exit 2 ;; esac
exit 1
`)
			writeTool("sleep", "printf 'sleep\\n' >> \"$AGENT_GATEWAY_RESTART_CALLS\"\nexit 0\n")
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
			case "delayed-removal":
				require.Zero(t, result.ExitCode, result.Stderr)
				require.Equal(t, "print\nbootout\nprint\nsleep\nprint\nps\nbootstrap\n", string(calls))
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
				if mode == "delayed-removal-process-stuck" {
					require.Equal(t, 30, strings.Count(string(calls), "ps\n"))
				}
				if mode == "still-loaded" {
					require.Equal(t, 32, strings.Count(string(calls), "print\n"))
					require.NotContains(t, string(calls), "ps\n")
				}
				if mode == "process-stuck" || mode == "delayed-removal-process-stuck" || mode == "still-loaded" {
					require.Equal(t, 30, strings.Count(string(calls), "sleep\n"))
					require.Equal(t, 1, strings.Count(string(calls), "bootout\n"))
				}
				if mode == "post-stop-error" {
					require.Equal(t, "print\nbootout\nprint\n", string(calls))
				}
			}
		})
	}
}
