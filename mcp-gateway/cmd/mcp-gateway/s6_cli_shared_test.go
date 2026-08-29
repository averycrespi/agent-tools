package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
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
	assert.Equal(t, "sha256:4311b674d079a556f598351af843417591bcc3f20f95417c31b0a27fc09fa753", digest)
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
			expectedFlags := append([]string{"address", "admin-bearer-file", "admin-bearer-stdin", "output"}, spec.Flags...)
			actualFlags := make([]string, 0, len(expectedFlags))
			command.Flags().VisitAll(func(flag *pflag.Flag) { actualFlags = append(actualFlags, flag.Name) })
			sort.Strings(expectedFlags)
			sort.Strings(actualFlags)
			assert.Equal(t, expectedFlags, actualFlags, strings.Join(spec.Path, " "))
		}
	})

	t.Run("online flags remain command scoped", func(t *testing.T) {
		assert.Nil(t, root.PersistentFlags().Lookup("address"))
		for name, expectedFlags := range map[string][]string{
			"initialize":  {"data-dir", "json", "output", "secret-output"},
			"admin-reset": {"data-dir", "json", "output", "secret-output"},
			"restore":     {"data-dir", "json", "output", "secret-output", "verify-current"},
			"serve":       {"data-dir", "listen"},
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
		command.SetArgs([]string{"status", "--address", "not-a-loopback-url", "--output", "json"})
		err := command.ExecuteContext(context.Background())
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err))
		assert.Empty(t, stdout.String())
		assert.JSONEq(t, `{"status":null,"code":"client_bearer_missing","title":"The selected administrator bearer does not exist. Run mcp-gateway initialize or select an existing owner-only bearer file.","exit_code":2,"uncertain":false}`, stderr.String())

		assert.Equal(t, 1, commandExitCode(commandFailure{}))
		assert.Equal(t, 1, commandExitCode(assert.AnError))
	})
}
