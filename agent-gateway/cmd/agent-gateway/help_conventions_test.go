package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestCommandDescriptionsFollowConventions(t *testing.T) {
	root := newRootCmd()
	rootDescriptions := map[string]string{
		"admin": "Manage administrator credentials", "agent": "Manage agents", "audit": "View audit history",
		"backup": "Manage backups", "completion": "Generate shell completion scripts", "doctor": "Check setup and diagnose problems",
		"help": "Show command help", "http": "Manage HTTP access", "init": "Initialize or complete local setup",
		"maintenance": "Inspect and recover stopped installations", "mcp": "Manage MCP servers, tools, and access",
		"serve": "Run Gateway in the foreground", "service": "Manage the macOS background service",
	}
	for _, command := range root.Commands() {
		require.Equal(t, rootDescriptions[command.Name()], command.Short, command.CommandPath())
	}
	verbs := map[string]bool{}
	for _, verb := range strings.Fields("Manage View Generate Check Show Initialize Inspect Run List Get Create Update Delete Export Preview Rotate Revoke Start Replace Cancel Issue Approve Reject Verify Reset Restore Migrate Install Stop Restart Uninstall") {
		verbs[verb] = true
	}
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		require.NotEmpty(t, command.Short, command.CommandPath())
		require.Equal(t, strings.TrimSpace(command.Short), command.Short)
		require.LessOrEqual(t, utf8.RuneCountInString(command.Short), 80, command.CommandPath())
		require.NotContains(t, command.Short, "\n", command.CommandPath())
		require.False(t, strings.HasSuffix(command.Short, "."), command.CommandPath())
		require.True(t, verbs[strings.Fields(command.Short)[0]], command.CommandPath())
		for _, term := range []string{"authority", "ETag", "canonical", "retained evidence"} {
			require.NotContains(t, command.Short, term, command.CommandPath())
		}
		if command.Name() == "get" || command.Name() == "list" || command.Name() == "update" {
			require.True(t, strings.HasPrefix(strings.ToLower(command.Short), command.Name()+" "), command.CommandPath())
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)
}
