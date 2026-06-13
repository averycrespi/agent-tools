package main

import (
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
	Short:         "Run local Pi workflows through pi-dispatcher",
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
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug output")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON where supported")
	rootCmd.PersistentFlags().StringVar(&workflowDir, "workflow-dir", "", "workflow definition directory")
	rootCmd.AddCommand(listCmd, showCmd, lintCmd, runCmd, psCmd, statusCmd, waitCmd, logsCmd, stopCmd, cleanupCmd, rmCmd, dashboardCmd, tokenCmd, supervisorCmd)
}

func Execute() error {
	return rootCmd.Execute()
}
