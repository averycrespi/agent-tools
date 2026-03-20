package cmd

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:           "sb",
	Short:         "Manage Lima VM sandboxes",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	return rootCmd.Execute()
}
