package main

import (
	"log/slog"
	"os"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	"github.com/spf13/cobra"
)

var (
	verbose bool
	jsonOut bool
	logger  *slog.Logger
	cfg     config.Config
)

var rootCmd = &cobra.Command{
	Use:           "pd",
	Short:         "Launch and manage background Pi agent runs",
	SilenceUsage:  true,
	SilenceErrors: false,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		level := slog.LevelWarn
		if verbose {
			level = slog.LevelDebug
		}
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
		loaded, err := config.Load()
		if err != nil {
			return err
		}
		cfg = loaded
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug output")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON where supported")
	rootCmd.AddCommand(configCmd, runCmd, listCmd, statusCmd, logsCmd, steerCmd, followupCmd, stopCmd, rmCmd, tokenCmd, dashboardCmd, supervisorCmd)
}

func Execute() error {
	return rootCmd.Execute()
}
