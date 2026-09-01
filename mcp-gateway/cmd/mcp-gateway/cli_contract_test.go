package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCLIDocumentationDrift(t *testing.T) {
	root := newRootCmd()
	var snapshot strings.Builder
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command.Hidden {
			return
		}
		fmt.Fprintf(&snapshot, "command=%s\nuse=%s\nshort=%s\n", command.CommandPath(), command.Use, command.Short)
		command.Flags().VisitAll(func(flag *pflag.Flag) {
			fmt.Fprintf(&snapshot, "flag=%s|%s|%s|%s\n", flag.Name, flag.Value.Type(), flag.DefValue, flag.Usage)
		})
		children := append([]*cobra.Command(nil), command.Commands()...)
		sort.Slice(children, func(left, right int) bool { return children[left].Name() < children[right].Name() })
		for _, child := range children {
			walk(child)
		}
	}
	walk(root)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(snapshot.String())))
	assert.Equal(t, "sha256:530566eb57d8196134d6afc70572103f73d544ac1d4235d0151b8d1836156a17", digest)
}

func testCLIGuideGeneratedHelpAndDefaultDrift(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	guides := map[string]string{}
	for _, path := range []string{"docs/cli-local-administration.md", "docs/server-configuration.md", "docs/access-policy.md", "docs/invocation-evidence.md", "docs/recovery.md"} {
		contents, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(path)))
		require.NoError(t, err)
		guides[path] = string(contents)
	}
	for _, family := range contract.DocumentationCommandManifest() {
		guide, owned := guides[family.CanonicalOwner]
		if owned {
			assert.Contains(t, guide, "`"+family.HelpInvocation+"`", family.ID)
		}
	}

	root := newRootCmd()
	assert.Equal(t, "", root.PersistentFlags().Lookup("data-dir").DefValue)
	serve, _, err := root.Find([]string{"serve"})
	require.NoError(t, err)
	assert.Equal(t, contract.DefaultAuthority, serve.Flags().Lookup("listen").DefValue)
	assert.Equal(t, "human", serve.Flags().Lookup("output").DefValue)
	status, _, err := root.Find([]string{"status"})
	require.NoError(t, err)
	assert.Equal(t, controlclient.DefaultAddress, status.Flags().Lookup("address").DefValue)
	assert.Equal(t, "human", status.Flags().Lookup("output").DefValue)
	assert.Contains(t, guides["docs/cli-local-administration.md"], contract.CanonicalOrigin)
}

func TestCLIContract(t *testing.T) {
	t.Run("documentation snapshot", testCLIDocumentationDrift)
	t.Run("guide help projection", testCLIGuideGeneratedHelpAndDefaultDrift)
	t.Run("documentation command ownership", testDocumentationCommandHelpProjection)
	t.Run("command errors", testCLICommandErrors)
	root := newRootCmd()

	t.Run("frozen capability uses match Cobra leaves", func(t *testing.T) {
		expected := make([]string, 0)
		for _, row := range contract.ControlPlaneCapabilityManifest() {
			expected = append(expected, row.CLIUses...)
		}
		sort.Strings(expected)
		actual := onlineCapabilityUses()
		sort.Strings(actual)
		assert.Equal(t, expected, actual)

		for _, spec := range onlineCommandSpecs() {
			command, _, err := root.Find(spec.Path)
			require.NoError(t, err, strings.Join(spec.Path, " "))
			assert.Equal(t, spec.Use, command.Use, strings.Join(spec.Path, " "))
			expectedFlags := append([]string{"address", "admin-bearer-file", "admin-bearer-stdin", "json", "output"}, spec.Flags...)
			actualFlags := make([]string, 0, len(expectedFlags))
			command.Flags().VisitAll(func(flag *pflag.Flag) { actualFlags = append(actualFlags, flag.Name) })
			sort.Strings(expectedFlags)
			sort.Strings(actualFlags)
			assert.Equal(t, expectedFlags, actualFlags, strings.Join(spec.Path, " "))
		}
	})

	t.Run("online flags remain command scoped", func(t *testing.T) {
		assert.Nil(t, root.PersistentFlags().Lookup("address"))
		assert.NotNil(t, root.PersistentFlags().Lookup("data-dir"))
		cases := []struct {
			path  []string
			use   string
			flags []string
		}{
			{path: []string{"initialize"}, use: "initialize", flags: []string{"data-dir", "json", "output", "secret-output"}},
			{path: []string{"admin", "reset"}, use: "reset", flags: []string{"data-dir", "json", "output", "secret-output"}},
			{path: []string{"restore"}, use: "restore [backup-id]", flags: []string{"data-dir", "json", "output", "secret-output", "verify-current"}},
			{path: []string{"serve"}, use: "serve", flags: []string{"data-dir", "json", "listen", "output"}},
		}
		for _, test := range cases {
			name := strings.Join(test.path, " ")
			command, _, err := root.Find(test.path)
			require.NoError(t, err)
			assert.Equal(t, test.use, command.Use, name)
			flags := make([]string, 0)
			command.Flags().VisitAll(func(flag *pflag.Flag) { flags = append(flags, flag.Name) })
			sort.Strings(flags)
			sort.Strings(test.flags)
			assert.Equal(t, test.flags, flags, name)
		}
	})

	t.Run("online failures use stderr and typed exits", func(t *testing.T) {
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		command := newRootCmd()
		command.SetOut(stdout)
		command.SetErr(stderr)
		dataDir := filepath.Join(t.TempDir(), "missing")
		command.SetArgs([]string{"--data-dir", dataDir, "status", "--address", "not-a-loopback-url", "--output", "json"})
		err := command.ExecuteContext(context.Background())
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err))
		assert.Empty(t, stdout.String())
		var problem struct {
			Code  string `json:"code"`
			Title string `json:"title"`
		}
		require.NoError(t, json.Unmarshal(stderr.Bytes(), &problem))
		assert.Equal(t, "client_invalid_input", problem.Code)
		assert.Equal(t, "The Gateway address is invalid.", problem.Title)

		assert.Equal(t, 1, commandExitCode(commandFailure{}))
		assert.Equal(t, 1, commandExitCode(assert.AnError))
	})
}

func TestCLIControlBoundary(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	files, err := filepath.Glob(filepath.Join(filepath.Dir(current), "online*.go"))
	require.NoError(t, err)

	acquisitionCalls := 0
	pathResolutionCalls := 0
	publicClientCalls := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		contents, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		source := string(contents)
		calls := strings.Count(source, "controlclient.AcquireAdminBearer(")
		if calls > 0 {
			assert.Equal(t, "online.go", filepath.Base(path), "bearer acquisition must have one command-layer owner")
		}
		acquisitionCalls += calls
		pathResolutionCalls += strings.Count(source, "gatewaypaths.Resolve(")
		publicClientCalls += strings.Count(source, "controlclient.New(")
	}
	assert.Equal(t, 1, acquisitionCalls)
	assert.Equal(t, 2, pathResolutionCalls)
	assert.Greater(t, publicClientCalls, 0, "online operations must remain on the hardened public HTTP client")
}
