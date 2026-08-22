package main

import "github.com/spf13/cobra"

func newRootCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "mcp-gateway",
		Short:         "Locally secure, deny-by-default MCP gateway",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
}
