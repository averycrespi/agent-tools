package main

import (
	"github.com/averycrespi/agent-tools/agent-mailbox/internal/config"
	"github.com/spf13/cobra"
)

var dbPath string

var rootCmd = &cobra.Command{
	Use:           "agent-mailbox",
	Short:         "Durable local mailbox for coding agents",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&dbPath, "db-path", "", "SQLite database path")
	rootCmd.AddCommand(sendCmd, listCmd, readCmd, ackCmd, resolveCmd, dashboardCmd, mcpCmd)
}

func Execute() error { return rootCmd.Execute() }

func activeDBPath() string {
	if dbPath != "" {
		return dbPath
	}
	return config.DefaultDBPath()
}
