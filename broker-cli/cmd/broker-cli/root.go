package main

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:          "broker-cli",
	Short:        "CLI frontend for the MCP broker",
	SilenceUsage: true,
	Long: `broker-cli connects to an MCP broker and exposes its tools as CLI subcommands.

Environment:
  MCP_BROKER_ENDPOINT    Broker URL (required)
  MCP_BROKER_AUTH_TOKEN  Bearer token (required)`,
}
