package main

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

var (
	verbose bool
	jsonOut bool
	logger  *slog.Logger
)

var rootCmd = &cobra.Command{
	Use:           "po",
	Short:         "Run local Pi workflows through pi-dispatcher",
	SilenceUsage:  true,
	SilenceErrors: false,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		level := slog.LevelWarn
		if verbose {
			level = slog.LevelDebug
		}
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug output")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON where supported")
	rootCmd.PersistentFlags().StringVar(&workflowDir, "workflow-dir", "", "workflow definition directory")
	rootCmd.AddCommand(listCmd, showCmd, lintCmd)
}

func Execute() error {
	return rootCmd.Execute()
}
