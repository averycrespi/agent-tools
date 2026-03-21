package cmd

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/worktree-manager/internal/config"
	wtexec "github.com/averycrespi/agent-tools/worktree-manager/internal/exec"
	"github.com/averycrespi/agent-tools/worktree-manager/internal/git"
	"github.com/averycrespi/agent-tools/worktree-manager/internal/tmux"
	"github.com/averycrespi/agent-tools/worktree-manager/internal/workspace"
)

var (
	verbose bool
	svc     *workspace.Service
	logger  *slog.Logger
)

var rootCmd = &cobra.Command{
	Use:           "wt",
	Short:         "Manage git worktrees",
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		level := slog.LevelWarn
		if verbose {
			level = slog.LevelDebug
		}
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

		cfg, err := config.Load()
		if err != nil {
			return err
		}

		runner := wtexec.NewOSRunner()
		tc := tmux.NewClient(runner)
		tc.TmuxEnv = os.Getenv("TMUX")

		svc = workspace.NewService(
			git.NewClient(runner),
			tc,
			cfg,
			logger,
			runner,
		)
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "show verbose output")
}

func Execute() error {
	return rootCmd.Execute()
}

func completeBranches(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	runner := wtexec.NewOSRunner()
	branches, err := git.NewClient(runner).ListBranches(cwd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return branches, cobra.ShellCompDirectiveNoFileComp
}
