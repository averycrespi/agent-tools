package main

import "github.com/spf13/cobra"

func newRootCommand() *cobra.Command {
	return &cobra.Command{
		Use:           "pi-session-analyzer",
		Short:         "Analyze local Pi session logs",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
}
