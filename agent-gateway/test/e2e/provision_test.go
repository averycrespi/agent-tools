//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

const provisionToken = "mgw_agent_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const rotatedProvisionToken = "mgw_agent_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
const provisionUnrelated = "export EXISTING=value\n# >>> mcp-broker >>>\nexport MCP_BROKER_ENDPOINT=unchanged\n# <<< mcp-broker <<<"

func TestProvisionGatewayScript(t *testing.T) {
	_, err := os.Lstat("../../examples/provision/configure-mcp-gateway.sh")
	require.ErrorIs(t, err, os.ErrNotExist, "retired script must not be published")
	for _, paths := range []string{"canonical", "both"} {
		t.Run(paths, func(t *testing.T) {
			home, run := provisionFixture(t, "configure-agent-gateway.sh")
			writeTokens := func(token string) {
				if paths == "both" {
					writeProvisionToken(t, home, "mcp-gateway", token+"\n")
				}
				writeProvisionToken(t, home, "agent-gateway", token)
			}
			writeTokens(provisionToken)
			rc := filepath.Join(home, ".bashrc")
			require.NoError(t, os.WriteFile(rc, []byte(provisionUnrelated), 0o600))
			require.True(t, run())
			first := readProvisionRC(t, home)
			require.True(t, strings.HasPrefix(first, provisionUnrelated+"\n# >>> agent-gateway >>>\n"))
			require.NotContains(t, first, provisionToken)
			require.NotContains(t, first, "# >>> mcp-gateway >>>")
			require.True(t, run())
			require.Equal(t, first, readProvisionRC(t, home))
			require.Equal(t, 1, strings.Count(first, "# >>> agent-gateway >>>"))
			info, err := os.Stat(rc)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			for _, token := range []string{provisionToken, rotatedProvisionToken} {
				writeTokens(token)
				assertProvisionStartup(t, home, token, true)
				require.Equal(t, first, readProvisionRC(t, home), "rotation needs no bashrc rewrite")
			}
		})
	}
}

func TestProvisionGatewayManagedMigration(t *testing.T) {
	for _, name := range []string{"mcp-gateway", "agent-gateway", "fresh"} {
		t.Run(name, func(t *testing.T) {
			home, run := provisionFixture(t, "configure-agent-gateway.sh")
			writeProvisionToken(t, home, "agent-gateway", provisionToken)
			if name != "fresh" {
				old := provisionUnrelated + "\n# >>> " + name + " >>>\nexport MCP_GATEWAY_ENDPOINT=obsolete\n# <<< " + name + " <<<\nexport AFTER=preserved"
				require.NoError(t, os.WriteFile(filepath.Join(home, ".bashrc"), []byte(old), 0o600))
			}
			require.True(t, run())
			first := readProvisionRC(t, home)
			require.NotContains(t, first, "obsolete")
			if name != "fresh" {
				require.True(t, strings.HasPrefix(first, provisionUnrelated+"\nexport AFTER=preserved\n# >>> agent-gateway >>>\n"))
			}
			require.True(t, run())
			require.Equal(t, first, readProvisionRC(t, home))
			assertProvisionStartup(t, home, provisionToken, true)
		})
	}
}

func TestProvisionGatewayRefusesAmbiguousMarkers(t *testing.T) {
	for _, layout := range []string{
		"# >>> mcp-gateway >>>\nunterminated",
		"# <<< agent-gateway <<<\n",
		"# >>> mcp-gateway >>>\n# <<< agent-gateway <<<\n",
		"# >>> mcp-gateway >>>\n# >>> agent-gateway >>>\n# <<< agent-gateway <<<\n# <<< mcp-gateway <<<\n",
		"# >>> mcp-gateway >>>\n# <<< mcp-gateway <<<\n# >>> mcp-gateway >>>\n# <<< mcp-gateway <<<\n",
		"# >>> mcp-gateway >>>\n# <<< mcp-gateway <<<\n# >>> agent-gateway >>>\n# <<< agent-gateway <<<\n",
		"prefix # >>> agent-gateway >>>\n",
		"# >>> mcp-gateway >>> trailing\n",
		"# >>>  agent-gateway >>>\n",
		"unrelated\x00bytes\n",
	} {
		t.Run(layout, func(t *testing.T) {
			home, run := provisionFixture(t, "configure-agent-gateway.sh")
			writeProvisionToken(t, home, "agent-gateway", provisionToken)
			before := provisionUnrelated + "\n" + layout
			require.NoError(t, os.WriteFile(filepath.Join(home, ".bashrc"), []byte(before), 0o600))
			require.False(t, run())
			require.Equal(t, before, readProvisionRC(t, home))
		})
	}
}

