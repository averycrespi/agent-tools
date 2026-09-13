//go:build integration

package scripts

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

type plistNode struct {
	XMLName xml.Name
	Text    string      `xml:",chardata"`
	Nodes   []plistNode `xml:",any"`
}

func (node *plistNode) member(t *testing.T, key string) *plistNode {
	t.Helper()
	for i := 0; i+1 < len(node.Nodes); i++ {
		if node.Nodes[i].XMLName.Local == "key" && node.Nodes[i].Text == key {
			return &node.Nodes[i+1]
		}
	}
	t.Fatalf("missing plist key %q", key)
	return nil
}
func readPlist(t *testing.T, path string) plistNode {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var node plistNode
	require.NoError(t, xml.Unmarshal(data, &node))
	require.Len(t, node.Nodes, 1)
	return node.Nodes[0]
}

type launchdFixture struct{ home, binary, script, plist, tools, calls string }

func (fixture launchdFixture) tool(t *testing.T, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(fixture.tools, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700))
}
func newLaunchdFixture(t *testing.T) launchdFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "account & <home> 'with spaces")
	tools := filepath.Join(root, "tools")
	gopath := filepath.Join(root, "go & tools")
	for _, directory := range []string{home, tools, filepath.Join(gopath, "bin")} {
		require.NoError(t, os.MkdirAll(directory, 0o700))
	}
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	f := launchdFixture{home: home, tools: tools, binary: filepath.Join(gopath, "bin", "agent-gateway"), script: filepath.Join(filepath.Dir(source), "install-launchd-agent.sh"), plist: filepath.Join(home, "Library", "LaunchAgents", "dev.agent-tools.agent-gateway.plist"), calls: filepath.Join(root, "calls")}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", filepath.Join(root, "wrong-home"))
	t.Setenv("XDG_DATA_HOME", "")
	var escaped bytes.Buffer
	require.NoError(t, xml.EscapeText(&escaped, []byte(home)))
	t.Setenv("AGENT_GATEWAY_TEST_ACCOUNT_PLIST", `<plist version="1.0"><dict><key>dsAttrTypeStandard:NFSHomeDirectory</key><array><string>`+escaped.String()+`</string></array></dict></plist>`)
	t.Setenv("AGENT_GATEWAY_TEST_GOPATH", gopath)
	t.Setenv("AGENT_GATEWAY_TEST_PLATFORM", "Darwin")
	t.Setenv("AGENT_GATEWAY_TEST_CALLS", f.calls)
	t.Setenv("AGENT_GATEWAY_TEST_FORBIDDEN", filepath.Join(root, "forbidden"))
	f.tool(t, "dscl", `printf '%s\n' "$AGENT_GATEWAY_TEST_ACCOUNT_PLIST"`)
	f.tool(t, "uname", `printf '%s\n' "$AGENT_GATEWAY_TEST_PLATFORM"`)
	f.tool(t, "go", `test "$1 $2" = 'env GOPATH' || exit 1; printf '%s\n' "$AGENT_GATEWAY_TEST_GOPATH"`)
	f.tool(t, "launchctl", `printf '%s\n' "$1 $2" >> "$AGENT_GATEWAY_TEST_CALLS"
[ "$1" = print ] || { touch "$AGENT_GATEWAY_TEST_FORBIDDEN"; exit 1; }
printf 'Could not find service "%s" in domain\n' "${2##*/}" >&2
exit 113`)
	require.NoError(t, os.WriteFile(f.binary, []byte("#!/bin/sh\ntouch \"$AGENT_GATEWAY_TEST_FORBIDDEN\"\nexit 1\n"), 0o700))
	t.Cleanup(func() {
		for _, path := range []string{os.Getenv("HOME"), os.Getenv("AGENT_GATEWAY_TEST_FORBIDDEN")} {
			_, err := os.Lstat(path)
			require.ErrorIs(t, err, os.ErrNotExist)
		}
	})
	return f
}
func (fixture launchdFixture) run(t *testing.T, args ...string) testutil.ProcessResult {
	t.Helper()
	runner, err := testutil.NewBinaryRunner(45*time.Second, 64<<10)
	require.NoError(t, err)
	result, runErr := runner.Run(t.Context(), "/bin/bash", append([]string{fixture.script}, args...)...)
	require.GreaterOrEqual(t, result.ExitCode, 0, "%v", runErr)
	require.False(t, result.StdoutTruncated)
	require.False(t, result.StderrTruncated)
	return result
}
func TestIntegrationLaunchdInstaller(t *testing.T) {
	for _, mode := range []string{"default", "xdg", "explicit", "custom-arguments"} {
		t.Run(mode, func(t *testing.T) {
			f := newLaunchdFixture(t)
			data := filepath.Join(f.home, ".local", "share", "agent-gateway")
			var args []string
			switch mode {
			case "xdg":
				base := filepath.Join(f.home, "XDG & <data>")
				t.Setenv("XDG_DATA_HOME", base)
				data = filepath.Join(base, "agent-gateway")
			case "explicit":
				t.Setenv("XDG_DATA_HOME", "relative-ignored")
				data = filepath.Join(f.home, "custom & <data>")
				args = []string{"--binary", f.binary, "--data-dir", data}
			case "custom-arguments":
				args = []string{"--listen", "127.0.0.1:8321", "--allowed-host", "gateway.example"}
			}
			result := f.run(t, args...)
			require.Zero(t, result.ExitCode, "%s", result.Stderr)
			require.Contains(t, string(result.Stdout), "Data directory: "+data)
			dict := readPlist(t, f.plist)
			require.Equal(t, "dev.agent-tools.agent-gateway", dict.member(t, "Label").Text)
			var actual []string
			for _, arg := range dict.member(t, "ProgramArguments").Nodes {
				actual = append(actual, arg.Text)
			}
			expected := []string{f.binary, "serve", "--data-dir", data, "--listen", "127.0.0.1:8210"}
			if mode == "custom-arguments" {
				expected[5] = "127.0.0.1:8321"
				expected = append(expected, "--allowed-host", "gateway.example")
			}
			require.Equal(t, expected, actual)
			logs := filepath.Join(f.home, "Library", "Logs", "agent-gateway")
			for path, mode := range map[string]os.FileMode{f.plist: 0o600, logs: 0o700, filepath.Join(logs, "stdout.log"): 0o600, filepath.Join(logs, "stderr.log"): 0o600} {
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.Equal(t, mode, info.Mode().Perm())
			}
			_, err := os.Lstat(data)
			require.ErrorIs(t, err, os.ErrNotExist)
			before, err := os.ReadFile(f.plist)
			require.NoError(t, err)
			require.NotZero(t, f.run(t, args...).ExitCode)
			after, err := os.ReadFile(f.plist)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
func TestIntegrationLaunchdInstallerRefusals(t *testing.T) {
	for _, mode := range []string{"relative-data", "relative-binary", "relative-xdg", "missing-binary", "multiple-gopath", "non-macos", "symlink-plist", "public-logs", "symlink-logs", "legacy-plist", "legacy-service", "canonical-service", "unknown-service", "legacy-only", "both"} {
		t.Run(mode, func(t *testing.T) {
			f := newLaunchdFixture(t)
			var args []string
			switch mode {
			case "relative-data":
				args = []string{"--data-dir", "relative"}
			case "relative-binary":
				args = []string{"--binary", "relative"}
			case "relative-xdg":
				t.Setenv("XDG_DATA_HOME", "relative")
			case "missing-binary":
				args = []string{"--binary", filepath.Join(f.home, "missing")}
			case "multiple-gopath":
				t.Setenv("AGENT_GATEWAY_TEST_GOPATH", "/one:/two")
			case "non-macos":
				t.Setenv("AGENT_GATEWAY_TEST_PLATFORM", "Linux")
			case "symlink-plist":
				require.NoError(t, os.MkdirAll(filepath.Dir(f.plist), 0o700))
				require.NoError(t, os.Symlink(filepath.Join(f.home, "missing"), f.plist))
			case "legacy-plist":
				require.NoError(t, os.MkdirAll(filepath.Dir(f.plist), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(f.plist), "dev.agent-tools.mcp-gateway.plist"), []byte("preserve"), 0o600))
			case "public-logs", "symlink-logs":
				logs := filepath.Join(f.home, "Library", "Logs", "agent-gateway")
				require.NoError(t, os.MkdirAll(filepath.Dir(logs), 0o700))
				if mode == "public-logs" {
					require.NoError(t, os.Mkdir(logs, 0o700))
					require.NoError(t, os.Chmod(logs, 0o755))
				} else {
					require.NoError(t, os.Symlink(f.home, logs))
				}
			case "legacy-service":
				f.tool(t, "launchctl", "exit 0")
			case "canonical-service":
				f.tool(t, "launchctl", `case "$2" in */dev.agent-tools.agent-gateway) exit 0;; esac
printf 'Could not find service "%s" in domain\n' "${2##*/}"; exit 113`)
			case "unknown-service":
				f.tool(t, "launchctl", "echo 'permission denied'; exit 1")
			case "legacy-only", "both":
				base := filepath.Join(f.home, "state")
				t.Setenv("XDG_DATA_HOME", base)
				require.NoError(t, os.MkdirAll(filepath.Join(base, "mcp-gateway"), 0o700))
				if mode == "both" {
					require.NoError(t, os.Mkdir(filepath.Join(base, "agent-gateway"), 0o700))
				}
			}
			result := f.run(t, args...)
			require.NotZero(t, result.ExitCode)
			if mode != "symlink-plist" {
				_, err := os.Lstat(f.plist)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}
func TestIntegrationLaunchdInstallerExplicitLegacyAndCanonicalRoots(t *testing.T) {
	for _, mode := range []string{"legacy-explicit", "canonical-only"} {
		t.Run(mode, func(t *testing.T) {
			f := newLaunchdFixture(t)
			base := filepath.Join(f.home, "state")
			t.Setenv("XDG_DATA_HOME", base)
			name := "agent-gateway"
			var args []string
			if mode == "legacy-explicit" {
				name = "mcp-gateway"
				args = []string{"--data-dir", filepath.Join(base, name)}
			}
			require.NoError(t, os.MkdirAll(filepath.Join(base, name), 0o700))
			result := f.run(t, args...)
			require.Zero(t, result.ExitCode, "%s", result.Stderr)
		})
	}
}
func TestIntegrationLaunchdArchivedArgumentHandover(t *testing.T) {
	f := newLaunchdFixture(t)
	require.Zero(t, f.run(t, "--listen", "127.0.0.1:8321", "--allowed-host", "gateway.example").ExitCode)
	archived := filepath.Join(f.home, "archived.plist")
	before, err := os.ReadFile(f.plist)
	require.NoError(t, err)
	require.NoError(t, os.Rename(f.plist, archived))
	destination := filepath.Join(f.home, "migrated")
	result := f.run(t, "--from-plist", archived, "--binary", f.binary, "--data-dir", destination)
	require.Zero(t, result.ExitCode, "%s", result.Stderr)
	definition := readPlist(t, f.plist)
	var argv []string
	for _, arg := range definition.member(t, "ProgramArguments").Nodes {
		argv = append(argv, arg.Text)
	}
	require.Equal(t, []string{f.binary, "serve", "--data-dir", destination, "--listen", "127.0.0.1:8321", "--allowed-host", "gateway.example"}, argv)
	after, err := os.ReadFile(archived)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestIntegrationLaunchdInspectionBounds(t *testing.T) {
	for _, mode := range []string{"timeout", "output"} {
		t.Run(mode, func(t *testing.T) {
			f := newLaunchdFixture(t)
			if mode == "timeout" {
				f.tool(t, "launchctl", "sleep 10")
			} else {
				f.tool(t, "launchctl", `python3 -c 'print("x" * (2 << 20))'`)
			}
			result := f.run(t)
			require.NotZero(t, result.ExitCode)
			_, err := os.Lstat(f.plist)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestIntegrationLaunchdInstallerHelp(t *testing.T) {
	f := newLaunchdFixture(t)
	result := f.run(t, "--help")
	require.Zero(t, result.ExitCode)
	require.Contains(t, strings.ToLower(string(result.Stdout)), "without initializing or starting")
	_, err := os.Lstat(filepath.Join(f.home, "Library"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
