package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentNamespaceHasNoPrincipalAlias(t *testing.T) {
	root := newRootCmd()
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"principal", "list"})
	require.Error(t, root.ExecuteContext(t.Context()))
}

func TestAgentLanguagePreservesCLIGrammar(t *testing.T) {
	root := newRootCmd()
	for _, test := range []struct{ path, description string }{
		{"agent", "Manage agents"},
		{"agent list", "List agents"},
		{"agent create", "Create an agent"},
		{"agent update", "Update an agent"},
		{"http default", "Manage agent HTTP defaults"},
	} {
		command, rest, err := root.Find(strings.Fields(test.path))
		require.NoError(t, err)
		require.Empty(t, rest)
		assert.Equal(t, test.description, command.Short)
		assert.Equal(t, "agent-gateway "+test.path, command.CommandPath())
		assert.Empty(t, command.Aliases)
	}
	grant, _, err := root.Find([]string{"mcp", "grant", "create"})
	require.NoError(t, err)
	assert.NotNil(t, grant.Flags().Lookup("principal-id"))
	assert.Nil(t, grant.Flags().Lookup("agent-id"))
	assert.Contains(t, invocationHeaders(), "AGENT")
	assert.NotContains(t, invocationHeaders(), "PRINCIPAL")
}

func TestAgentLanguagePreservesProblemJSON(t *testing.T) {
	for _, test := range []struct{ title, human string }{
		{"The principal ID is invalid.", "The agent ID is invalid."},
		{"The current principal revision is required.", "The current agent revision is required."},
		{"The explicit principal ETag does not match the loaded principal.", "The explicit agent ETag does not match the loaded agent."},
		{"The agent update outcome is uncertain. Inspect agent get ID.", "The agent update outcome is uncertain. Inspect agent get ID."},
		{"Principal credential issue does not match the current credential slot state.", "Agent credential issue does not match the current credential slot state."},
		{"Usage: agent-gateway agent get ID", "Usage: agent-gateway agent get ID"},
		{"principal_default", "principal_default"},
		{"Principal investigator", "Principal investigator"},
	} {
		for _, mode := range []string{"human", "json"} {
			t.Run(test.title+"/"+mode, func(t *testing.T) {
				failure := controlclient.NewInputError(test.title)
				stderr := new(bytes.Buffer)
				command := &cobra.Command{Use: "test"}
				command.SetErr(stderr)
				assert.Same(t, failure, writeOnlineFailure(command, mode, failure))
				assert.Equal(t, test.title, failure.Title)
				if mode == "json" {
					expected, err := json.Marshal(failure)
					require.NoError(t, err)
					assert.Equal(t, string(expected)+"\n", stderr.String())
				} else {
					assert.Equal(t, test.human+"\n", stderr.String())
				}
			})
		}
	}
}
