package cmd

import "github.com/spf13/cobra"

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the sandbox",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return svc.Stop()
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
