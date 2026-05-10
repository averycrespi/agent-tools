package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/averycrespi/agent-tools/agent-dispatch/internal/config"
	"github.com/spf13/cobra"
)

var (
	verbose bool
	jsonOut bool
	logger  *slog.Logger
	cfg     config.Config
)

var rootCmd = &cobra.Command{
	Use:           "ad",
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
	rootCmd.AddCommand(configCmd, runCmd, listCmd, statusCmd, logsCmd, eventsCmd, attachCmd, steerCmd, followupCmd, followUpCmd, stopCmd, rmCmd, templateCmd, supervisorCmd)
}

func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}
