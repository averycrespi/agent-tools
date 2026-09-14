package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPGrantNamespaceCompletion(t *testing.T) {
	complete := func(args ...string) []string {
		t.Helper()
		root := newRootCmd()
		var output, stderr bytes.Buffer
		root.SetOut(&output)
		root.SetErr(&stderr)
		root.SetArgs(append([]string{"__complete"}, args...))
		require.NoError(t, root.Execute())
		var names []string
		for _, line := range strings.Split(output.String(), "\n") {
			name, _, _ := strings.Cut(line, "\t")
			if name != "" && !strings.HasPrefix(name, ":") {
				names = append(names, name)
			}
		}
		return names
	}
	root := complete("")
	require.Contains(t, root, "principal")
	require.NotContains(t, root, "grant")
	require.NotContains(t, root, "grant-request")
	require.NotContains(t, root, "invocation")
	mcp := complete("mcp", "")
	require.Contains(t, mcp, "grant")
	require.Contains(t, mcp, "grant-request")
	require.Contains(t, mcp, "invocation")
	require.ElementsMatch(t, []string{"get", "list"}, complete("mcp", "invocation", ""))
	require.ElementsMatch(t, []string{"create", "delete", "get", "list", "update"}, complete("mcp", "grant", ""))
	require.ElementsMatch(t, []string{"approve", "get", "list", "reject"}, complete("mcp", "grant-request", ""))
}
