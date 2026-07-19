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
		"repo":     {"add", "list", "remove", "roots"},
		"worktree": {"path", "create", "remove", "setup", "launch"},
		"daemon":   {"install", "uninstall", "start", "stop", "restart", "status", "logs"},
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
	for _, command := range []string{"attach", "status", "reconcile", "cleanup", "doctor"} {
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
		{"attach"}, {"cleanup"}, {"config"}, {"daemon"}, {"doctor"}, {"reconcile"}, {"repo"}, {"status"}, {"worktree"},
		{"config", "path"}, {"config", "edit"}, {"config", "validate"}, {"config", "refresh"},
		{"repo", "add"}, {"repo", "list"}, {"repo", "remove"}, {"repo", "roots"},
		{"repo", "roots", "show"}, {"repo", "roots", "set-creation"}, {"repo", "roots", "add-allowed"}, {"repo", "roots", "remove-allowed"},
		{"worktree", "path"}, {"worktree", "create"}, {"worktree", "remove"}, {"worktree", "setup"}, {"worktree", "launch"},
		{"daemon", "install"}, {"daemon", "uninstall"}, {"daemon", "start"}, {"daemon", "stop"}, {"daemon", "restart"}, {"daemon", "status"}, {"daemon", "logs"},
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
		{"repo", "roots", "show", "extra"}, {"repo", "roots", "set-creation"}, {"repo", "roots", "add-allowed"}, {"repo", "roots", "remove-allowed"},
		{"worktree", "path"}, {"worktree", "path", "branch", "repo"},
		{"worktree", "create"}, {"worktree", "create", "branch", "repo"},
		{"worktree", "remove"}, {"worktree", "remove", "branch", "repo"},
		{"worktree", "setup"}, {"worktree", "setup", "branch", "repo"},
		{"worktree", "launch"}, {"worktree", "launch", "branch", "repo"},
		{"attach", "repo"}, {"status", "repo"}, {"reconcile", "repo"}, {"cleanup", "extra"}, {"doctor", "extra"},
		{"daemon", "install", "extra"}, {"daemon", "uninstall", "extra"}, {"daemon", "start", "extra"}, {"daemon", "stop", "extra"}, {"daemon", "restart", "extra"}, {"daemon", "status", "extra"}, {"daemon", "logs", "extra"},
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

func TestOptionalRepositorySelectorsUseRepoIDFlag(t *testing.T) {
	tests := []struct {
		args   []string
		action string
	}{
		{[]string{"worktree", "path", "branch", "--repo-id", "repo"}, "worktree.path"},
		{[]string{"worktree", "create", "branch", "--repo-id", "repo"}, "worktree.create"},
		{[]string{"worktree", "remove", "branch", "--repo-id", "repo"}, "worktree.remove"},
		{[]string{"worktree", "setup", "branch", "--repo-id", "repo"}, "worktree.setup"},
		{[]string{"worktree", "launch", "branch", "--repo-id", "repo"}, "worktree.launch"},
		{[]string{"attach", "--repo-id", "repo"}, "attach"},
		{[]string{"status", "--repo-id", "repo"}, "status"},
		{[]string{"reconcile", "--repo-id", "repo"}, "reconcile"},
	}
	for _, tt := range tests {
		controller := &recordingController{}
		err := cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, tt.args)
		require.NoError(t, err, tt.args)
		require.Equal(t, tt.action, controller.request.Action, tt.args)
		require.Equal(t, "repo", controller.request.Options["repo_id"], tt.args)
	}
}

func TestWorktreeLaunchHelpExplainsManagedWindowBehavior(t *testing.T) {
	root := cmd.NewWTS(nil)
	launch, _, err := root.Find([]string{"worktree", "launch"})
	require.NoError(t, err)
	require.Contains(t, launch.Long, "existing managed tmux window")
	require.Contains(t, launch.Long, "does not create a window or attach")
	require.Contains(t, launch.Example, "--rerun")
}

func TestRepoAddForwardsCreationAndAllowedRoots(t *testing.T) {
	controller := &recordingController{}
	err := cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, []string{"repo", "add", "--worktree-root", "/managed", "--allowed-worktree-root", "/external-one", "--allowed-worktree-root", "/external-two"})
	require.NoError(t, err)
	require.Equal(t, "/managed", controller.request.Options["root"])
	require.Equal(t, []string{"/external-one", "/external-two"}, controller.request.Options["roots"])

	controller = &recordingController{}
	err = cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, []string{"repo", "add", "--no-default-allowed-roots"})
	require.NoError(t, err)
	require.Equal(t, true, controller.request.Options["no_default_allowed_roots"])
}

