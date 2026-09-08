//go:build integration

package scripts

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

type plistNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr  `xml:",any,attr"`
	Text    string      `xml:",chardata"`
	Nodes   []plistNode `xml:",any"`
}

func (node *plistNode) member(t *testing.T, key string) *plistNode {
	t.Helper()
	for index := 0; index+1 < len(node.Nodes); index++ {
		if node.Nodes[index].XMLName.Local == "key" && node.Nodes[index].Text == key {
			return &node.Nodes[index+1]
		}
	}
	t.Fatalf("missing plist key %q", key)
	return nil
}

func readPlist(t *testing.T, path string) plistNode {
	t.Helper()
	var content []byte
	var err error
	if path == "-" {
		content, err = io.ReadAll(os.Stdin)
	} else {
		content, err = os.ReadFile(path)
	}
	require.NoError(t, err)
	var node plistNode
	require.NoError(t, xml.Unmarshal(content, &node))
	require.Equal(t, "plist", node.XMLName.Local)
	require.Len(t, node.Nodes, 1)
	require.Equal(t, "dict", node.Nodes[0].XMLName.Local)
	return node.Nodes[0]
}

// Linux exercises the script's actual filesystem effects with a closed plutil
// substitute. macOS exercises the same cases using the native plist utility.
func TestIntegrationLaunchdPlutilFixture(t *testing.T) {
	if os.Getenv("GATEWAY_PLUTIL_FIXTURE") != "1" {
		t.Skip("subprocess fixture only")
	}
	var args []string
	for index, arg := range os.Args {
		if arg == "--" {
			args = os.Args[index+1:]
			break
		}
	}
	require.NotEmpty(t, args)
	if args[0] == "-extract" {
		require.Equal(t, []string{"-extract", "dsAttrTypeStandard:NFSHomeDirectory.0", "raw", "-o", "-", "-"}, args)
		dict := readPlist(t, "-")
		fmt.Print(dict.member(t, "dsAttrTypeStandard:NFSHomeDirectory").Nodes[0].Text)
		os.Exit(0)
	}
	if args[0] == "-lint" {
		require.Len(t, args, 2)
		readPlist(t, args[1])
		return
	}
	require.Len(t, args, 5)
	require.Equal(t, "-replace", args[0])
	dict := readPlist(t, args[4])
	var target *plistNode
	switch args[1] {
	case "ProgramArguments":
		require.Equal(t, "-xml", args[2])
		var replacement plistNode
		require.NoError(t, xml.Unmarshal([]byte(args[3]), &replacement))
		require.Equal(t, "array", replacement.XMLName.Local)
		*dict.member(t, "ProgramArguments") = replacement
	case "ProgramArguments.0", "ProgramArguments.3":
		// A final numeric keypath inserts even with -replace on affected macOS
		// versions. Preserve that observed behavior rather than assuming a set.
		array := dict.member(t, "ProgramArguments")
		index := 0
		if args[1] == "ProgramArguments.3" {
			index = 3
		}
		array.Nodes = append(array.Nodes, plistNode{})
		copy(array.Nodes[index+1:], array.Nodes[index:])
		array.Nodes[index] = plistNode{XMLName: xml.Name{Local: "string"}}
		target = &array.Nodes[index]
	case "StandardOutPath", "StandardErrorPath":
		target = dict.member(t, args[1])
	default:
		t.Fatalf("unexpected replacement %q", args[1])
	}
	if args[1] != "ProgramArguments" {
		require.Equal(t, "-string", args[2])
		require.Equal(t, "string", target.XMLName.Local)
		target.Text = args[3]
	}
	root := plistNode{XMLName: xml.Name{Local: "plist"}, Nodes: []plistNode{dict}}
	content, err := xml.MarshalIndent(root, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(args[4], content, 0o600))
}