func TestProvisionGatewayRejectsUnsafeTokens(t *testing.T) {
	for _, state := range []string{"missing", "empty", "directory", "symlink", "public-file", "public-directory", "writable-config", "unreadable", "invalid", "administrator", "nul", "conflict", "legacy-only", "empty-canonical-with-legacy"} {
		t.Run(state, func(t *testing.T) {
			home, run := provisionFixture(t, "configure-agent-gateway.sh")
			path := writeProvisionToken(t, home, "agent-gateway", provisionToken)
			switch state {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "empty":
				require.NoError(t, os.WriteFile(path, nil, 0o600))
			case "directory":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700))
			case "symlink":
				require.NoError(t, os.Rename(path, path+".real"))
				require.NoError(t, os.Symlink(path+".real", path))
			case "public-file":
				require.NoError(t, os.Chmod(path, 0o644))
			case "public-directory":
				require.NoError(t, os.Chmod(filepath.Dir(path), 0o755))
			case "writable-config":
				require.NoError(t, os.Chmod(filepath.Join(home, ".config"), 0o777))
			case "unreadable":
				require.NoError(t, os.Chmod(path, 0o000))
			case "invalid":
				require.NoError(t, os.WriteFile(path, []byte("invalid"), 0o600))
			case "administrator":
				require.NoError(t, os.WriteFile(path, []byte("mgw_admin_"+strings.Repeat("A", 43)), 0o600))
			case "nul":
				require.NoError(t, os.WriteFile(path, []byte(provisionToken+"\x00"), 0o600))
			case "conflict":
				writeProvisionToken(t, home, "mcp-gateway", rotatedProvisionToken)
			case "legacy-only":
				require.NoError(t, os.Remove(path))
				writeProvisionToken(t, home, "mcp-gateway", provisionToken)
			case "empty-canonical-with-legacy":
				require.NoError(t, os.WriteFile(path, nil, 0o600))
				writeProvisionToken(t, home, "mcp-gateway", provisionToken)
			}
			require.NoError(t, os.WriteFile(filepath.Join(home, ".bashrc"), []byte(provisionUnrelated), 0o600))
			require.False(t, run())
			require.Equal(t, provisionUnrelated, readProvisionRC(t, home))
		})
	}
}

func TestProvisionGatewayRejectsUnsafeDirectories(t *testing.T) {
	for _, directory := range []string{"agent-gateway", "mcp-gateway"} {
		for _, state := range []string{"empty-symlink", "dangling-symlink", "public-directory", "regular-file"} {
			t.Run(directory+"/"+state, func(t *testing.T) {
				home, run := provisionFixture(t, "configure-agent-gateway.sh")
				writeProvisionToken(t, home, "agent-gateway", provisionToken)
				writeProvisionToken(t, home, "mcp-gateway", provisionToken)
				require.True(t, run())
				before := readProvisionRC(t, home)
				path := filepath.Join(home, ".config", directory)
				require.NoError(t, os.Remove(filepath.Join(path, "agent-token")))
				require.NoError(t, os.Remove(path))
				switch state {
				case "empty-symlink":
					require.NoError(t, os.Symlink(t.TempDir(), path))
				case "dangling-symlink":
					require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), path))
				case "public-directory":
					require.NoError(t, os.Mkdir(path, 0o700))
					require.NoError(t, os.Chmod(path, 0o755))
				case "regular-file":
					require.NoError(t, os.WriteFile(path, nil, 0o600))
				}
				require.False(t, run(), "unsafe parent must not become missing-token fallback")
				require.Equal(t, before, readProvisionRC(t, home))
				assertProvisionStartup(t, home, "", false)
			})
		}
	}
}

