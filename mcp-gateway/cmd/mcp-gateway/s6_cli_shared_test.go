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
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6DocumentationDrift(t *testing.T) {
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
	assert.Equal(t, "sha256:73dfe4ed9f3644ca9d1108e5b9aa9289dafdb80098c6546125afbc7ce595d7fc", digest)
}

func TestS6CLISharedContract(t *testing.T) {
	root := newRootCmd()

	t.Run("frozen capability uses match Cobra leaves", func(t *testing.T) {
		expected := make([]string, 0)
		for _, row := range contract.S6CapabilityManifest() {
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
		for name, expectedFlags := range map[string][]string{
			"initialize":  {"data-dir", "json", "output", "secret-output"},
			"admin-reset": {"data-dir", "json", "output", "secret-output"},
			"restore":     {"data-dir", "json", "output", "secret-output", "verify-current"},
			"serve":       {"data-dir", "json", "listen", "output"},
		} {
			command, _, err := root.Find([]string{name})
			require.NoError(t, err)
			expectedUse := name
			if name == "restore" {
				expectedUse = "restore [backup-id]"
			}
			assert.Equal(t, expectedUse, command.Use, name)
			flags := make([]string, 0)
			command.Flags().VisitAll(func(flag *pflag.Flag) { flags = append(flags, flag.Name) })
			sort.Strings(flags)
			sort.Strings(expectedFlags)
			assert.Equal(t, expectedFlags, flags, name)
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
		assert.Equal(t, "client_bearer_missing", problem.Code)
		assert.Contains(t, problem.Title, filepath.Join(dataDir, "admin-bearer"))

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
	assert.Equal(t, 1, pathResolutionCalls)
	assert.Greater(t, publicClientCalls, 0, "online operations must remain on the hardened public HTTP client")
}