type launchdFixture struct {
	home   string
	binary string
	script string
	plist  string
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
	writeTool := func(name, body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700))
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", filepath.Join(root, "wrong-home"))
	t.Setenv("XDG_DATA_HOME", "")
	var escapedHome bytes.Buffer
	require.NoError(t, xml.EscapeText(&escapedHome, []byte(home)))
	t.Setenv("GATEWAY_TEST_ACCOUNT_PLIST", `<plist version="1.0"><dict><key>dsAttrTypeStandard:NFSHomeDirectory</key><array><string>`+escapedHome.String()+`</string></array></dict></plist>`)
	t.Setenv("GATEWAY_TEST_GOPATH", gopath)
	t.Setenv("GATEWAY_TEST_PLATFORM", "Darwin")
	t.Setenv("GATEWAY_TEST_FORBIDDEN", filepath.Join(root, "forbidden-execution"))
	writeTool("dscl", `printf '%s\n' "$GATEWAY_TEST_ACCOUNT_PLIST"`)
	writeTool("uname", `printf '%s\n' "$GATEWAY_TEST_PLATFORM"`)
	writeTool("go", `test "$1 $2" = 'env GOPATH' || exit 1; printf '%s\n' "$GATEWAY_TEST_GOPATH"`)
	writeTool("launchctl", `touch "$GATEWAY_TEST_FORBIDDEN"; exit 1`)
	if runtime.GOOS == "darwin" {
		writeTool("plutil", `exec /usr/bin/plutil "$@"`)
	} else {
		executable, err := os.Executable()
		require.NoError(t, err)
		t.Setenv("GATEWAY_TEST_EXECUTABLE", executable)
		writeTool("plutil", `GATEWAY_PLUTIL_FIXTURE=1 GORACE="${GORACE:-} atexit_sleep_ms=0" exec "$GATEWAY_TEST_EXECUTABLE" -test.run=^TestIntegrationLaunchdPlutilFixture$ -- "$@"`)
	}
	binary := filepath.Join(gopath, "bin", "mcp-gateway")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\ntouch \"$GATEWAY_TEST_FORBIDDEN\"\nexit 1\n"), 0o700))
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	fixture := launchdFixture{
		home: home, binary: binary,
		script: filepath.Join(filepath.Dir(source), "install-launchd-agent.sh"),
		plist:  filepath.Join(home, "Library", "LaunchAgents", "dev.agent-tools.mcp-gateway.plist"),
	}
	t.Cleanup(func() {
		for _, path := range []string{os.Getenv("GATEWAY_TEST_FORBIDDEN"), filepath.Join(home, ".local"), os.Getenv("HOME")} {
			_, err := os.Lstat(path)
			require.True(t, os.IsNotExist(err), "unexpected side effect at %s: %v", path, err)
		}
	})
	return fixture
}

func (fixture launchdFixture) run(t *testing.T, args ...string) testutil.ProcessResult {
	t.Helper()
	runner, err := testutil.NewBinaryRunner(30*time.Second, 64<<10)
	require.NoError(t, err)
	result, runErr := runner.Run(t.Context(), "/bin/bash", append([]string{fixture.script}, args...)...)
	if result.ExitCode == 0 {
		require.NoError(t, runErr)
	}
	require.GreaterOrEqual(t, result.ExitCode, 0, "subprocess failed: %v", runErr)
	require.False(t, result.StdoutTruncated)
	require.False(t, result.StderrTruncated)
	return result
}

