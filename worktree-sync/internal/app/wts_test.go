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

func TestEveryCommandUsesSpecificArgumentValidation(t *testing.T) {
	tests := [][]string{
		{"config", "path", "extra"}, {"config", "edit", "extra"}, {"config", "validate", "extra"}, {"config", "refresh", "extra"},
		{"repo", "add", "one", "two"}, {"repo", "list", "extra"}, {"repo", "remove"},
		{"worktree", "path"}, {"worktree", "create"}, {"worktree", "remove"}, {"worktree", "setup"}, {"worktree", "launch"},
		{"attach", "one", "two"}, {"status", "one", "two"}, {"reconcile", "one", "two"}, {"cleanup", "extra"},
		{"daemon", "install", "extra"}, {"daemon", "uninstall", "extra"}, {"daemon", "start", "extra"}, {"daemon", "stop", "extra"}, {"daemon", "status", "extra"},
	}
	for _, args := range tests {
		root := app.NewWTS(nil)
		root.SetArgs(args)
		root.SilenceUsage = true
		root.SilenceErrors = true
		err := root.Execute()
		require.Error(t, err, args)
		require.NotRegexp(t, `accepts \d+ arg`, err.Error(), args)
	}
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