func TestRepoAddRejectsDefaultAllowedRootOptOutWithExplicitRoots(t *testing.T) {
	err := cmd.ExecuteWTS(context.Background(), &recordingController{}, &bytes.Buffer{}, &bytes.Buffer{}, []string{"repo", "add", "--no-default-allowed-roots", "--allowed-worktree-root", "/external"})
	require.ErrorContains(t, err, "choose only one")
}

func TestRepoRootsCommandsForwardRepositoryAndPath(t *testing.T) {
	for command, action := range map[string]string{"set-creation": "repo.roots.set-creation", "add-allowed": "repo.roots.add-allowed", "remove-allowed": "repo.roots.remove-allowed"} {
		controller := &recordingController{}
		err := cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, []string{"repo", "roots", command, "/root", "--repo-id", "api"})
		require.NoError(t, err)
		require.Equal(t, action, controller.request.Action)
		require.Equal(t, "api", controller.request.Options["repo_id"])
		require.Equal(t, []string{"/root"}, controller.request.Args)
	}
	controller := &recordingController{}
	err := cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, []string{"repo", "roots", "show", "--repo-id", "api"})
	require.NoError(t, err)
	require.Equal(t, "repo.roots.show", controller.request.Action)
}

func TestWorktreeCreateForwardsBranchOrigin(t *testing.T) {
	controller := &recordingController{}
	err := cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, []string{"worktree", "create", "feature", "--from", "origin/main"})
	require.NoError(t, err)
	require.Equal(t, "origin/main", controller.request.Options["from"])
}

func TestReconcileDryRunHelpAndForwarding(t *testing.T) {
	root := cmd.NewWTS(nil)
	reconcileCommand, _, err := root.Find([]string{"reconcile"})
	require.NoError(t, err)
	require.Contains(t, reconcileCommand.Long, "respawn-window -k")
	require.Contains(t, reconcileCommand.Long, "terminate")
	require.Contains(t, reconcileCommand.Long, "does not reserve")

	controller := &recordingController{}
	err = cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, []string{"reconcile", "--repo-id", "repo", "--dry-run"})
	require.NoError(t, err)
	require.Equal(t, "reconcile", controller.request.Action)
	require.Equal(t, "repo", controller.request.Options["repo_id"])
	require.Equal(t, true, controller.request.Options["dry_run"])
}

func TestStatusHelpExplainsProgressiveAndCheckOutput(t *testing.T) {
	root := cmd.NewWTS(nil)
	status, _, err := root.Find([]string{"status"})
	require.NoError(t, err)
	require.Contains(t, status.Long, "attention")
	require.Contains(t, status.Long, "--check")
	require.Contains(t, status.Example, "--verbose")
	require.Contains(t, status.Example, "--json --check")
}

func TestStatusForwardsVerboseAndCheckAndRejectsVerboseJSON(t *testing.T) {
	controller := &recordingController{}
	err := cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, []string{"status", "--verbose", "--check"})
	require.NoError(t, err)
	require.Equal(t, true, controller.request.Options["verbose"])
	require.Equal(t, true, controller.request.Options["check"])

	err = cmd.ExecuteWTS(context.Background(), &recordingController{}, &bytes.Buffer{}, &bytes.Buffer{}, []string{"status", "--verbose", "--json"})
	require.ErrorContains(t, err, "choose only one")
}

func TestStatusAndReconcileSupportExplicitAll(t *testing.T) {
	for _, action := range []string{"status", "reconcile"} {
		controller := &recordingController{}
		err := cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, []string{action, "--all"})
		require.NoError(t, err)
		require.Equal(t, true, controller.request.Options["all"])
	}
}

func TestRepoIDAndAllAreMutuallyExclusive(t *testing.T) {
	for _, action := range []string{"status", "reconcile"} {
		err := cmd.ExecuteWTS(context.Background(), &recordingController{}, &bytes.Buffer{}, &bytes.Buffer{}, []string{action, "--repo-id", "repo", "--all"})
		require.ErrorContains(t, err, "choose only one")
	}
}

func TestDoctorForwardsJSONOutput(t *testing.T) {
	controller := &recordingController{}
	err := cmd.ExecuteWTS(context.Background(), controller, &bytes.Buffer{}, &bytes.Buffer{}, []string{"doctor", "--json"})
	require.NoError(t, err)
	require.Equal(t, "doctor", controller.request.Action)
	require.Equal(t, true, controller.request.Options["json"])
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
