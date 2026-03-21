package cmd

import (
	"log/slog"
	"os"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/config"
	sbexec "github.com/averycrespi/agent-tools/sandbox-manager/internal/exec"
	"github.com/averycrespi/agent-tools/sandbox-manager/internal/lima"
	"github.com/averycrespi/agent-tools/sandbox-manager/internal/sandbox"
	"github.com/spf13/cobra"
)

var (
	verbose bool
	svc     *sandbox.Service
	logger  *slog.Logger
)

var rootCmd = &cobra.Command{
	Use:           "sb",
	Short:         "Manage a Lima VM sandbox",
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

		runner := sbexec.NewOSRunner()
		limaClient := lima.NewClient(runner)
		svc = sandbox.NewService(limaClient, cfg, logger)

		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug output")
	rootCmd.AddCommand(completionCmd)
}

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish]",
	Short: "Generate shell completions",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return rootCmd.GenBashCompletion(os.Stdout)
		case "zsh":
			return rootCmd.GenZshCompletion(os.Stdout)
		case "fish":
			return rootCmd.GenFishCompletion(os.Stdout, true)
		default:
			return cmd.Usage()
		}
	},
}

func Execute() error {
	return rootCmd.Execute()
}