func TestIntegrationLaunchdInstaller(t *testing.T) {
	for _, mode := range []string{"default", "xdg", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newLaunchdFixture(t)
			data := filepath.Join(fixture.home, ".local", "share", "mcp-gateway")
			binary := fixture.binary
			var args []string
			switch mode {
			case "xdg":
				xdg := filepath.Join(fixture.home, "XDG & <data>")
				t.Setenv("XDG_DATA_HOME", xdg+"/")
				data = filepath.Join(xdg, "mcp-gateway")
			case "explicit":
				t.Setenv("XDG_DATA_HOME", "relative-ignored")
				t.Setenv("GATEWAY_TEST_GOPATH", "invalid-ignored")
				data = filepath.Join(fixture.home, "custom & <data>")
				binary = filepath.Join(fixture.home, "custom & <gateway> \"quoted\" \\path")
				content, err := os.ReadFile(fixture.binary)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(binary, content, 0o700))
				args = []string{"--binary", binary, "--data-dir", data}
			}
			result := fixture.run(t, args...)
			require.Equal(t, 0, result.ExitCode, "%s", result.Stderr)
			require.Contains(t, string(result.Stdout), "Data directory: "+data)
			dict := readPlist(t, fixture.plist)
			require.Equal(t, "dev.agent-tools.mcp-gateway", dict.member(t, "Label").Text)
			var actualArgs []string
			for _, arg := range dict.member(t, "ProgramArguments").Nodes {
				actualArgs = append(actualArgs, arg.Text)
			}
			require.Equal(t, []string{binary, "serve", "--data-dir", data, "--listen", "127.0.0.1:8210"}, actualArgs)
			require.Equal(t, "true", dict.member(t, "RunAtLoad").XMLName.Local)
			require.Equal(t, "true", dict.member(t, "KeepAlive").XMLName.Local)
			require.Equal(t, "30", dict.member(t, "ExitTimeOut").Text)
			require.Equal(t, "/usr/bin:/bin:/usr/sbin:/sbin", dict.member(t, "EnvironmentVariables").member(t, "PATH").Text)
			logs := filepath.Join(fixture.home, "Library", "Logs", "mcp-gateway")
			require.Equal(t, filepath.Join(logs, "stdout.log"), dict.member(t, "StandardOutPath").Text)
			require.Equal(t, filepath.Join(logs, "stderr.log"), dict.member(t, "StandardErrorPath").Text)
			for path, mode := range map[string]os.FileMode{fixture.plist: 0o600, logs: 0o700, filepath.Join(logs, "stdout.log"): 0o600, filepath.Join(logs, "stderr.log"): 0o600} {
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.Equal(t, mode, info.Mode().Perm(), path)
			}
			_, err := os.Lstat(data)
			require.True(t, os.IsNotExist(err))
			before, err := os.ReadFile(fixture.plist)
			require.NoError(t, err)
			result = fixture.run(t, args...)
			require.NotZero(t, result.ExitCode)
			require.Contains(t, string(result.Stderr), "Plist already exists")
			after, err := os.ReadFile(fixture.plist)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestIntegrationLaunchdInstallerRefusals(t *testing.T) {
	for _, name := range []string{"relative-data", "relative-binary", "missing-value", "unknown-argument", "relative-xdg", "missing-binary", "multiple-gopath", "non-macos", "symlink-plist", "public-logs", "symlink-logs", "public-log-file", "invalid-template"} {
		t.Run(name, func(t *testing.T) {
			fixture := newLaunchdFixture(t)
			var args []string
			var expected string
			logs := filepath.Join(fixture.home, "Library", "Logs", "mcp-gateway")
			switch name {
			case "relative-data", "relative-binary":
				flag := "--data-dir"
				if name == "relative-binary" {
					flag = "--binary"
				}
				args, expected = []string{flag, "relative"}, "must be an absolute path"
			case "missing-value":
				args, expected = []string{"--data-dir"}, "Missing path"
			case "unknown-argument":
				args, expected = []string{"--force"}, "Unknown argument"
			case "relative-xdg":
				t.Setenv("XDG_DATA_HOME", "relative")
				expected = "XDG_DATA_HOME must be an absolute path"
			case "missing-binary":
				args, expected = []string{"--binary", filepath.Join(fixture.home, "missing")}, "Gateway executable not found"
			case "multiple-gopath":
				t.Setenv("GATEWAY_TEST_GOPATH", "/first:/second")
				expected = "Multiple GOPATH entries"
			case "non-macos":
				t.Setenv("GATEWAY_TEST_PLATFORM", "Linux")
				expected = "requires macOS"
			case "symlink-plist":
				require.NoError(t, os.MkdirAll(filepath.Dir(fixture.plist), 0o700))
				require.NoError(t, os.Symlink(filepath.Join(fixture.home, "missing"), fixture.plist))
				expected = "Plist already exists"
			case "public-logs", "symlink-logs", "public-log-file":
				require.NoError(t, os.MkdirAll(filepath.Dir(logs), 0o700))
				if name == "symlink-logs" {
					require.NoError(t, os.Symlink(fixture.home, logs))
					expected = "Refusing symlink directory"
				} else {
					require.NoError(t, os.Mkdir(logs, 0o700))
					if name == "public-logs" {
						require.NoError(t, os.Chmod(logs, 0o755))
						expected = "Log directory must have mode 0700"
					} else {
						log := filepath.Join(logs, "stdout.log")
						require.NoError(t, os.WriteFile(log, []byte("preserve existing log"), 0o600))
						require.NoError(t, os.Chmod(log, 0o644))
						expected = "Log must have mode 0600"
					}
				}
			case "invalid-template":
				module := filepath.Join(t.TempDir(), "copy")
				require.NoError(t, os.MkdirAll(filepath.Join(module, "scripts"), 0o700))
				require.NoError(t, os.MkdirAll(filepath.Join(module, "examples", "launchd"), 0o700))
				content, err := os.ReadFile(fixture.script)
				require.NoError(t, err)
				fixture.script = filepath.Join(module, "scripts", "install-launchd-agent.sh")
				require.NoError(t, os.WriteFile(fixture.script, content, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(module, "examples", "launchd", "mcp-gateway.plist"), []byte("not XML"), 0o600))
			}
			result := fixture.run(t, args...)
			require.NotZero(t, result.ExitCode)
			if expected != "" {
				require.Contains(t, string(result.Stderr), expected)
			}
			if name == "symlink-plist" {
				target, err := os.Readlink(fixture.plist)
				require.NoError(t, err)
				require.Equal(t, filepath.Join(fixture.home, "missing"), target)
			} else {
				_, err := os.Lstat(fixture.plist)
				require.True(t, os.IsNotExist(err))
			}
			staged, err := filepath.Glob(filepath.Join(filepath.Dir(fixture.plist), ".mcp-gateway.*"))
			require.NoError(t, err)
			require.Empty(t, staged)
			if name == "public-log-file" {
				content, err := os.ReadFile(filepath.Join(logs, "stdout.log"))
				require.NoError(t, err)
				require.Equal(t, "preserve existing log", string(content))
			}
		})
	}
}

func TestIntegrationLaunchdInstallerHelp(t *testing.T) {
	fixture := newLaunchdFixture(t)
	t.Setenv("GATEWAY_TEST_PLATFORM", "Linux")
	result := fixture.run(t, "--help")
	require.Zero(t, result.ExitCode)
	require.Contains(t, string(result.Stdout), "without initializing or starting")
	_, err := os.Lstat(filepath.Join(fixture.home, "Library"))
	require.True(t, os.IsNotExist(err))
}
