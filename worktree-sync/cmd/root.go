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
	root := &cobra.Command{Use: "wts", Short: "Continuously mirror registered Git worktrees into isolated tmux sessions", SilenceErrors: true, SilenceUsage: true}
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)

	configCmd := &cobra.Command{Use: "config", Short: "View, edit, validate, and refresh configuration"}
	for _, spec := range []struct{ name, short, action string }{
		{"path", "Print the configuration file path", "config.path"}, {"edit", "Edit and validate configuration before saving", "config.edit"}, {"validate", "Validate the current configuration", "config.validate"}, {"refresh", "Migrate configuration and add missing defaults", "config.refresh"},
	} {
		configCmd.AddCommand(&cobra.Command{Use: spec.name, Short: spec.short, Args: exactArgs(spec.name+" does not accept arguments", 0), RunE: run(controller, spec.action, nil)})
	}

	repoCmd := &cobra.Command{Use: "repo", Short: "Manage registered Git repositories"}
	var repoID, creationRoot string
	var allowedRoots []string
	repoAdd := &cobra.Command{Use: "add [path]", Short: "Register an existing primary Git worktree", Args: rangeArgs("repo add accepts at most one repository path", 0, 1)}
	repoAdd.Flags().StringVar(&repoID, "id", "", "stable tmux-safe repository ID")
	repoAdd.Flags().StringVar(&creationRoot, "worktree-root", "", "existing root for worktrees created by wts")
	repoAdd.Flags().StringSliceVar(&allowedRoots, "allowed-worktree-root", nil, "additional existing allowed root; repeatable and overrides defaults")
	repoAdd.RunE = run(controller, "repo.add", func(*cobra.Command) map[string]any {
		return map[string]any{"id": repoID, "root": creationRoot, "roots": allowedRoots}
	})
	repoList := &cobra.Command{Use: "list", Short: "List registered Git repositories", Args: exactArgs("repo list does not accept arguments", 0), RunE: run(controller, "repo.list", nil)}
	repoRemove := &cobra.Command{Use: "remove <repository-id>", Short: "Unregister a repository without deleting Git worktrees", Args: exactArgs("repo remove requires exactly one repository ID", 1), RunE: run(controller, "repo.remove", nil)}
	repoCmd.AddCommand(repoAdd, repoList, repoRemove)

	worktreeCmd := &cobra.Command{Use: "worktree", Short: "Manage Git worktrees for registered repositories"}
	var pathRepoID string
	worktreePath := &cobra.Command{Use: "path <branch>", Short: "Print the existing or planned path for a branch", Args: exactArgs("worktree path requires exactly one branch", 1)}
	worktreePath.Flags().StringVar(&pathRepoID, "repo-id", "", "registered repository ID (defaults to current directory)")
	worktreePath.RunE = run(controller, "worktree.path", func(*cobra.Command) map[string]any { return map[string]any{"repo_id": pathRepoID} })
	var branchFrom, createRepoID string
	worktreeCreate := &cobra.Command{Use: "create <branch>", Short: "Create a Git worktree and reconcile its tmux window", Args: exactArgs("worktree create requires exactly one branch", 1)}
	worktreeCreate.Flags().StringVar(&branchFrom, "from", "", "revision from which to create a new branch")
	worktreeCreate.Flags().StringVar(&createRepoID, "repo-id", "", "registered repository ID (defaults to current directory)")
	worktreeCreate.RunE = run(controller, "worktree.create", func(*cobra.Command) map[string]any {
		return map[string]any{"from": branchFrom, "repo_id": createRepoID}
	})
	var removeRepoID string
	var forceRemove, deleteBranch, forceDeleteBranch bool
	worktreeRemove := &cobra.Command{Use: "remove <path-or-branch>", Aliases: []string{"rm"}, Short: "Remove a Git worktree and optionally delete its branch", Args: exactArgs("worktree remove requires exactly one path or branch", 1)}
	worktreeRemove.Flags().StringVar(&removeRepoID, "repo-id", "", "registered repository ID (defaults to current directory)")
	worktreeRemove.Flags().BoolVar(&forceRemove, "force", false, "pass --force to git worktree remove")
	worktreeRemove.Flags().BoolVar(&deleteBranch, "delete-branch", false, "safely delete the local branch after removal")
	worktreeRemove.Flags().BoolVar(&forceDeleteBranch, "force-delete-branch", false, "force-delete the local branch after removal")
	worktreeRemove.RunE = run(controller, "worktree.remove", func(*cobra.Command) map[string]any {
		return map[string]any{"force": forceRemove, "delete_branch": deleteBranch, "force_delete_branch": forceDeleteBranch, "repo_id": removeRepoID}
	})
	var setupRepoID string
	var rerun bool
	setup := &cobra.Command{Use: "setup <worktree>", Short: "Run configured copy and setup actions", Args: exactArgs("worktree setup requires exactly one worktree", 1)}
	setup.Flags().StringVar(&setupRepoID, "repo-id", "", "registered repository ID (defaults to current directory)")
	setup.Flags().BoolVar(&rerun, "rerun", false, "rerun the configured action definition")
	setup.RunE = run(controller, "worktree.setup", func(*cobra.Command) map[string]any { return map[string]any{"rerun": rerun, "repo_id": setupRepoID} })
	var launchRepoID string
	var relaunch bool
	launch := &cobra.Command{
		Use:   "launch <worktree>",
		Short: "Run the configured command in the worktree's tmux window",
		Long: `Run the repository's configured launch command in the worktree's
existing managed tmux window.

The command is typed into the window's interactive shell and Enter is sent.
This does not create a window or attach to the tmux session. A successful or
failed definition is recorded; use --rerun to attempt the same definition again.`,
		Example: `  wts worktree launch feature/auth
  wts worktree launch feature/auth --repo-id api
  wts worktree launch feature/auth --rerun`,
		Args: exactArgs("worktree launch requires exactly one worktree", 1),
	}
	launch.Flags().StringVar(&launchRepoID, "repo-id", "", "registered repository ID (defaults to current directory)")
	launch.Flags().BoolVar(&relaunch, "rerun", false, "rerun the configured launch definition")
	launch.RunE = run(controller, "worktree.launch", func(*cobra.Command) map[string]any { return map[string]any{"rerun": relaunch, "repo_id": launchRepoID} })
	worktreeCmd.AddCommand(worktreePath, worktreeCreate, worktreeRemove, setup, launch)

	var attachRepoID string
	attach := &cobra.Command{Use: "attach", Short: "Attach to a repository's managed tmux session", Args: exactArgs("attach does not accept arguments", 0)}
	attach.Flags().StringVar(&attachRepoID, "repo-id", "", "registered repository ID (defaults to current directory)")
	attach.RunE = run(controller, "attach", func(*cobra.Command) map[string]any { return map[string]any{"repo_id": attachRepoID} })
	var statusRepoID string
	var jsonOutput, statusAll bool
	status := &cobra.Command{Use: "status", Short: "Show worktree, tmux, and action status", Args: exactArgs("status does not accept arguments", 0)}
	status.Flags().StringVar(&statusRepoID, "repo-id", "", "registered repository ID (defaults to current directory)")
	status.Flags().BoolVar(&statusAll, "all", false, "show all registered repositories")
	status.Flags().BoolVar(&jsonOutput, "json", false, "emit stable versioned JSON")
	status.PreRunE = func(*cobra.Command, []string) error {
		if statusRepoID != "" && statusAll {
			return fmt.Errorf("choose only one of --repo-id or --all")
		}
		return nil
	}
	status.RunE = run(controller, "status", func(*cobra.Command) map[string]any {
		return map[string]any{"json": jsonOutput, "repo_id": statusRepoID, "all": statusAll}
	})
	var reconcileRepoID string
	var reconcileAll bool
	reconcile := &cobra.Command{Use: "reconcile", Short: "Synchronize Git worktrees with managed tmux windows", Args: exactArgs("reconcile does not accept arguments", 0)}
	reconcile.Flags().StringVar(&reconcileRepoID, "repo-id", "", "registered repository ID (defaults to current directory)")
	reconcile.Flags().BoolVar(&reconcileAll, "all", false, "reconcile all registered repositories")
	reconcile.PreRunE = func(*cobra.Command, []string) error {
		if reconcileRepoID != "" && reconcileAll {
			return fmt.Errorf("choose only one of --repo-id or --all")
		}
		return nil
	}
	reconcile.RunE = run(controller, "reconcile", func(*cobra.Command) map[string]any {
		return map[string]any{"repo_id": reconcileRepoID, "all": reconcileAll}
	})
	var pruneID, orphanID string
	cleanup := &cobra.Command{
		Use:   "cleanup",
		Short: "Inspect or explicitly remove stale Git and tmux state",
		Long: `Inspect cleanup status or explicitly remove stale state.

Without flags, cleanup is reporting-only and makes no changes.

--prune-git revalidates one registered repository and runs
"git worktree prune" only when Git reports stale worktree metadata.

--remove-orphaned-tmux revalidates Git and tmux state, then removes only
wts-owned worktree windows whose identities are no longer desired.

Cleanup never deletes branches, live worktree directories, manual tmux
windows, or foreign tmux resources. Incomplete snapshots fail closed.`,
		Example: `  wts cleanup
  wts cleanup --prune-git my-repo
  wts cleanup --remove-orphaned-tmux my-repo
  wts cleanup --prune-git my-repo --remove-orphaned-tmux my-repo`,
		Args: exactArgs("cleanup does not accept positional arguments", 0),
	}
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
		{"restart", "Restart the installed per-user LaunchAgent"},
		{"status", "Show the per-user LaunchAgent status"},
	} {
		daemonCmd.AddCommand(&cobra.Command{Use: spec.name, Short: spec.short, Args: exactArgs("daemon "+spec.name+" does not accept arguments", 0), RunE: run(controller, "daemon."+spec.name, nil)})
	}
	var logLines int
	var followLogs bool
	logs := &cobra.Command{
		Use:   "logs",
		Short: "Show recent LaunchAgent logs",
		Long: `Show stdout and stderr logs for the worktree-sync LaunchAgent.

By default, the last 100 lines are shown. Use --follow to stream new
entries until interrupted. launchd does not rotate these files.`,
		Args: exactArgs("daemon logs does not accept arguments", 0),
		PreRunE: func(*cobra.Command, []string) error {
			if logLines < 1 {
				return fmt.Errorf("--lines must be positive")
			}
			return nil
		},
	}
	logs.Flags().IntVarP(&logLines, "lines", "n", 100, "number of recent lines to show")
	logs.Flags().BoolVarP(&followLogs, "follow", "f", false, "stream new log entries until interrupted")
	logs.RunE = run(controller, "daemon.logs", func(*cobra.Command) map[string]any { return map[string]any{"lines": logLines, "follow": followLogs} })
	daemonCmd.AddCommand(logs)
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
