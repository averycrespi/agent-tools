package cmd_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/cmd"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
)

type partialController struct{}

func (partialController) Execute(context.Context, app.Request) (string, error) {
	return "/created/path", errors.New("reconciliation degraded")
}

func TestCommandPrintsPartialSuccessWithoutUsageBeforeReturningError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := cmd.ExecuteWTS(context.Background(), partialController{}, &stdout, &stderr, []string{"reconcile"})
	require.ErrorContains(t, err, "degraded")
	require.Contains(t, stdout.String(), "/created/path")
	require.NotContains(t, stdout.String(), "Usage:")
	require.NotContains(t, stderr.String(), "Usage:")
}

func TestWTSCommandSurface(t *testing.T) {
	root := cmd.NewWTS(nil)
	expected := map[string][]string{
		"config":   {"path", "edit", "validate", "refresh"},
		"repo":     {"add", "list", "remove"},
		"worktree": {"path", "create", "remove", "setup", "launch"},
		"daemon":   {"install", "uninstall", "start", "stop", "status", "logs"},
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

func TestEveryUserCommandHasDescription(t *testing.T) {
	root := cmd.NewWTS(nil)
	paths := [][]string{
		{"attach"}, {"cleanup"}, {"config"}, {"daemon"}, {"reconcile"}, {"repo"}, {"status"}, {"worktree"},
		{"config", "path"}, {"config", "edit"}, {"config", "validate"}, {"config", "refresh"},
		{"repo", "add"}, {"repo", "list"}, {"repo", "remove"},
		{"worktree", "path"}, {"worktree", "create"}, {"worktree", "remove"}, {"worktree", "setup"}, {"worktree", "launch"},
		{"daemon", "install"}, {"daemon", "uninstall"}, {"daemon", "start"}, {"daemon", "stop"}, {"daemon", "status"}, {"daemon", "logs"},
	}
	for _, path := range paths {
		command, _, err := root.Find(path)
		require.NoError(t, err)
		require.NotEmpty(t, command.Short, path)
	}
}

func TestEveryCommandUsesSpecificArgumentValidation(t *testing.T) {
	tests := [][]string{
		{"config", "path", "extra"}, {"config", "edit", "extra"}, {"config", "validate", "extra"}, {"config", "refresh", "extra"},
		{"repo", "add", "one", "two"}, {"repo", "list", "extra"}, {"repo", "remove"},
		{"worktree", "path"}, {"worktree", "create"}, {"worktree", "remove"}, {"worktree", "setup"}, {"worktree", "launch"},
		{"attach", "one", "two"}, {"status", "one", "two"}, {"reconcile", "one", "two"}, {"cleanup", "extra"},
		{"daemon", "install", "extra"}, {"daemon", "uninstall", "extra"}, {"daemon", "start", "extra"}, {"daemon", "stop", "extra"}, {"daemon", "status", "extra"}, {"daemon", "logs", "extra"},
	}
	for _, args := range tests {
		root := cmd.NewWTS(nil)
		root.SetArgs(args)
		root.SilenceUsage = true
		root.SilenceErrors = true
		err := root.Execute()
		require.Error(t, err, args)
		require.NotRegexp(t, `accepts \d+ arg`, err.Error(), args)
	}
}

type recordingController struct{ request app.Request }

func (c *recordingController) Execute(_ context.Context, request app.Request) (string, error) {
	c.request = request
	return "", nil
}

func TestDaemonLogsForwardsHistoryAndFollowOptions(t *testing.T) {
	controller := &recordingController{}
	err := cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, []string{"daemon", "logs", "--lines", "25", "--follow"})
	require.NoError(t, err)
	require.Equal(t, "daemon.logs", controller.request.Action)
	require.Equal(t, 25, controller.request.Options["lines"])
	require.Equal(t, true, controller.request.Options["follow"])
}

func TestRequiredRepoArgumentUsesCommandSpecificError(t *testing.T) {
	root := cmd.NewWTS(nil)
	root.SetArgs([]string{"repo", "remove"})
	root.SilenceUsage = true
	root.SilenceErrors = true
	err := root.Execute()
	require.ErrorContains(t, err, "repository ID")
	require.NotContains(t, err.Error(), "accepts")
}