func TestProvisionGatewayStartupFailsClosed(t *testing.T) {
	for _, state := range []string{"removed", "empty", "conflict", "legacy-only"} {
		t.Run(state, func(t *testing.T) {
			home, run := provisionFixture(t, "configure-agent-gateway.sh")
			path := writeProvisionToken(t, home, "agent-gateway", provisionToken)
			require.True(t, run())
			switch state {
			case "removed", "legacy-only":
				require.NoError(t, os.Remove(path))
				if state == "legacy-only" {
					writeProvisionToken(t, home, "mcp-gateway", provisionToken)
				}
			case "empty":
				require.NoError(t, os.WriteFile(path, nil, 0o600))
			case "conflict":
				writeProvisionToken(t, home, "mcp-gateway", rotatedProvisionToken)
			}
			assertProvisionStartup(t, home, "", false)
		})
	}
}

func provisionFixture(t *testing.T, entry string) (string, func() bool) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home with spaces")
	require.NoError(t, os.Mkdir(home, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("BASH_ENV", "")
	for _, name := range []string{"AGENT_GATEWAY_ENDPOINT", "AGENT_GATEWAY_AGENT_TOKEN", "MCP_GATEWAY_ENDPOINT", "MCP_GATEWAY_AGENT_TOKEN"} {
		t.Setenv(name, "stale-"+name)
	}
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join("../../examples/provision", entry))
	require.NoError(t, err)
	// sb provision copies one script at a time, without siblings or its name.
	script := filepath.Join(t.TempDir(), "sb-provision-script")
	require.NoError(t, os.WriteFile(script, data, 0o700))
	runner, err := testutil.NewBinaryRunner(5*time.Second, 16*1024)
	require.NoError(t, err)
	return home, func() bool {
		result, runErr := runner.Run(context.Background(), bash, "-x", script)
		output := string(result.Stdout) + string(result.Stderr)
		require.NotContains(t, output, provisionToken)
		require.NotContains(t, output, rotatedProvisionToken)
		if runErr != nil {
			require.Equal(t, 1, result.ExitCode, "%s", output)
		}
		return runErr == nil
	}
}

func writeProvisionToken(t *testing.T, home, directory, token string) string {
	t.Helper()
	path := filepath.Join(home, ".config", directory, "agent-token")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(token), 0o600))
	return path
}

func readProvisionRC(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	require.NoError(t, err)
	return string(data)
}

func assertProvisionStartup(t *testing.T, home, token string, valid bool) {
	t.Helper()
	t.Setenv("EXPECTED_TOKEN", token)
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(5*time.Second, 16*1024)
	require.NoError(t, err)
	check := `source "$HOME/.bashrc"; bash --noprofile --norc -c '
if [[ -n "$EXPECTED_TOKEN" ]]; then
  [[ "$AGENT_GATEWAY_ENDPOINT" == http://host.lima.internal:8210/mcp && "$MCP_GATEWAY_ENDPOINT" == "$AGENT_GATEWAY_ENDPOINT" && "$AGENT_GATEWAY_AGENT_TOKEN" == "$EXPECTED_TOKEN" && "$MCP_GATEWAY_AGENT_TOKEN" == "$EXPECTED_TOKEN" ]] || exit 1
else
  [[ -z "${AGENT_GATEWAY_ENDPOINT+x}${MCP_GATEWAY_ENDPOINT+x}${AGENT_GATEWAY_AGENT_TOKEN+x}${MCP_GATEWAY_AGENT_TOKEN+x}" ]] || exit 1
fi
printf "startup verified\n"
'`
	result, runErr := runner.Run(context.Background(), bash, "--noprofile", "--norc", "-x", "-c", check)
	require.NoError(t, runErr)
	require.Equal(t, "startup verified\n", string(result.Stdout))
	require.NotContains(t, string(result.Stderr), provisionToken)
	require.NotContains(t, string(result.Stderr), rotatedProvisionToken)
	if valid {
		require.NotContains(t, string(result.Stderr), "no client credentials exported")
	} else {
		require.Contains(t, string(result.Stderr), "no client credentials exported")
	}
	require.NotContains(t, readProvisionRC(t, home), provisionToken)
}
