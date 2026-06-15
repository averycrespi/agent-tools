package main

import (
	"fmt"
	"os"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/config"
	"github.com/spf13/cobra"
)

var (
	verbose bool
	jsonOut bool
	cfg     config.Config
)

var rootCmd = &cobra.Command{
	Use:           "po",
	Short:         "Coordinate durable Pi workflows through pd",
	SilenceUsage:  true,
	SilenceErrors: false,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		loaded, err := config.Load()
		if err != nil {
			return err
		}
		if workflowDir != "" {
			loaded.WorkflowDir = workflowDir
		}
		cfg = loaded
		if !isConfigCommand(cmd) && !isMCPCommand(cmd) {
			if err := os.MkdirAll(cfg.WorkflowDir, 0o750); err != nil {
				return fmt.Errorf("create workflow directory %s: %w", cfg.WorkflowDir, err)
			}
		}
		return nil
	},
}

func isConfigCommand(cmd *cobra.Command) bool {
	return commandIsOrDescendsFrom(cmd, configCmd)
}

func isMCPCommand(cmd *cobra.Command) bool {
	return commandIsOrDescendsFrom(cmd, mcpCmd)
}

func commandIsOrDescendsFrom(cmd *cobra.Command, target *cobra.Command) bool {
	for current := cmd; current != nil; current = current.Parent() {
		if current == target {
			return true
		}
	}
	return false
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug output")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON where supported")
	rootCmd.PersistentFlags().StringVar(&workflowDir, "workflow-dir", "", "workflow definition directory")
	rootCmd.AddCommand(configCmd, listCmd, showCmd, lintCmd, runCmd, psCmd, statusCmd, waitCmd, logsCmd, stopCmd, cleanupCmd, rmCmd, dashboardCmd, tokenCmd, supervisorCmd, mcpCmd)
}

func Execute() error {
	return rootCmd.Execute()
}
