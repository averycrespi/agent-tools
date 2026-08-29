package main

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			for _, flag := range append([]string{"address", "admin-bearer-file", "admin-bearer-stdin", "output"}, spec.Flags...) {
				assert.NotNil(t, command.Flags().Lookup(flag), "%s missing --%s", strings.Join(spec.Path, " "), flag)
			}
			assert.Nil(t, command.Flags().Lookup("data-dir"), strings.Join(spec.Path, " "))
		}
	})

	t.Run("online flags remain command scoped", func(t *testing.T) {
		assert.Nil(t, root.PersistentFlags().Lookup("address"))
		for name, expectedFlags := range map[string][]string{
			"initialize":  {"data-dir", "secret-output"},
			"admin-reset": {"data-dir", "secret-output"},
			"restore":     {"data-dir", "secret-output", "verify-current"},
			"serve":       {"data-dir", "listen"},
		} {
			command, _, err := root.Find([]string{name})
			require.NoError(t, err)
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
		command.SetArgs([]string{"grant", "list", "--output", "json"})
		err := command.ExecuteContext(context.Background())
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err))
		assert.Empty(t, stdout.String())
		assert.JSONEq(t, `{"status":null,"code":"client_invalid_input","title":"This online command is not implemented yet.","exit_code":2,"uncertain":false}`, stderr.String())

		assert.Equal(t, 1, commandExitCode(commandFailure{}))
		assert.Equal(t, 1, commandExitCode(assert.AnError))
	})
}
