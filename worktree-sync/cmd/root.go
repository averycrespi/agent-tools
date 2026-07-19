package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
)

func exactArgs(message string, count int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != count {
			return fmt.Errorf("%s", message)
		}
		return nil
	}
}
func rangeArgs(message string, min, max int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < min || len(args) > max {
			return fmt.Errorf("%s", message)
		}
		return nil
	}
}

func run(controller app.Controller, action string, options func(*cobra.Command) map[string]any) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if controller == nil {
			return fmt.Errorf("worktree-sync service is unavailable")
		}
		values := map[string]any{}
		if options != nil {
			values = options(cmd)
		}
		output, err := controller.Execute(cmd.Context(), app.Request{Action: action, Args: args, Options: values})
		if output != "" {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), output)
		}
		return err
	}
}

func NewWTS(controller app.Controller) *cobra.Command {
	root := &cobra.Command{Use: "wts", Short: "Continuously mirror registered Git worktrees into isolated tmux sessions", SilenceErrors: true}
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)

	configCmd := &cobra.Command{Use: "config", Short: "View, edit, validate, and refresh configuration"}
	for _, spec := range []struct{ name, short, action string }{
		{"path", "Print the configuration file path", "config.path"}, {"edit", "Edit and validate configuration before saving", "config.edit"}, {"validate", "Validate the current configuration", "config.validate"}, {"refresh", "Add missing defaults to the current configuration", "config.refresh"},
	} {
		configCmd.AddCommand(&cobra.Command{Use: spec.name, Short: spec.short, Args: exactArgs(spec.name+" does not accept arguments", 0), RunE: run(controller, spec.action, nil)})
	}

	repoCmd := &cobra.Command{Use: "repo", Short: "Manage registered Git repositories"}
	var repoID string
	var roots []string
	repoAdd := &cobra.Command{Use: "add [path]", Short: "Register an existing primary Git worktree", Args: rangeArgs("repo add accepts at most one repository path", 0, 1)}
	repoAdd.Flags().StringVar(&repoID, "id", "", "stable tmux-safe repository ID")
	repoAdd.Flags().StringSliceVar(&roots, "worktree-root", nil, "existing allowed worktree root (repeatable)")
	repoAdd.RunE = run(controller, "repo.add", func(*cobra.Command) map[string]any { return map[string]any{"id": repoID, "roots": roots} })
	repoList := &cobra.Command{Use: "list", Short: "List registered Git repositories", Args: exactArgs("repo list does not accept arguments", 0), RunE: run(controller, "repo.list", nil)}
	repoRemove := &cobra.Command{Use: "remove <repository-id>", Short: "Unregister a repository without deleting Git worktrees", Args: exactArgs("repo remove requires exactly one repository ID", 1), RunE: run(controller, "repo.remove", nil)}
	repoCmd.AddCommand(repoAdd, repoList, repoRemove)

	worktreeCmd := &cobra.Command{Use: "worktree", Short: "Manage Git worktrees for registered repositories"}
	worktreePath := &cobra.Command{Use: "path <branch> [repository-id]", Short: "Print the existing or planned path for a branch", Args: rangeArgs("worktree path requires a branch and optional repository ID", 1, 2), RunE: run(controller, "worktree.path", nil)}
	var start string
	worktreeCreate := &cobra.Command{Use: "create <branch> [repository-id]", Short: "Create a Git worktree and reconcile its tmux window", Args: rangeArgs("worktree create requires a branch and optional repository ID", 1, 2)}
	worktreeCreate.Flags().StringVar(&start, "start", "", "start point for a new local branch")
	worktreeCreate.RunE = run(controller, "worktree.create", func(*cobra.Command) map[string]any { return map[string]any{"start": start} })
	var forceRemove, deleteBranch, forceDeleteBranch bool
	worktreeRemove := &cobra.Command{Use: "remove <path-or-branch> [repository-id]", Aliases: []string{"rm"}, Short: "Remove a Git worktree and optionally delete its branch", Args: rangeArgs("worktree remove requires a path or branch and optional repository ID", 1, 2)}
	worktreeRemove.Flags().BoolVar(&forceRemove, "force", false, "pass --force to git worktree remove")
	worktreeRemove.Flags().BoolVar(&deleteBranch, "delete-branch", false, "safely delete the local branch after removal")
	worktreeRemove.Flags().BoolVar(&forceDeleteBranch, "force-delete-branch", false, "force-delete the local branch after removal")
	worktreeRemove.RunE = run(controller, "worktree.remove", func(*cobra.Command) map[string]any {
		return map[string]any{"force": forceRemove, "delete_branch": deleteBranch, "force_delete_branch": forceDeleteBranch}
	})
	var rerun bool
	setup := &cobra.Command{Use: "setup <worktree> [repository-id]", Short: "Run configured copy and setup actions", Args: rangeArgs("worktree setup requires a path and optional repository ID", 1, 2)}
	setup.Flags().BoolVar(&rerun, "rerun", false, "rerun the configured action definition")
	setup.RunE = run(controller, "worktree.setup", func(*cobra.Command) map[string]any { return map[string]any{"rerun": rerun} })
	var relaunch bool
	launch := &cobra.Command{Use: "launch <worktree> [repository-id]", Short: "Run the configured command in the worktree's tmux window", Args: rangeArgs("worktree launch requires a path and optional repository ID", 1, 2)}
	launch.Flags().BoolVar(&relaunch, "rerun", false, "rerun the configured launch definition")
	launch.RunE = run(controller, "worktree.launch", func(*cobra.Command) map[string]any { return map[string]any{"rerun": relaunch} })
	worktreeCmd.AddCommand(worktreePath, worktreeCreate, worktreeRemove, setup, launch)

	attach := &cobra.Command{Use: "attach [repository-id]", Short: "Attach to a repository's managed tmux session", Args: rangeArgs("attach accepts at most one repository ID", 0, 1), RunE: run(controller, "attach", nil)}
	var jsonOutput bool
	status := &cobra.Command{Use: "status [repository-id]", Short: "Show worktree, tmux, and action status", Args: rangeArgs("status accepts at most one repository ID", 0, 1)}
	status.Flags().BoolVar(&jsonOutput, "json", false, "emit stable versioned JSON")
	status.RunE = run(controller, "status", func(*cobra.Command) map[string]any { return map[string]any{"json": jsonOutput} })
	reconcile := &cobra.Command{Use: "reconcile [repository-id]", Short: "Synchronize Git worktrees with managed tmux windows", Args: rangeArgs("reconcile accepts at most one repository ID", 0, 1), RunE: run(controller, "reconcile", nil)}
	var pruneID, orphanID string
	cleanup := &cobra.Command{Use: "cleanup", Short: "Preview or perform explicit Git and tmux cleanup", Args: exactArgs("cleanup does not accept positional arguments", 0)}
	cleanup.Flags().StringVar(&pruneID, "prune-git", "", "revalidate and prune one registered repository")
	cleanup.Flags().StringVar(&orphanID, "remove-orphaned-tmux", "", "re-snapshot and remove owned orphan tmux resources for one repository")
	cleanup.RunE = run(controller, "cleanup", func(*cobra.Command) map[string]any {
		return map[string]any{"prune_git": pruneID, "remove_orphaned_tmux": orphanID}
	})

	daemonCmd := &cobra.Command{Use: "daemon", Short: "Manage the macOS per-user LaunchAgent"}
	for _, spec := range []struct{ name, short string }{
		{"install", "Install or update the per-user LaunchAgent"},
		{"uninstall", "Unload and remove the per-user LaunchAgent"},
		{"start", "Start the installed per-user LaunchAgent"},
		{"stop", "Stop the installed per-user LaunchAgent"},
		{"status", "Show the per-user LaunchAgent status"},
	} {
		daemonCmd.AddCommand(&cobra.Command{Use: spec.name, Short: spec.short, Args: exactArgs("daemon "+spec.name+" does not accept arguments", 0), RunE: run(controller, "daemon."+spec.name, nil)})
	}
	root.AddCommand(configCmd, repoCmd, worktreeCmd, attach, status, reconcile, cleanup, daemonCmd)
	return root
}

func ExecuteWTS(ctx context.Context, controller app.Controller, stdout, stderr io.Writer, args []string) error {
	root := NewWTS(controller)
	root.SetContext(ctx)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	return root.Execute()
}
