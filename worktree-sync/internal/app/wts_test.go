package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
)

func TestWTSCommandSurface(t *testing.T) {
	root := app.NewWTS(nil)
	expected := map[string][]string{
		"config":   {"path", "edit", "validate", "refresh"},
		"repo":     {"add", "list", "remove"},
		"worktree": {"path", "create", "remove", "setup", "launch"},
		"daemon":   {"install", "uninstall", "start", "stop", "status"},
	}
	for parent, children := range expected {
		command, _, err := root.Find([]string{parent})
		require.NoError(t, err)
		for _, child := range children {
			found, _, findErr := command.Find([]string{child})
			require.NoError(t, findErr)
			require.Equal(t, child, found.Name())
		}
	}
	for _, command := range []string{"attach", "status", "reconcile", "cleanup"} {
		found, _, err := root.Find([]string{command})
		require.NoError(t, err)
		require.Equal(t, command, found.Name())
	}
	remove, _, err := root.Find([]string{"worktree", "rm"})
	require.NoError(t, err)
	require.Equal(t, "remove", remove.Name())
}

func TestRequiredRepoArgumentUsesCommandSpecificError(t *testing.T) {
	root := app.NewWTS(nil)
	root.SetArgs([]string{"repo", "remove"})
	root.SilenceUsage = true
	root.SilenceErrors = true
	err := root.Execute()
	require.ErrorContains(t, err, "repository ID")
	require.NotContains(t, err.Error(), "accepts")
}
