//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestProvisionGatewayScript(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("provisioning targets Linux Lima guests and GNU sed")
	}
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	script, err := filepath.Abs("../../examples/provision/configure-mcp-gateway.sh")
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(5*time.Second, 16*1024)
	require.NoError(t, err)

	for _, state := range []string{"missing", "empty", "unreadable", "directory", "valid"} {
		t.Run(state, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home with spaces")
			tokenPath := filepath.Join(home, ".config", "mcp-gateway", "agent-token")
			require.NoError(t, os.MkdirAll(filepath.Dir(tokenPath), 0o700))
			t.Setenv("HOME", home)
			t.Setenv("BASH_ENV", "")
			t.Setenv("MCP_GATEWAY_ENDPOINT", "stale")
			t.Setenv("MCP_GATEWAY_AGENT_TOKEN", "stale")
			const token = "mgw_agent_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			switch state {
			case "empty":
				require.NoError(t, os.WriteFile(tokenPath, nil, 0o600))
			case "unreadable":
				if os.Geteuid() == 0 {
					t.Skip("root can read mode-000 files")
				}
				require.NoError(t, os.WriteFile(tokenPath, []byte(token), 0o000))
			case "directory":
				require.NoError(t, os.Mkdir(tokenPath, 0o700))
			case "valid":
				require.NoError(t, os.WriteFile(tokenPath, []byte(token+"\n"), 0o600))
			}
			bashrc := filepath.Join(home, ".bashrc")
			const existing = "export EXISTING=value\n# >>> mcp-broker >>>\nexport MCP_BROKER_ENDPOINT=unchanged\n# <<< mcp-broker <<<"
			require.NoError(t, os.WriteFile(bashrc, []byte(existing), 0o600))
			readRC := func() string {
				data, readErr := os.ReadFile(bashrc)
				require.NoError(t, readErr)
				return string(data)
			}
			run := func() {
				result, runErr := runner.Run(context.Background(), bash, script)
				require.NoError(t, runErr, "%s", result.Stderr)
				require.NotContains(t, string(result.Stdout)+string(result.Stderr), token)
			}
			if state != "valid" {
				result, runErr := runner.Run(context.Background(), bash, script)
				require.Error(t, runErr)
				require.Equal(t, 1, result.ExitCode)
				require.Contains(t, string(result.Stderr), "copy_paths")
				require.Equal(t, existing, readRC())
				return
			}
			run()
			first := readRC()
			require.True(t, strings.HasPrefix(first, existing+"\n# >>> mcp-gateway >>>\n"))
			require.NotContains(t, first, token)
			require.NotContains(t, first, "admin-bearer")
			run()
			require.Equal(t, first, readRC())
			require.Equal(t, 1, strings.Count(first, "# >>> mcp-gateway >>>"))

			stale := strings.Replace(first, "http://host.lima.internal:8210/mcp", "http://obsolete.invalid/mcp", 1)
			require.NoError(t, os.WriteFile(bashrc, []byte(stale+"export AFTER=preserved\n"), 0o600))
			run()
			require.NotContains(t, readRC(), "obsolete.invalid")
			require.Contains(t, readRC(), "export AFTER=preserved\n")

			for _, current := range []string{token, "mgw_agent_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"} {
				require.NoError(t, os.WriteFile(tokenPath, []byte(current+"\n"), 0o600))
				result, runErr := runner.Run(context.Background(), bash, "--noprofile", "--norc", "-c", `source "$HOME/.bashrc"; printf '%s\n' "$MCP_GATEWAY_ENDPOINT" "$MCP_GATEWAY_AGENT_TOKEN" "$EXISTING" "$AFTER" "$MCP_BROKER_ENDPOINT"`)
				require.NoError(t, runErr)
				require.Empty(t, result.Stderr)
				require.Equal(t, "http://host.lima.internal:8210/mcp\n"+current+"\nvalue\npreserved\nunchanged\n", string(result.Stdout))
				require.NotContains(t, readRC(), current)
			}
		})
	}
}
