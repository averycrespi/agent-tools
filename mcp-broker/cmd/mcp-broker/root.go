package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:           "mcp-broker",
	Short:         "MCP proxy with policy rules, approval, and audit logging",
	SilenceUsage:  true,
	SilenceErrors: false,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", fmt.Sprintf("config file (default %q)", config.ConfigPath()))
}

func configPath() string {
	if cfgFile != "" {
		return cfgFile
	}
	return config.ConfigPath()
}
