//go:build integration

package scripts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntegrationLaunchdRestart(t *testing.T) {
	for _, mode := range []string{"running", "absent", "no-pid", "delayed-removal", "still-loaded", "process-stuck", "process-reused", "unknown-process", "unknown-service", "legacy-loaded", "wrong-argv", "wrong-plist", "bootout-error", "bootstrap-error"} {
		t.Run(mode, func(t *testing.T) {
			f := newLaunchdFixture(t)
			require.Zero(t, f.run(t).ExitCode)
			require.NoError(t, os.WriteFile(f.calls, nil, 0o600))
			f.script = filepath.Join(filepath.Dir(f.script), "restart-launchd-agent.sh")
			stopped := filepath.Join(t.TempDir(), "stopped")
			t.Setenv("AGENT_GATEWAY_TEST_STOPPED", stopped)
			t.Setenv("AGENT_GATEWAY_TEST_RESTART_MODE", mode)
			data := filepath.Join(f.home, ".local", "share", "agent-gateway")
			state := fmt.Sprintf("path = %s\nprogram = %s\narguments = {\n%s\nserve\n--data-dir\n%s\n--listen\n127.0.0.1:8210\n}\n", f.plist, f.binary, f.binary, data)
			if mode != "no-pid" {
				state += "pid = 12345\n"
			}
			if mode == "wrong-argv" {
				state = strings.ReplaceAll(state, "127.0.0.1:8210", "127.0.0.1:9999")
			}
			t.Setenv("AGENT_GATEWAY_TEST_LOADED_STATE", state)
			f.tool(t, "launchctl", `
printf '%s\n' "$1 $2" >> "$AGENT_GATEWAY_TEST_CALLS"
case "$1" in
 print)
  case "$2" in */dev.agent-tools.mcp-gateway)
   if [ "$AGENT_GATEWAY_TEST_RESTART_MODE" = legacy-loaded ]; then echo loaded; exit 0; fi
   echo 'Could not find service "dev.agent-tools.mcp-gateway" in domain';exit 113;;
  esac
  if [ "$AGENT_GATEWAY_TEST_RESTART_MODE" = unknown-service ]; then echo denied; exit 1; fi
  if [ "$AGENT_GATEWAY_TEST_RESTART_MODE" = delayed-removal ] && [ -f "$AGENT_GATEWAY_TEST_STOPPED" ] && [ ! -f "$AGENT_GATEWAY_TEST_STOPPED.observed" ]; then
   touch "$AGENT_GATEWAY_TEST_STOPPED.observed"; printf '%s' "$AGENT_GATEWAY_TEST_LOADED_STATE";exit 0
  fi
  if [ "$AGENT_GATEWAY_TEST_RESTART_MODE" = absent ] || { [ -f "$AGENT_GATEWAY_TEST_STOPPED" ] && [ "$AGENT_GATEWAY_TEST_RESTART_MODE" != still-loaded ]; }; then
   echo 'Could not find service "dev.agent-tools.agent-gateway" in domain';exit 113
  fi
  printf '%s' "$AGENT_GATEWAY_TEST_LOADED_STATE";;
 bootout)
  [ "$AGENT_GATEWAY_TEST_RESTART_MODE" != bootout-error ] || exit 1
  touch "$AGENT_GATEWAY_TEST_STOPPED";;
 bootstrap) [ "$AGENT_GATEWAY_TEST_RESTART_MODE" != bootstrap-error ];;
 *) exit 99;;
esac`)
			t.Setenv("AGENT_GATEWAY_TEST_PROCESS", fmt.Sprintf("%d Sun Sep 13 00:00:00 2026 %s", os.Getuid(), f.binary))
			f.tool(t, "ps", `
printf 'ps\n' >> "$AGENT_GATEWAY_TEST_CALLS"
if [ "$1" = -axwwo ]; then echo '0 1 /sbin/launchd';exit 0;fi
if [ "$AGENT_GATEWAY_TEST_RESTART_MODE" = unknown-process ]; then echo denied;exit 2;fi
if [ -f "$AGENT_GATEWAY_TEST_STOPPED" ]; then
 case "$AGENT_GATEWAY_TEST_RESTART_MODE" in
  process-stuck) printf '%s\n' "$AGENT_GATEWAY_TEST_PROCESS";exit 0;;
  process-reused) printf '%s\n' "$AGENT_GATEWAY_TEST_PROCESS" | sed s/00:00:00/00:00:01/;exit 0;;
  *) exit 1;;
 esac
fi
printf '%s\n' "$AGENT_GATEWAY_TEST_PROCESS"`)
			f.tool(t, "sleep", "exit 0")
			if mode == "wrong-plist" {
				require.NoError(t, os.WriteFile(f.plist, []byte("invalid"), 0o600))
			}
			result := f.run(t)
			calls, err := os.ReadFile(f.calls)
			require.NoError(t, err)
			stopCount := strings.Count(string(calls), "bootout ")
			startCount := strings.Count(string(calls), "bootstrap ")
			require.LessOrEqual(t, stopCount, 1)
			require.LessOrEqual(t, startCount, 1)
			switch mode {
			case "running", "absent", "delayed-removal":
				require.Zero(t, result.ExitCode, "%s", result.Stderr)
				require.Equal(t, 1, startCount)
				require.Contains(t, string(result.Stdout), "Readiness has not been checked")
			case "bootstrap-error":
				require.NotZero(t, result.ExitCode)
				require.Equal(t, 1, startCount)
			default:
				require.NotZero(t, result.ExitCode)
				require.Zero(t, startCount)
			}
			if mode == "wrong-argv" || mode == "wrong-plist" || mode == "unknown-process" || mode == "legacy-loaded" {
				require.Zero(t, stopCount)
			}
		})
	}
}
