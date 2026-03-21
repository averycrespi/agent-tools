package cmd

import "github.com/spf13/cobra"

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the sandbox",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return svc.Start()
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
}
